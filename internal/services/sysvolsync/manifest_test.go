package sysvolsync

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestManifestRoundTripAndTreeVerification(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Policies"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Policies", "GPT.INI"), []byte("[General]\nVersion=4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := buildTreeManifest(root, false)
	if err != nil {
		t.Fatal(err)
	}
	manifest := ExportManifest{Version: 1, GeneratedAt: time.Unix(1, 0), Entries: entries}
	path := filepath.Join(root, manifestName)
	if err := writeManifest(path, manifest); err != nil {
		t.Fatal(err)
	}

	got, err := readManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyTree(root, got); err != nil {
		t.Fatalf("verifyTree() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "Policies", "GPT.INI"), []byte("[General]\nVersion=8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyTree(root, got); err == nil {
		t.Fatal("verifyTree() accepted content that differs from the writer manifest")
	}
}

func TestBuildTreeManifestRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if _, err := buildTreeManifest(root, false); err == nil {
		t.Fatal("buildTreeManifest() accepted a symlink")
	}
}

func TestValidateManifestRejectsUnsafePath(t *testing.T) {
	manifest := ExportManifest{
		Version: 1,
		Entries: map[string]string{"../outside": "directory"},
	}
	if err := validateManifest(manifest); err == nil {
		t.Fatal("validateManifest() accepted a path outside the export")
	}
}

func TestChangedPathsIncludesAddedChangedAndDeletedEntries(t *testing.T) {
	current := map[string]string{
		"changed.ini": "sha256:new",
		"added.ini":   "sha256:added",
	}
	previous := map[string]string{
		"changed.ini": "sha256:old",
		"deleted.ini": "sha256:deleted",
	}
	want := []string{"added.ini", "changed.ini", "deleted.ini"}
	if got := changedPaths(current, previous); !reflect.DeepEqual(got, want) {
		t.Fatalf("changedPaths() = %v, want %v", got, want)
	}
}
