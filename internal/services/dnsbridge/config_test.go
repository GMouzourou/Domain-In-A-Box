package dnsbridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigurationRejectsExposedListenerAndUnboundedAddressAuthority(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:8888", ":8888", "localhost:8888", "127.0.0.1:0", "127.0.0.1:not-a-port"} {
		cfg := testConfig()
		cfg.Listen = listen
		if _, err := cfg.normalized(); err == nil {
			t.Errorf("unsafe or invalid listener %q accepted", listen)
		}
	}
	for _, networks := range [][]string{nil, {"0.0.0.0/0"}, {"::/0"}, {"198.18.0.1/24"}} {
		cfg := testConfig()
		cfg.AllowedNetworks = networks
		if _, err := cfg.normalized(); err == nil {
			t.Errorf("unbounded or noncanonical address authority %v accepted", networks)
		}
	}
}

func TestCredentialFileMustBeProtectedAndCannotInjectInputLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("test-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if password, err := readPassword(path); err != nil || password != "test-password" {
		t.Fatalf("protected credential could not be read: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPassword(path); err == nil {
		t.Fatal("world-readable credential accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("password\ninjected-command\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPassword(path); err == nil {
		t.Fatal("multiline credential accepted")
	}
}
