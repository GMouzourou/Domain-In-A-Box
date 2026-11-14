package sysvolsync

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolvePendingStateCommitsWhenActiveTreeMatches(t *testing.T) {
	active := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(filepath.Join(active, "policy.txt"), []byte("complete"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := buildTreeManifest(active, false)
	if err != nil {
		t.Fatal(err)
	}
	manifest := ExportManifest{Version: 1, Entries: entries}
	state := syncState{Version: 1, Pending: &manifest}

	got, err := resolvePendingState(state, statePath, active)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pending != nil || got.Current == nil || !reflect.DeepEqual(got.Current.Entries, entries) {
		t.Fatalf("resolved state = %#v, want pending cleared and matching current tree", got)
	}
	persisted, err := loadSyncState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Current == nil || persisted.Pending != nil {
		t.Fatalf("persisted state = %#v, want committed current manifest", persisted)
	}
}

func TestResolvePendingStateDiscardsWhenActiveTreeDoesNotMatch(t *testing.T) {
	active := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(filepath.Join(active, "policy.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldEntries, err := buildTreeManifest(active, false)
	if err != nil {
		t.Fatal(err)
	}
	current := ExportManifest{Version: 1, Entries: oldEntries}
	pending := ExportManifest{Version: 1, Entries: map[string]string{"policy.txt": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	state := syncState{Version: 1, Current: &current, Pending: &pending}

	got, err := resolvePendingState(state, statePath, active)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pending != nil || !reflect.DeepEqual(got.Current, &current) {
		t.Fatalf("resolved state = %#v, want pending discarded and old current retained", got)
	}
}

func TestPublishDirectoryRejectsSymlinkedStage(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	if err := os.Mkdir(active, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(root, "stage")
	if err := os.Symlink(target, staging); err != nil {
		t.Fatal(err)
	}
	if err := publishDirectory(staging, active); err == nil {
		t.Fatal("publishDirectory() accepted a symlinked staging tree")
	}
}

func TestSyncTreeSyncsARegularTree(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "Policies")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "GPT.INI"), []byte("[General]\nVersion=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syncTree(root); err != nil {
		t.Fatalf("syncTree() error = %v", err)
	}
}

func TestWriteAuthorizedKeysIsReadableByRestrictedAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorized_keys")
	publicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKey dib-sysvol-reader"

	if err := writeAuthorizedKeys(path, publicKey); err != nil {
		t.Fatalf("writeAuthorizedKeys() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o644); got != want {
		t.Fatalf("authorized_keys mode = %04o, want %04o", got, want)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "restrict "+publicKey+"\n"; got != want {
		t.Fatalf("authorized_keys contents = %q, want %q", got, want)
	}
}
