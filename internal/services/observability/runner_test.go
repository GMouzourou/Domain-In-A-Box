package observability

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapSkipsStorkWhenNoServerIsConfigured(t *testing.T) {
	t.Setenv("DIB_OBSERVABILITY_COMPONENT", "stork-agent")
	t.Setenv("STORK_SERVER_HOST", "")
	if err := NewRunner().Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
}

func TestExistingStorkEnvironmentFilePermissionsAreRestricted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.env")
	if err := os.WriteFile(path, []byte("STORK_DATABASE_PASSWORD=secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeStorkServerEnvironmentAt(path); err != nil {
		t.Fatalf("writeStorkServerEnvironmentAt() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("server.env mode = %04o, want 0600", got)
	}
}

func TestRegisterStorkAgent(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail string
	}{
		{name: "flat credential files"},
		{name: "registration failure", fail: "register"},
		{name: "ownership failure", fail: "chown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			state := filepath.Join(dir, "state")
			if err := os.Mkdir(state, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("DIB_TEST_STORK_STATE", state)
			t.Setenv("DIB_TEST_STORK_FAIL", tc.fail)
			commands := map[string]string{
				"stork-agent": `#!/bin/sh
set -eu
[ "$#" -eq 2 ]
[ "$1" = register ]
[ "$2" = --non-interactive ]
[ ! -e "$DIB_TEST_STORK_STATE/.stork-agent-registered" ]
[ "$DIB_TEST_STORK_FAIL" != register ] || exit 1
for file in root-cert.pem server-cert.pem server-key.pem token.txt server-cert.sha256; do
    printf 'test credential\n' > "$DIB_TEST_STORK_STATE/$file"
done
`,
				"chown": `#!/bin/sh
set -eu
[ "$#" -eq 3 ]
[ "$1" = -R ]
[ "$2" = stork-agent:root ]
[ "$3" = "$DIB_TEST_STORK_STATE" ]
[ ! -e "$DIB_TEST_STORK_STATE/.stork-agent-registered" ]
for file in root-cert.pem server-cert.pem server-key.pem token.txt server-cert.sha256; do
    [ -s "$3/$file" ]
done
[ "$DIB_TEST_STORK_FAIL" != chown ] || exit 1
printf 'ownership updated\n' > "$3/ownership-updated"
`,
			}
			for name, script := range commands {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			err := registerStorkAgent(context.Background(), state, "--non-interactive")
			if tc.fail != "" {
				if err == nil || !strings.Contains(err.Error(), "exit status 1") {
					t.Fatalf("registerStorkAgent() error = %v, want command failure", err)
				}
				if _, err := os.Stat(filepath.Join(state, ".stork-agent-registered")); !os.IsNotExist(err) {
					t.Fatalf("registration marker must not exist after failure; stat error = %v", err)
				}
				t.Setenv("DIB_TEST_STORK_FAIL", "")
				err = registerStorkAgent(context.Background(), state, "--non-interactive")
			}
			if err != nil {
				t.Fatalf("registerStorkAgent() error = %v", err)
			}
			for _, name := range []string{"ownership-updated", ".stork-agent-registered"} {
				if _, err := os.Stat(filepath.Join(state, name)); err != nil {
					t.Fatalf("expected %s after registration: %v", name, err)
				}
			}
		})
	}
}
