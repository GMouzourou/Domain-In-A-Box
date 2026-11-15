package identitycore

import (
	"strings"
	"testing"
)

func TestRedactGeneratedPassword(t *testing.T) {
	input := "INFO Setting up sam.ldb\n" +
		"INFO 2026-11-15 21:04:41,113 pid:11 provision.py #260: Administrator password will be set randomly!\n" +
		"INFO 2026-11-15 21:04:52,001 pid:11 provision.py #340: Admin password:        s3cr3t-Value\n" +
		"INFO Server Role:           active directory domain controller"
	got := string(redactGeneratedPassword([]byte(input)))
	if strings.Contains(got, "s3cr3t-Value") {
		t.Fatalf("generated password was not redacted:\n%s", got)
	}
	want := "INFO Setting up sam.ldb\n" +
		"INFO 2026-11-15 21:04:41,113 pid:11 provision.py #260: Administrator password will be set randomly!\n" +
		"INFO 2026-11-15 21:04:52,001 pid:11 provision.py #340: Admin password: <redacted>\n" +
		"INFO Server Role:           active directory domain controller"
	if got != want {
		t.Fatalf("unexpected redaction:\n got: %q\nwant: %q", got, want)
	}
}

func TestProvisionArgumentsDoNotCarryPassword(t *testing.T) {
	for _, argument := range provisionArguments("EXAMPLE.TEST", "EXAMPLE", "dc1", "192.0.2.10", "net1", "off") {
		if strings.HasPrefix(argument, "--adminpass") || strings.HasPrefix(argument, "--newpassword") {
			t.Fatalf("provision arguments expose a password option: %q", argument)
		}
	}
}

func TestReverseRecordNameFollowsZoneDepth(t *testing.T) {
	for _, tc := range []struct {
		address, zone, want string
	}{
		{"192.168.1.8", "1.168.192.in-addr.arpa", "8"},
		{"172.16.4.9", "16.172.in-addr.arpa", "9.4"},
		{"10.1.2.3", "10.in-addr.arpa.", "3.2.1"},
		{"192.168.1.8", "1.168.192.IN-ADDR.ARPA", "8"},
	} {
		got, err := reverseRecordName(tc.address, tc.zone)
		if err != nil || got != tc.want {
			t.Fatalf("reverseRecordName(%q, %q) = %q, %v; want %q", tc.address, tc.zone, got, err, tc.want)
		}
	}
	for _, tc := range []struct{ address, zone string }{
		{"192.168.0.8", "1.168.192.in-addr.arpa"},
		{"fd00::1", "1.168.192.in-addr.arpa"},
		{"192.168.1.8", "11.168.192.in-addr.arpa"},
	} {
		if got, err := reverseRecordName(tc.address, tc.zone); err == nil {
			t.Fatalf("reverseRecordName(%q, %q) = %q, want error", tc.address, tc.zone, got)
		}
	}
}
