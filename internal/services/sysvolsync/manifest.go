package sysvolsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const manifestName = ".dib-sysvol-export-manifest.json"

type ExportManifest struct {
	Version              int                  `json:"version"`
	GeneratedAt          time.Time            `json:"generatedAt"`
	Entries              map[string]string    `json:"entries"`
	GPOVersionMismatches []GPOVersionMismatch `json:"gpoVersionMismatches,omitempty"`
}

type GPOVersionMismatch struct {
	GUID          string `json:"guid"`
	ADVersion     *int   `json:"adVersion,omitempty"`
	SYSVOLVersion *int   `json:"sysvolVersion,omitempty"`
}

func buildTreeManifest(root string, excludeManifest bool) (map[string]string, error) {
	entries := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk SYSVOL tree %s: %w", path, walkErr)
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("make SYSVOL path relative: %w", err)
		}
		if excludeManifest && relative == manifestName {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("SYSVOL tree contains unsupported symlink %q", relative)
		}
		if entry.IsDir() {
			entries[filepath.ToSlash(relative)] = "directory"
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect SYSVOL entry %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("SYSVOL tree contains unsupported file type %q at %q", info.Mode().Type(), relative)
		}
		digest, err := digestFile(path)
		if err != nil {
			return err
		}
		entries[filepath.ToSlash(relative)] = "sha256:" + digest
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open SYSVOL file %s: %w", path, err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash SYSVOL file %s: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateManifest(manifest ExportManifest) error {
	if manifest.Version != 1 {
		return fmt.Errorf("unsupported SYSVOL manifest version %d", manifest.Version)
	}
	if manifest.Entries == nil {
		return fmt.Errorf("SYSVOL manifest has no entries")
	}
	for path, entry := range manifest.Entries {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		if clean != path || clean == "." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			return fmt.Errorf("SYSVOL manifest contains unsafe path %q", path)
		}
		if entry != "directory" && (!strings.HasPrefix(entry, "sha256:") || len(entry) != len("sha256:")+sha256.Size*2) {
			return fmt.Errorf("SYSVOL manifest contains invalid entry for %q", path)
		}
		if strings.HasPrefix(entry, "sha256:") {
			if _, err := hex.DecodeString(strings.TrimPrefix(entry, "sha256:")); err != nil {
				return fmt.Errorf("SYSVOL manifest contains invalid hash for %q: %w", path, err)
			}
		}
	}
	return nil
}

func readManifest(path string) (ExportManifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ExportManifest{}, fmt.Errorf("inspect SYSVOL export manifest: %w", err)
	}
	if !info.Mode().IsRegular() {
		return ExportManifest{}, fmt.Errorf("SYSVOL export manifest is not a regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return ExportManifest{}, fmt.Errorf("read SYSVOL export manifest: %w", err)
	}
	var manifest ExportManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return ExportManifest{}, fmt.Errorf("parse SYSVOL export manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return ExportManifest{}, err
	}
	return manifest, nil
}

func verifyTree(root string, manifest ExportManifest) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}
	actual, err := buildTreeManifest(root, true)
	if err != nil {
		return err
	}
	if len(actual) != len(manifest.Entries) {
		return fmt.Errorf("SYSVOL tree entry count %d does not match writer manifest %d", len(actual), len(manifest.Entries))
	}
	for path, expected := range manifest.Entries {
		if got, exists := actual[path]; !exists || got != expected {
			return fmt.Errorf("SYSVOL tree entry %q does not match writer manifest", path)
		}
	}
	return nil
}

func changedPaths(current, previous map[string]string) []string {
	paths := make(map[string]struct{}, len(current)+len(previous))
	for path := range current {
		paths[path] = struct{}{}
	}
	for path := range previous {
		paths[path] = struct{}{}
	}

	changed := make([]string, 0)
	for path := range paths {
		if current[path] != previous[path] {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed
}

func writeManifest(path string, manifest ExportManifest) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}
	contents, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode SYSVOL export manifest: %w", err)
	}
	if err := writeAtomic(path, contents, 0o644); err != nil {
		return fmt.Errorf("write SYSVOL export manifest: %w", err)
	}
	return nil
}

func writeAtomic(path string, contents []byte, mode fs.FileMode) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".dib-sysvol-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)

	if err := file.Chmod(mode); err != nil {
		file.Close()
		return fmt.Errorf("set temporary file mode: %w", err)
	}
	if _, err := file.Write(contents); err != nil {
		file.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish temporary file: %w", err)
	}
	dir, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open parent directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync parent directory: %w", err)
	}
	return nil
}
