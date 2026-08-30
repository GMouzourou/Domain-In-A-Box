package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"

	platformlog "github.com/GMouzourou/domain-in-a-box/internal/platform/log"
	"github.com/GMouzourou/domain-in-a-box/internal/platform/wait"
)

const defaultTimeout = 120 * time.Second

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	logger := platformlog.New("bootstrap")
	timeout, err := bootstrapTimeout()
	if err != nil {
		return err
	}

	for _, dependency := range []struct {
		name    string
		address string
	}{
		{name: "BIND", address: net.JoinHostPort(requiredEnv("IP"), "5353")},
		{name: "Samba LDAP", address: "127.0.0.1:389"},
		{name: "Samba KDC", address: "127.0.0.1:88"},
		{name: "PostgreSQL", address: "127.0.0.1:5432"},
	} {
		logger.Infof("waiting for %s", dependency.name)
		if err := waitForTCP(ctx, dependency.name, dependency.address, timeout); err != nil {
			return err
		}
	}

	logger.Infof("running identity bootstrap")
	if err := runCommand(ctx, "/usr/local/bin/dib-identity-core-ctl", "bootstrap"); err != nil {
		return err
	}

	logger.Infof("running network bootstrap")
	if err := runCommand(ctx, "/usr/local/bin/dib-network-core-ctl", "bootstrap"); err != nil {
		return err
	}

	logger.Infof("running observability bootstrap")
	if err := runCommand(ctx, "/usr/local/bin/dib-observability-ctl", "bootstrap"); err != nil {
		return err
	}
	return nil
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
