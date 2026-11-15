package supervisor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func event(name, payload string) string {
	return fmt.Sprintf("ver:3.0 server:supervisor serial:1 pool:watchdog poolserial:1 eventname:%s len:%d\n%s", name, len(payload), payload)
}

func TestListenStopsOnlyForCriticalFatalPrograms(t *testing.T) {
	input := event("PROCESS_STATE_FATAL", "processname:isc-stork-agent groupname:isc-stork-agent from_state:BACKOFF") +
		event("PROCESS_STATE_FATAL", "processname:samba groupname:samba from_state:BACKOFF")
	var output bytes.Buffer
	var reasons []string
	err := Listen(strings.NewReader(input), &output, &Watchdog{Now: time.Now}, func(reason string) {
		reasons = append(reasons, reason)
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Listen() error = %v, want EOF", err)
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "samba") {
		t.Fatalf("stop reasons = %q", reasons)
	}
	if got := output.String(); got != "READY\nRESULT 2\nOKREADY\nRESULT 2\nOKREADY\n" {
		t.Fatalf("protocol output = %q", got)
	}
}

func TestWatchdogDetectsCrashLoopsOfProgramsThatNeverReachFatal(t *testing.T) {
	now := time.Unix(0, 0)
	watchdog := &Watchdog{Now: func() time.Time { return now }}
	unexpected := map[string]string{"processname": "kea-dhcp4", "expected": "0"}

	for range CrashLoopExits - 1 {
		if reason := watchdog.Observe("PROCESS_STATE_EXITED", unexpected); reason != "" {
			t.Fatalf("stopped too early: %s", reason)
		}
		now = now.Add(10 * time.Second)
	}
	if reason := watchdog.Observe("PROCESS_STATE_EXITED", map[string]string{"processname": "kea-dhcp4", "expected": "1"}); reason != "" {
		t.Fatalf("expected exit (DHCP disabled) stopped the container: %s", reason)
	}
	if reason := watchdog.Observe("PROCESS_STATE_EXITED", map[string]string{"processname": "smb-prometheus-endpoint", "expected": "0"}); reason != "" {
		t.Fatalf("monitoring program stopped the container: %s", reason)
	}
	if reason := watchdog.Observe("PROCESS_STATE_EXITED", unexpected); !strings.Contains(reason, "kea-dhcp4") {
		t.Fatalf("crash loop not detected, reason = %q", reason)
	}

	spaced := &Watchdog{Now: func() time.Time { return now }}
	for range CrashLoopExits {
		now = now.Add(CrashLoopWindow)
		if reason := spaced.Observe("PROCESS_STATE_EXITED", unexpected); reason != "" {
			t.Fatalf("occasional restarts treated as a crash loop: %s", reason)
		}
	}
}
