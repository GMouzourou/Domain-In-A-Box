package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func releaseFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"VERSION":                            "0.5.0\n",
		"charts/domain-in-a-box/Chart.yaml":  "version: 0.3.0\nappVersion: \"0.4.0\"\n",
		"charts/domain-in-a-box/values.yaml": "# Preserve comments\ncoreImage:\n  repository: core\n  tag: \"0.4.0\"\nstorkImage:\n  repository: stork\n  tag: \"0.4.0\"\nnetwork:\n  cniVersion: \"0.4.0\"\n",
		"docker-compose.yml":                 "image: ghcr.io/gmouzourou/domain-in-a-box:latest\nimage: ghcr.io/gmouzourou/domain-in-a-box-stork:latest\n",
		"docs/docker.md":                     "docker run ghcr.io/gmouzourou/domain-in-a-box:latest\n",
		"charts/domain-in-a-box-external-dns-bridge/Chart.yaml":  "version: 0.3.0\nappVersion: \"0.4.0\"\n",
		"charts/domain-in-a-box-external-dns-bridge/values.yaml": "bridgeImage:\n  tag: \"0.4.0\"\nexternalDNSImage:\n  tag: v0.24.0\n",
		"docs/external-dns.md": "docker pull ghcr.io/gmouzourou/domain-in-a-box-external-dns-bridge:latest\n",
	}
	for path, content := range files {
		fullPath := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestUpdateSynchronizesPinsAndPreservesUnrelatedContent(t *testing.T) {
	root := releaseFixture(t)
	if err := synchronize(root, "", "", false); err == nil {
		t.Fatal("check accepted stale release pins")
	}
	if err := synchronize(root, "0.5.1", "", true); err != nil {
		t.Fatal(err)
	}
	if err := synchronize(root, "", "v0.5.1", false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "charts/domain-in-a-box/values.yaml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(before), "# Preserve comments") || !strings.Contains(string(before), `cniVersion: "0.4.0"`) {
		t.Fatal("update changed unrelated content")
	}
	if err := synchronize(root, "", "", true); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("update is not idempotent")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(after), `tag: "0.5.1"`, `tag: "0.4.0"`, 1)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := synchronize(root, "", "", false); err == nil {
		t.Fatal("check accepted a stale image pin")
	}
}

func TestCheckFindsReleaseImagesOutsideKnownFields(t *testing.T) {
	root := releaseFixture(t)
	if err := synchronize(root, "", "", true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "docs/new-guide.md")
	content := "docker pull ghcr.io/gmouzourou/domain-in-a-box-stork:0.4.0\nkeep ghcr.io/gmouzourou/domain-in-a-box:latest\n"
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := synchronize(root, "", "", false); err == nil || !strings.Contains(err.Error(), "docs/new-guide.md") {
		t.Fatalf("check missed a stale image reference: %v", err)
	}
	if err := synchronize(root, "", "", true); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "docker pull ghcr.io/gmouzourou/domain-in-a-box-stork:0.5.0\nkeep ghcr.io/gmouzourou/domain-in-a-box:latest\n"; string(updated) != want {
		t.Fatalf("updated content = %q, want %q", updated, want)
	}
}

func TestCheckRejectsMismatchedTagAndInvalidVersion(t *testing.T) {
	root := releaseFixture(t)
	if err := synchronize(root, "", "", true); err != nil {
		t.Fatal(err)
	}
	if err := synchronize(root, "", "v0.4.0", false); err == nil || !strings.Contains(err.Error(), "release tag") {
		t.Fatalf("expected tag mismatch, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("v0.5.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := synchronize(root, "", "", false); err == nil || !strings.Contains(err.Error(), "invalid release version") {
		t.Fatalf("expected invalid version, got %v", err)
	}
}

func TestCheckUsesGitHubTagWithoutChangingFiles(t *testing.T) {
	root := releaseFixture(t)
	if err := synchronize(root, "", "", true); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("GITHUB_REF_TYPE", "tag")
	t.Setenv("GITHUB_REF_NAME", "v0.4.0")
	if err := run([]string{"check"}); err == nil {
		t.Fatal("CI check ignored the release tag")
	}
	if err := synchronize(root, "", "v0.5.0", false); err != nil {
		t.Fatalf("failed tag check modified release files: %v", err)
	}
	t.Setenv("GITHUB_REF_NAME", "v0.5.0")
	if err := run([]string{"check"}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRejectsUnexpectedLayoutWithoutWriting(t *testing.T) {
	root := releaseFixture(t)
	path := filepath.Join(root, "docs/docker.md")
	if err := os.WriteFile(path, []byte("Missing image example\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := synchronize(root, "0.5.1", "", true); err == nil {
		t.Fatal("update accepted missing version field")
	}
	version, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	if string(version) != "0.5.0\n" {
		t.Fatal("update wrote VERSION before validating all fields")
	}
}

func TestCheckRejectsBridgeDriftWithoutChangingThirdPartyPins(t *testing.T) {
	root := releaseFixture(t)
	if err := synchronize(root, "0.6.0", "", true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "charts/domain-in-a-box-external-dns-bridge/values.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "tag: v0.24.0") {
		t.Fatal("release update changed the independent ExternalDNS version")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), `"0.6.0"`, `"0.5.1"`, 1)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := synchronize(root, "", "v0.6.0", false); err == nil {
		t.Fatal("release check accepted bridge image drift")
	}
}
