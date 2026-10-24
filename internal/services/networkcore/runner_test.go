package networkcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateKeaDHCPPoolPreservesOtherConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kea-dhcp4.conf")
	configuration := `{
    "Dhcp4": {
        "custom-option": "preserve-me",
        "subnet4": [
            {"id": 2, "pools": [{"pool": "10.0.0.10-10.0.0.20"}]},
            {"id": 1, "pools": [{"pool": "192.168.1.10-192.168.1.20"}]}
        ]
    }
}`
	if err := os.WriteFile(path, []byte(configuration), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := updateKeaDHCPPool(path, "192.168.1.100-192.168.1.199"); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(contents)
	if !strings.Contains(updated, `"pool": "192.168.1.100-192.168.1.199"`) {
		fatalf(t, "managed pool was not updated: %s", updated)
	}
	if !strings.Contains(updated, `"custom-option": "preserve-me"`) || !strings.Contains(updated, `"pool": "10.0.0.10-10.0.0.20"`) {
		fatalf(t, "unmanaged configuration changed: %s", updated)
	}
}

func TestUpdateBindForwardersPreservesOtherConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "named.conf.options")
	configuration := `options {
    custom-option yes;
    forwarders { 1.1.1.1; 8.8.8.8; };
    listen-on port 5353 { 192.168.1.1; };
};
`
	if err := os.WriteFile(path, []byte(configuration), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := updateBindForwarders(path, "9.9.9.9; 149.112.112.112;"); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(contents)
	if !strings.Contains(updated, "forwarders { 9.9.9.9; 149.112.112.112; };") {
		fatalf(t, "forwarders were not updated: %s", updated)
	}
	if !strings.Contains(updated, "custom-option yes;") || !strings.Contains(updated, "listen-on port 5353") {
		fatalf(t, "unmanaged configuration changed: %s", updated)
	}
}

func TestEnsureCronEntryPreservesJobsAndAvoidsDuplicates(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "crontab.txt")
	command := filepath.Join(dir, "crontab")
	script := `#!/bin/sh
if [ "$3" = "-l" ]; then
    [ -f "$DIB_TEST_CRONTAB" ] || exit 1
    cat "$DIB_TEST_CRONTAB"
    exit 0
fi
if [ "$3" = "-" ]; then
    cat > "$DIB_TEST_CRONTAB"
    exit 0
fi
exit 2
`
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DIB_TEST_CRONTAB", state)
	line := "0 */4 * * * /usr/bin/kinit -kt /var/lib/kea/keaddns.keytab keaddns@DOMAIN.HOME.ARPA"
	if err := os.WriteFile(state, []byte("15 * * * * /usr/bin/other-job\n"+line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := ensureCronEntry(context.Background(), "_kea", line); err != nil {
			t.Fatalf("ensureCronEntry() error = %v", err)
		}
	}

	contents, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(contents), line); got != 1 {
		t.Fatalf("renewal job count = %d, want 1; crontab:\n%s", got, contents)
	}
	if !strings.Contains(string(contents), "/usr/bin/other-job") {
		t.Fatalf("unrelated crontab entry was removed: %s", contents)
	}
}

func fatalf(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf(format, args...)
}
