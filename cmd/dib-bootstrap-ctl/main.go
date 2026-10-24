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
	corePending := os.Getenv("DIB_BOOTSTRAP_PENDING") == "TRUE"
	storkPending := os.Getenv("DIB_STORK_BOOTSTRAP_PENDING") == "TRUE"
	if !corePending && !storkPending {
		logger.Infof("one-time bootstrap is complete; refreshing network credentials")
	}

	pgHost := os.Getenv("STORK_DATABASE_HOST")
	if pgHost == "" {
		pgHost = "127.0.0.1"
	}
	pgPort := os.Getenv("STORK_DATABASE_PORT")
	if pgPort == "" {
		pgPort = "5432"
	}

	dependencies := bootstrapDependencies(requiredEnv("IP"), corePending, storkPending, pgHost, pgPort)
	for _, dependency := range dependencies {
		logger.Infof("waiting for %s", dependency.name)
		if err := waitForTCP(ctx, dependency.name, dependency.address, timeout); err != nil {
			return err
		}
	}

	if corePending {
		logger.Infof("running identity bootstrap")
		if err := runCommand(ctx, "/usr/local/bin/dib-identity-core-ctl", "bootstrap"); err != nil {
			return err
		}
	}

	logger.Infof("running network bootstrap")
	if err := runCommand(ctx, "/usr/local/bin/dib-network-core-ctl", "bootstrap"); err != nil {
		return err
	}
	if corePending {
		if err := os.WriteFile("/var/lib/samba/.dib-bootstrap-complete", nil, 0o600); err != nil {
			return fmt.Errorf("mark core bootstrap complete: %w", err)
		}
	}

	if storkPending {
		logger.Infof("running Stork agent bootstrap")
		if err := runCommand(ctx, "/usr/local/bin/dib-observability-ctl", "bootstrap"); err != nil {
			return err
		}
		if err := os.WriteFile("/var/lib/stork-agent/.dib-bootstrap-complete", nil, 0o600); err != nil {
			return fmt.Errorf("mark Stork bootstrap complete: %w", err)
		}
	}
	return nil
}

type dependency struct {
	name    string
	address string
}

func bootstrapDependencies(ip string, corePending, storkPending bool, pgHost, pgPort string) []dependency {
	dependencies := []dependency{
		{name: "Samba LDAP", address: "127.0.0.1:389"},
		{name: "Samba KDC", address: "127.0.0.1:88"},
	}
	if corePending {
		dependencies = append([]dependency{{name: "BIND", address: net.JoinHostPort(ip, "5353")}}, dependencies...)
	}
	if storkPending {
		dependencies = append(dependencies, dependency{name: "PostgreSQL", address: net.JoinHostPort(pgHost, pgPort)})
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
