package log

import (
	"fmt"
	"io"
	stdlog "log"
	"os"
)

// Logger provides consistent structured-ish logs for all orchestration binaries.
type Logger struct {
	prefix string
	base   *stdlog.Logger
}

func New(prefix string) *Logger {
	return &Logger{
		prefix: prefix,
		base:   stdlog.New(os.Stdout, "", stdlog.LstdFlags),
	}
}

func (l *Logger) WithWriter(w io.Writer) *Logger {
	copy := *l
	copy.base = stdlog.New(w, "", stdlog.LstdFlags)
	return &copy
}

func (l *Logger) Infof(format string, args ...any) {
	l.base.Printf("INFO [%s] %s", l.prefix, fmt.Sprintf(format, args...))
}

func (l *Logger) Warnf(format string, args ...any) {
	l.base.Printf("WARN [%s] %s", l.prefix, fmt.Sprintf(format, args...))
}

func (l *Logger) Errorf(format string, args ...any) {
	l.base.Printf("ERROR [%s] %s", l.prefix, fmt.Sprintf(format, args...))
}
