package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSecretFilesReadsFileIntoKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIB_TEST_SECRET", "")
	t.Setenv("DIB_TEST_SECRET_FILE", path)

	if err := LoadSecretFiles("DIB_TEST_SECRET"); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("DIB_TEST_SECRET"); got != "s3cret" {
		t.Fatalf("DIB_TEST_SECRET = %q", got)
	}
	if _, set := os.LookupEnv("DIB_TEST_SECRET_FILE"); set {
		t.Fatal("DIB_TEST_SECRET_FILE was not cleared")
	}
}

func TestLoadSecretFilesRejectsInvalidSources(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct{ value, file, want string }{
		"both set": {"direct", empty, "not both"},
		"missing":  {"", filepath.Join(dir, "missing"), "read DIB_TEST_SECRET_FILE"},
		"empty":    {"", empty, "is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DIB_TEST_SECRET", test.value)
			t.Setenv("DIB_TEST_SECRET_FILE", test.file)
			err := LoadSecretFiles("DIB_TEST_SECRET")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
