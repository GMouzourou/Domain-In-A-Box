package sysvolsync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func publishDirectory(staging, active string) error {
	stagingInfo, err := os.Lstat(staging)
	if err != nil {
		return fmt.Errorf("inspect staged SYSVOL: %w", err)
	}
	if stagingInfo.Mode()&os.ModeSymlink != 0 || !stagingInfo.IsDir() {
		return fmt.Errorf("staged SYSVOL %s is not a directory", staging)
	}
	activeInfo, err := os.Lstat(active)
	if os.IsNotExist(err) {
		if err := os.Rename(staging, active); err != nil {
			return fmt.Errorf("publish initial SYSVOL directory: %w", err)
		}
		return syncDirectory(filepath.Dir(active))
	}
	if err != nil {
		return fmt.Errorf("inspect active SYSVOL: %w", err)
	}
	if activeInfo.Mode()&os.ModeSymlink != 0 || !activeInfo.IsDir() {
		return fmt.Errorf("active SYSVOL %s is not a directory", active)
	}
	if filepath.Dir(staging) != filepath.Dir(active) {
		return fmt.Errorf("staged and active SYSVOL directories must share a parent")
	}
	if err := unix.Renameat2(unix.AT_FDCWD, staging, unix.AT_FDCWD, active, unix.RENAME_EXCHANGE); err != nil {
		return fmt.Errorf("atomically exchange staged and active SYSVOL directories: %w", err)
	}
	return syncDirectory(filepath.Dir(active))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory %s: %w", path, err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync directory %s: %w", path, err)
	}
	return nil
}

func rewriteSambaConfig(contents []byte, activePath, stagedPath, dnsDomain string) ([]byte, error) {
	activeNetlogon := filepath.Join(activePath, dnsDomain, "scripts")
	stagedNetlogon := filepath.Join(stagedPath, dnsDomain, "scripts")
	lines := strings.Split(string(contents), "\n")
	section := ""
	foundSYSVOL := false
	foundNetlogon := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			continue
		}
		if section != "sysvol" && section != "netlogon" {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "path") {
			continue
		}
		expectedPath, replacementPath := activePath, stagedPath
		if section == "netlogon" {
			expectedPath, replacementPath = activeNetlogon, stagedNetlogon
		}
		if strings.TrimSpace(value) != expectedPath {
			return nil, fmt.Errorf("Samba [%s] path is %q, expected %q", section, strings.TrimSpace(value), expectedPath)
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + "path = " + replacementPath
		if section == "sysvol" {
			foundSYSVOL = true
		} else {
			foundNetlogon = true
		}
	}
	if !foundSYSVOL || !foundNetlogon {
		return nil, fmt.Errorf("Samba config must define paths for both [sysvol] and [netlogon]")
	}
	return []byte(strings.Join(lines, "\n")), nil
}
