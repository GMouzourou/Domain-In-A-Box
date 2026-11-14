package sysvolsync

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var gpoGUIDPattern = regexp.MustCompile(`(?i)^\{([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\}$`)

func readADGPOVersions(ctx context.Context, realm string) (map[string]int, error) {
	domainDN, err := domainDNFromRealm(realm)
	if err != nil {
		return nil, err
	}
	baseDN := "CN=Policies,CN=System," + domainDN
	command := exec.CommandContext(
		ctx,
		"ldbsearch",
		"-H", "/var/lib/samba/private/sam.ldb",
		"-b", baseDN,
		"(objectClass=groupPolicyContainer)",
		"cn",
		"versionNumber",
	)
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("read AD GPO versions from local SAM database: %w", err)
	}
	return parseADGPOVersions(string(output))
}

func domainDNFromRealm(realm string) (string, error) {
	labels := strings.Split(strings.TrimSuffix(strings.TrimSpace(realm), "."), ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("DIB_REALM must be a DNS domain with at least two labels")
	}
	dcValues := make([]string, len(labels))
	for i, label := range labels {
		if label == "" {
			return "", fmt.Errorf("DIB_REALM contains an empty DNS label")
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z') &&
				!(character >= 'A' && character <= 'Z') &&
				!(character >= '0' && character <= '9') &&
				character != '-' {
				return "", fmt.Errorf("DIB_REALM contains an invalid DNS label %q", label)
			}
		}
		dcValues[i] = "DC=" + strings.ToLower(label)
	}
	return strings.Join(dcValues, ","), nil
}

func parseADGPOVersions(output string) (map[string]int, error) {
	versions := make(map[string]int)
	for _, record := range strings.Split(output, "\n\n") {
		var guid string
		var version *int
		for _, line := range strings.Split(record, "\n") {
			switch {
			case strings.HasPrefix(line, "cn: "):
				guid = strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(line, "cn: ")))
			case strings.HasPrefix(line, "versionNumber: "):
				value, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "versionNumber: ")))
				if err != nil {
					return nil, fmt.Errorf("parse AD GPO version %q: %w", line, err)
				}
				version = &value
			}
		}
		if guid == "" && version == nil {
			continue
		}
		if !gpoGUIDPattern.MatchString(guid) || version == nil {
			return nil, fmt.Errorf("incomplete AD GPO record for %q", guid)
		}
		if _, exists := versions[guid]; exists {
			return nil, fmt.Errorf("duplicate AD GPO record for %s", guid)
		}
		versions[guid] = *version
	}
	return versions, nil
}

func readSYSVOLGPOVersions(root string) (map[string]int, error) {
	policies := filepath.Join(root, "Policies")
	versions := make(map[string]int)
	err := filepath.WalkDir(policies, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk SYSVOL policy files: %w", walkErr)
		}
		if entry.IsDir() || !strings.EqualFold(entry.Name(), "GPT.INI") {
			return nil
		}
		guid := strings.ToUpper(filepath.Base(filepath.Dir(path)))
		if !gpoGUIDPattern.MatchString(guid) {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		defer file.Close()

		var version *int
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			key, value, ok := strings.Cut(scanner.Text(), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(key), "Version") {
				continue
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return fmt.Errorf("parse GPT.INI version in %s: %w", path, err)
			}
			version = &parsed
			break
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if version == nil {
			return fmt.Errorf("GPT.INI in %s has no Version value", path)
		}
		if _, exists := versions[guid]; exists {
			return fmt.Errorf("duplicate GPT.INI for GPO %s", guid)
		}
		versions[guid] = *version
		return nil
	})
	if err != nil {
		return nil, err
	}
	return versions, nil
}

func findGPOVersionMismatches(adVersions, sysvolVersions map[string]int) []GPOVersionMismatch {
	guidSet := make(map[string]struct{}, len(adVersions)+len(sysvolVersions))
	for guid := range adVersions {
		guidSet[guid] = struct{}{}
	}
	for guid := range sysvolVersions {
		guidSet[guid] = struct{}{}
	}

	mismatches := make([]GPOVersionMismatch, 0)
	for guid := range guidSet {
		adVersion, inAD := adVersions[guid]
		sysvolVersion, inSYSVOL := sysvolVersions[guid]
		if inAD && inSYSVOL && adVersion == sysvolVersion {
			continue
		}
		mismatch := GPOVersionMismatch{GUID: guid}
		if inAD {
			mismatch.ADVersion = &adVersion
		}
		if inSYSVOL {
			mismatch.SYSVOLVersion = &sysvolVersion
		}
		mismatches = append(mismatches, mismatch)
	}
	sort.Slice(mismatches, func(i, j int) bool {
		return mismatches[i].GUID < mismatches[j].GUID
	})
	return mismatches
}
