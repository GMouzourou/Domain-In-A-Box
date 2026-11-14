package sysvolsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishDirectoryAtomicallyReplacesExistingTree(t *testing.T) {
	parent := t.TempDir()
	active := filepath.Join(parent, "sysvol")
	staging := filepath.Join(parent, "sysvol.stage")
	if err := os.Mkdir(active, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := publishDirectory(staging, active); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(active, "new.txt")); err != nil {
		t.Fatalf("new directory was not published: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "old.txt")); err != nil {
		t.Fatalf("old active directory was not exchanged into staging: %v", err)
	}
}

func TestPublishDirectoryRejectsDifferentParents(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	if err := os.Mkdir(active, 0o755); err != nil {
		t.Fatal(err)
	}
	stagingParent := filepath.Join(root, "other")
	if err := os.Mkdir(stagingParent, 0o755); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(stagingParent, "stage")
	if err := os.Mkdir(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := publishDirectory(staging, active); err == nil {
		t.Fatal("publishDirectory() accepted directories with different parents")
	}
}

func TestRewriteSambaConfigChangesOnlyManagedSharePaths(t *testing.T) {
	config := []byte(`[global]
workgroup = DOMAIN

[sysvol]
	path = /var/lib/samba/sysvol

[netlogon]
	path = /var/lib/samba/sysvol/domain.home.arpa/scripts
`)
	got, err := rewriteSambaConfig(config, "/var/lib/samba/sysvol", "/var/lib/samba/sysvol.stage", "domain.home.arpa")
	if err != nil {
		t.Fatal(err)
	}
	updated := string(got)
	if !strings.Contains(updated, "path = /var/lib/samba/sysvol.stage\n") ||
		!strings.Contains(updated, "path = /var/lib/samba/sysvol.stage/domain.home.arpa/scripts\n") {
		t.Fatalf("Samba share paths were not updated: %s", updated)
	}
	if !strings.Contains(updated, "workgroup = DOMAIN") {
		t.Fatalf("unmanaged Samba configuration changed: %s", updated)
	}
}

func TestRewriteSambaConfigRejectsUnexpectedPaths(t *testing.T) {
	config := []byte(`[sysvol]
 path = /different/location
[netlogon]
 path = /var/lib/samba/sysvol/domain.home.arpa/scripts
`)
	if _, err := rewriteSambaConfig(config, "/var/lib/samba/sysvol", "/var/lib/samba/sysvol.stage", "domain.home.arpa"); err == nil {
		t.Fatal("rewriteSambaConfig() accepted an unexpected SYSVOL path")
	}
}
