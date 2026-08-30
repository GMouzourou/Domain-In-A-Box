package db

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	platformlog "github.com/GMouzourou/domain-in-a-box/internal/platform/log"
	"github.com/GMouzourou/domain-in-a-box/internal/platform/wait"
)

type Runner struct {
	log *platformlog.Logger
}

func NewRunner() *Runner {
	return &Runner{log: platformlog.New("db")}
}

func (r *Runner) Name() string { return "db" }

func (r *Runner) Configure(ctx context.Context) error {
	r.log.Infof("configuring PostgreSQL runtime directory")
	if err := os.MkdirAll("/run/postgresql", 0o775); err != nil {
		return fmt.Errorf("create PostgreSQL runtime directory: %w", err)
	}
	if err := os.Chmod("/run/postgresql", 0o775); err != nil {
		return fmt.Errorf("set PostgreSQL runtime directory permissions: %w", err)
	}
	if err := run(ctx, "chown", "postgres:postgres", "/run/postgresql"); err != nil {
		return fmt.Errorf("set PostgreSQL runtime directory ownership: %w", err)
	}

	version, err := postgresVersion()
	if err != nil {
		return err
	}

	if err := run(ctx, "pg_lsclusters", "-h"); err != nil {
		return fmt.Errorf("list PostgreSQL clusters: %w", err)
	}
	if clusterExists(version) {
		r.log.Infof("keeping existing PostgreSQL cluster %s/main", version)
		return nil
	}

	r.log.Infof("creating PostgreSQL cluster %s/main", version)
	if err := run(ctx, "pg_createcluster", version, "main", "--start-conf=manual"); err != nil {
		return fmt.Errorf("create PostgreSQL cluster %s/main: %w", version, err)
	}
	return nil
}

func (r *Runner) Bootstrap(context.Context) error {
	r.log.Infof("no database bootstrap actions required")
	return nil
}

func (r *Runner) Validate(ctx context.Context) error {
	version, err := postgresVersion()
	if err != nil {
		return err
	}
	if !clusterExists(version) {
		return fmt.Errorf("PostgreSQL cluster %s/main is missing", version)
	}
	return nil
}

func (r *Runner) Run(ctx context.Context) error {
	version, err := postgresVersion()
	if err != nil {
		return err
	}

	return run(ctx,
		filepath.Join("/usr/lib/postgresql", version, "bin/postgres"),
		"-D", filepath.Join("/var/lib/postgresql", version, "main"),
		"-c", "config_file="+filepath.Join("/etc/postgresql", version, "main", "postgresql.conf"),
	)
}

func (r *Runner) Health(ctx context.Context) error {
	return wait.Until(ctx, "PostgreSQL", 5, time.Second, func() error {
		return run(ctx, "pg_isready", "-q", "-d", "postgres")
	})
}

func postgresVersion() (string, error) {
	entries, err := os.ReadDir("/usr/lib/postgresql")
	if err != nil {
		return "", fmt.Errorf("read PostgreSQL installation directory: %w", err)
	}

	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			versions = append(versions, entry.Name())
		}
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("unable to determine PostgreSQL major version from /usr/lib/postgresql")
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[i], versions[j]) })
	return versions[len(versions)-1], nil
}

func clusterExists(version string) bool {
	output, err := exec.Command("pg_lsclusters", "-h").Output()
	if err != nil {
		return false
	}

	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == version && fields[1] == "main" {
			return true
		}
	}
	return false
}

func versionLess(left, right string) bool {
	leftMajor, leftErr := strconv.Atoi(left)
	rightMajor, rightErr := strconv.Atoi(right)
	if leftErr == nil && rightErr == nil {
		return leftMajor < rightMajor
	}
	return strings.Compare(left, right) < 0
}

func run(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
