// Package supervisor implements a supervisord event listener that stops the
// controller container when a critical service cannot be kept running, so the
// Docker restart policy or Kubernetes restarts it.
package supervisor

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// CriticalPrograms are the supervisord programs the controller cannot serve
// without. Monitoring and synchronization programs are deliberately excluded.
var CriticalPrograms = map[string]bool{
	"chrony":    true,
	"samba":     true,
	"bind":      true,
	"kea-dhcp4": true,
	"dhcp-ddns": true,
}

const (
	// CrashLoopExits unexpected exits within CrashLoopWindow count as a
	// crash loop. Programs started with startsecs=0 never reach FATAL.
	CrashLoopExits  = 5
	CrashLoopWindow = 2 * time.Minute
)

// Watchdog decides when a critical program has failed permanently.
type Watchdog struct {
	Now   func() time.Time
	exits map[string][]time.Time
}

// Observe returns a non-empty reason when the event shows a critical program
// has failed permanently.
func (w *Watchdog) Observe(eventName string, payload map[string]string) string {
	program := payload["processname"]
	if !CriticalPrograms[program] {
		return ""
	}
	switch eventName {
	case "PROCESS_STATE_FATAL":
		return fmt.Sprintf("critical program %s entered FATAL after repeated start failures", program)
	case "PROCESS_STATE_EXITED":
		if payload["expected"] != "0" {
			return ""
		}
		if w.exits == nil {
			w.exits = map[string][]time.Time{}
		}
		now := w.Now()
		recent := []time.Time{now}
		for _, exit := range w.exits[program] {
			if now.Sub(exit) < CrashLoopWindow {
				recent = append(recent, exit)
			}
		}
		w.exits[program] = recent
		if len(recent) >= CrashLoopExits {
			return fmt.Sprintf("critical program %s exited unexpectedly %d times within %s", program, len(recent), CrashLoopWindow)
		}
	}
	return ""
}

// Listen speaks the supervisord event listener protocol on in and out until
// a critical failure is seen, then calls stop with the reason. Diagnostics must
// not be written to out, which carries the protocol.
func Listen(in io.Reader, out io.Writer, watchdog *Watchdog, stop func(reason string)) error {
	reader := bufio.NewReader(in)
	for {
		if _, err := io.WriteString(out, "READY\n"); err != nil {
			return err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		headers := parseTokens(line)
		length, err := strconv.Atoi(headers["len"])
		if err != nil {
			return fmt.Errorf("invalid supervisord event header %q", strings.TrimSpace(line))
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(reader, body); err != nil {
			return err
		}
		payloadLine, _, _ := strings.Cut(string(body), "\n")
		reason := watchdog.Observe(headers["eventname"], parseTokens(payloadLine))
		if _, err := io.WriteString(out, "RESULT 2\nOK"); err != nil {
			return err
		}
		if reason != "" {
			stop(reason)
		}
	}
}

func parseTokens(line string) map[string]string {
	tokens := map[string]string{}
	for _, field := range strings.Fields(line) {
		if key, value, found := strings.Cut(field, ":"); found {
			tokens[key] = value
		}
	}
	return tokens
}
