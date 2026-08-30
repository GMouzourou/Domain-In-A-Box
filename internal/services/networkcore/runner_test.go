package networkcore

import (
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

func fatalf(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf(format, args...)
}