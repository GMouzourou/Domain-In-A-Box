package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	platformlog "github.com/GMouzourou/domain-in-a-box/internal/platform/log"
	"github.com/GMouzourou/domain-in-a-box/internal/platform/supervisor"
	"github.com/GMouzourou/domain-in-a-box/internal/platform/wait"
)

const (
	defaultTimeout = 120 * time.Second
	coreReadyFile  = "/run/dib-core-ready"
	coreMarker     = "/var/lib/samba/.dib-bootstrap-complete"
	storkMarker    = "/var/lib/stork-agent/.dib-bootstrap-complete"
)

func main() {
	var err error
	switch {
	case len(os.Args) == 1:
		err = run(context.Background())
	case len(os.Args) == 2 && os.Args[1] == "stork":
		err = runStork(context.Background())
	case len(os.Args) == 2 && os.Args[1] == "watchdog":
		err = runWatchdog()
	case len(os.Args) >= 2 && os.Args[1] == "start":
		err = start(os.Args[2:])
	default:
		err = fmt.Errorf("usage: dib-bootstrap-ctl [start [command...]|stork|watchdog]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runWatchdog is a supervisord event listener. Stdout carries the listener
// protocol, so diagnostics go to stderr.
func runWatchdog() error {
	logger := platformlog.New("watchdog").WithWriter(os.Stderr)
	stopping := false
	return supervisor.Listen(os.Stdin, os.Stdout, &supervisor.Watchdog{Now: time.Now}, func(reason string) {
		if stopping {
			return
		}
		stopping = true
		logger.Errorf("%s; stopping the controller so the container runtime restarts it", reason)
		if err := syscall.Kill(os.Getppid(), syscall.SIGTERM); err != nil {
			logger.Errorf("signal supervisord: %v", err)
		}
	})
}

func run(ctx context.Context) error {
	logger := platformlog.New("bootstrap")
	timeout, err := bootstrapTimeout()
	if err != nil {
		return err
	}
	corePending := os.Getenv("DIB_BOOTSTRAP_PENDING") == "TRUE"
	if !corePending {
		logger.Infof("one-time bootstrap is complete; refreshing network credentials")
	}

	return bootstrapCore(ctx, requiredEnv("IP"), corePending, timeout, coreOperations{
		wait:    waitForTCP,
		command: runCommand,
		write: func(path string) error {
			return os.WriteFile(path, nil, 0o600)
		},
	})
}

type coreOperations struct {
	wait    func(context.Context, string, string, time.Duration) error
	command func(context.Context, string, ...string) error
	write   func(string) error
}

func bootstrapCore(ctx context.Context, ip string, corePending bool, timeout time.Duration, ops coreOperations) error {
	logger := platformlog.New("bootstrap")
	dependencies := bootstrapDependencies(ip, corePending)
	for _, dependency := range dependencies {
		logger.Infof("waiting for %s", dependency.name)
		if err := ops.wait(ctx, dependency.name, dependency.address, timeout); err != nil {
			return err
		}
	}

	if corePending {
		logger.Infof("running identity bootstrap")
		if err := ops.command(ctx, "/usr/local/bin/dib-identity-core-ctl", "bootstrap"); err != nil {
			return err
		}
	}

	logger.Infof("running network bootstrap")
	if err := ops.command(ctx, "/usr/local/bin/dib-network-core-ctl", "bootstrap"); err != nil {
		return err
	}
	if corePending {
		if err := ops.write(coreMarker); err != nil {
			return fmt.Errorf("mark core bootstrap complete: %w", err)
		}
	}
	if err := ops.write(coreReadyFile); err != nil {
		return fmt.Errorf("mark current core startup ready: %w", err)
	}
	return nil
}

func runStork(ctx context.Context) error {
	if os.Getenv("DIB_STORK_BOOTSTRAP_PENDING") != "TRUE" {
		return nil
	}
	timeout, err := bootstrapTimeout()
	if err != nil {
		return err
	}
	logger := platformlog.New("stork-bootstrap")
	return retryStork(ctx, 10*time.Second, func(ctx context.Context) error {
		return bootstrapStork(ctx, storkOperations{
			waitCore: func(ctx context.Context) error {
				waitCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				return wait.Until(waitCtx, "core bootstrap", int(timeout/time.Second), time.Second, func() error {
					_, err := os.Stat(coreReadyFile)
					return err
				})
			},
			command: runCommand,
			write: func(path string) error {
				return os.WriteFile(path, nil, 0o600)
			},
		})
	}, func(err error) {
		logger.Infof("Stork bootstrap failed; retrying: %v", err)
	})
}

type storkOperations struct {
	waitCore func(context.Context) error
	command  func(context.Context, string, ...string) error
	write    func(string) error
}

func bootstrapStork(ctx context.Context, ops storkOperations) error {
	if err := ops.waitCore(ctx); err != nil {
		return err
	}
	if err := ops.command(ctx, "/usr/local/bin/dib-observability-ctl", "bootstrap"); err != nil {
		return err
	}
	if err := ops.write(storkMarker); err != nil {
		return fmt.Errorf("mark Stork bootstrap complete: %w", err)
	}
	return nil
}

func retryStork(ctx context.Context, delay time.Duration, attempt func(context.Context) error, report func(error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := attempt(ctx)
		if err == nil {
			return nil
		}
		report(err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type dependency struct {
	name    string
	address string
}

func bootstrapDependencies(ip string, corePending bool) []dependency {
	dependencies := []dependency{
		{name: "Samba LDAP", address: "127.0.0.1:389"},
		{name: "Samba KDC", address: "127.0.0.1:88"},
	}
	if corePending {
		dependencies = append([]dependency{{name: "BIND", address: net.JoinHostPort(ip, "5353")}}, dependencies...)
	}
	return dependencies
}

func runCommand(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	return nil
}

func waitForTCP(ctx context.Context, name, address string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return wait.Until(waitCtx, name, int(timeout/time.Second), time.Second, func() error {
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			return err
		}
		return connection.Close()
	})
}

func bootstrapTimeout() (time.Duration, error) {
	value := os.Getenv("DIB_BOOTSTRAP_TIMEOUT_SECONDS")
	if value == "" {
		return defaultTimeout, nil
	}

	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 1 {
		return 0, fmt.Errorf("DIB_BOOTSTRAP_TIMEOUT_SECONDS must be a positive integer")
	}
	return time.Duration(seconds) * time.Second, nil
}

func requiredEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		fmt.Fprintf(os.Stderr, "environment variable %s is not set\n", key)
		os.Exit(1)
	}
	return value
}
