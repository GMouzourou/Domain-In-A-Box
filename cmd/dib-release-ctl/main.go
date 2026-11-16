package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type versionField struct {
	path    string
	pattern *regexp.Regexp
	value   func(string) string
}

var versionFields = []versionField{
	{"charts/domain-in-a-box-external-dns-bridge/Chart.yaml", regexp.MustCompile(`(?m)^version: [^\n]+$`), func(v string) string { return "version: " + v }},
	{"charts/domain-in-a-box-external-dns-bridge/Chart.yaml", regexp.MustCompile(`(?m)^appVersion: [^\n]+$`), func(v string) string { return `appVersion: "` + v + `"` }},
	{"charts/domain-in-a-box-external-dns-bridge/values.yaml", regexp.MustCompile(`(?m)(^bridgeImage:\n(?:[ \t]+[^\n]*\n)*?[ \t]+tag: )[^\n]+`), func(v string) string { return `"` + v + `"` }},
	{"docs/external-dns.md", regexp.MustCompile(`ghcr\.io/gmouzourou/domain-in-a-box-external-dns-bridge:[A-Za-z0-9_.-]+`), func(v string) string { return "ghcr.io/gmouzourou/domain-in-a-box-external-dns-bridge:" + v }},
	{"charts/domain-in-a-box/Chart.yaml", regexp.MustCompile(`(?m)^version: [^\n]+$`), func(v string) string { return "version: " + v }},
	{"charts/domain-in-a-box/Chart.yaml", regexp.MustCompile(`(?m)^appVersion: [^\n]+$`), func(v string) string { return `appVersion: "` + v + `"` }},
	{"charts/domain-in-a-box/values.yaml", regexp.MustCompile(`(?m)(^coreImage:\n(?:[ \t]+[^\n]*\n)*?[ \t]+tag: )[^\n]+`), func(v string) string { return `"` + v + `"` }},
	{"charts/domain-in-a-box/values.yaml", regexp.MustCompile(`(?m)(^storkImage:\n(?:[ \t]+[^\n]*\n)*?[ \t]+tag: )[^\n]+`), func(v string) string { return `"` + v + `"` }},
	{"docker-compose.yml", regexp.MustCompile(`ghcr\.io/gmouzourou/domain-in-a-box:[A-Za-z0-9_.-]+`), func(v string) string { return "ghcr.io/gmouzourou/domain-in-a-box:" + v }},
	{"docker-compose.yml", regexp.MustCompile(`ghcr\.io/gmouzourou/domain-in-a-box-stork:[A-Za-z0-9_.-]+`), func(v string) string { return "ghcr.io/gmouzourou/domain-in-a-box-stork:" + v }},
	{"docs/docker.md", regexp.MustCompile(`ghcr\.io/gmouzourou/domain-in-a-box:[A-Za-z0-9_.-]+`), func(v string) string { return "ghcr.io/gmouzourou/domain-in-a-box:" + v }},
}

// releaseImagePattern finds published image references anywhere in the
// repository, so a new document or manifest cannot pin a stale release.
var releaseImagePattern = regexp.MustCompile(`(ghcr\.io/gmouzourou/domain-in-a-box(?:-stork|-external-dns-bridge)?:)v?[0-9]+\.[0-9]+\.[0-9]+\b`)

var scannedExtensions = map[string]bool{".md": true, ".yml": true, ".yaml": true, ".hcl": true, ".sh": true, ".json": true, ".tpl": true, ".txt": true}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: dib-release-ctl check [--tag vX.Y.Z] | update [X.Y.Z] (run from repository root)")
	}
	switch args[0] {
	case "check":
		var tag string
		if len(args) == 3 && args[1] == "--tag" {
			tag = args[2]
			if tag == "" {
				return errors.New("release tag must not be empty")
			}
		} else if len(args) != 1 {
			return errors.New("usage: dib-release-ctl check [--tag vX.Y.Z]")
		} else if os.Getenv("GITHUB_REF_TYPE") == "tag" {
			tag = os.Getenv("GITHUB_REF_NAME")
			if tag == "" {
				return errors.New("GITHUB_REF_NAME is missing for a tag build")
			}
		}
		if err := synchronize(".", "", tag, false); err != nil {
			return err
		}
		fmt.Println("Release version and deployment pins are consistent")
	case "update":
		if len(args) > 2 {
			return errors.New("usage: dib-release-ctl update [X.Y.Z]")
		}
		var version string
		if len(args) == 2 {
			version = args[1]
			if version == "" {
				return errors.New("release version must not be empty")
			}
		}
		if err := synchronize(".", version, "", true); err != nil {
			return err
		}
		fmt.Println("Updated VERSION and deployment pins")
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	return nil
}

func synchronize(root, version, tag string, update bool) error {
	current, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return fmt.Errorf("read VERSION: %w", err)
	}
	if version == "" {
		version = strings.TrimSpace(string(current))
	}
	if !versionPattern.MatchString(version) {
		return fmt.Errorf("invalid release version %q: expected X.Y.Z without a v prefix", version)
	}
	if tag != "" && tag != "v"+version {
		return fmt.Errorf("release tag %q does not match VERSION (v%s)", tag, version)
	}

	original := map[string][]byte{"VERSION": current}
	updated := map[string][]byte{"VERSION": []byte(version + "\n")}
	for _, field := range versionFields {
		content, loaded := updated[field.path]
		if !loaded {
			content, err = os.ReadFile(filepath.Join(root, field.path))
			if err != nil {
				return fmt.Errorf("read %s: %w", field.path, err)
			}
			original[field.path] = content
		}
		matches := field.pattern.FindAllSubmatchIndex(content, -1)
		if len(matches) != 1 {
			return fmt.Errorf("%s: expected exactly one version field matching %s, found %d", field.path, field.pattern, len(matches))
		}
		match := matches[0]
		start, end := match[0], match[1]
		if len(match) > 2 {
			start = match[3] // Preserve the YAML section and indentation.
		}
		replacement := []byte(field.value(version))
		next := bytes.Clone(content[:start])
		next = append(next, replacement...)
		next = append(next, content[end:]...)
		updated[field.path] = next
	}

	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") && entry.Name() != ".github" || entry.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !scannedExtensions[filepath.Ext(path)] && entry.Name() != "Dockerfile" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		content, loaded := updated[relative]
		if !loaded {
			content, err = os.ReadFile(path) //nolint:gosec // G122: a developer tool reading its own checkout.
			if err != nil {
				return fmt.Errorf("read %s: %w", relative, err)
			}
		}
		next := releaseImagePattern.ReplaceAll(content, []byte("${1}"+version))
		if !bytes.Equal(next, content) {
			if !loaded {
				original[relative] = content
			}
			updated[relative] = next
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan release image references: %w", err)
	}

	var mismatches []error
	for path, content := range updated {
		if !bytes.Equal(original[path], content) {
			mismatches = append(mismatches, fmt.Errorf("%s is out of sync with VERSION %s; run make release-version", path, version))
		}
	}
	if !update {
		return errors.Join(mismatches...)
	}
	// Validate every field before modifying any file, so changed layouts fail closed.
	for path, content := range updated {
		if bytes.Equal(original[path], content) {
			continue
		}
		fullPath := filepath.Join(root, path)
		info, err := os.Stat(fullPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(fullPath, content, info.Mode().Perm()); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}
