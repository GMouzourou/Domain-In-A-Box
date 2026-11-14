package sysvolsync

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDomainDNFromRealm(t *testing.T) {
	got, err := domainDNFromRealm("DOMAIN.HOME.ARPA")
	if err != nil {
		t.Fatal(err)
	}
	if want := "DC=domain,DC=home,DC=arpa"; got != want {
		t.Fatalf("domainDNFromRealm() = %q, want %q", got, want)
	}
	for _, invalid := range []string{"DOMAIN", "domain..home.arpa", "domain;dc=evil"} {
		if _, err := domainDNFromRealm(invalid); err == nil {
			t.Errorf("domainDNFromRealm(%q) accepted invalid realm", invalid)
		}
	}
}

func TestParseADGPOVersions(t *testing.T) {
	output := `# record 1
dn: CN={31B2F340-016D-11D2-945F-00C04FB984F9},CN=Policies,CN=System,DC=domain,DC=home,DC=arpa
cn: {31B2F340-016D-11D2-945F-00C04FB984F9}
versionNumber: 4

# returned 1 records
`
	got, err := parseADGPOVersions(output)
	if err != nil {
		t.Fatal(err)
	}
	if got["{31B2F340-016D-11D2-945F-00C04FB984F9}"] != 4 {
		t.Fatalf("parseADGPOVersions() = %v", got)
	}
}

func TestReadSYSVOLGPOVersions(t *testing.T) {
	root := t.TempDir()
	policy := filepath.Join(root, "Policies", "{31B2F340-016D-11D2-945F-00C04FB984F9}")
	if err := os.MkdirAll(policy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(policy, "GPT.INI"), []byte("[General]\nVersion=4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readSYSVOLGPOVersions(root)
	if err != nil {
		t.Fatal(err)
	}
	if got["{31B2F340-016D-11D2-945F-00C04FB984F9}"] != 4 {
		t.Fatalf("readSYSVOLGPOVersions() = %v", got)
	}
}

func TestFindGPOVersionMismatchesReportsDifferenceAndMissingSide(t *testing.T) {
	ad := map[string]int{
		"{A0000000-0000-0000-0000-000000000001}": 4,
		"{A0000000-0000-0000-0000-000000000002}": 2,
	}
	sysvol := map[string]int{
		"{A0000000-0000-0000-0000-000000000001}": 8,
		"{A0000000-0000-0000-0000-000000000003}": 1,
	}
	got := findGPOVersionMismatches(ad, sysvol)
	if len(got) != 3 {
		t.Fatalf("findGPOVersionMismatches() returned %d mismatches, want 3: %v", len(got), got)
	}
	if got[0].GUID != "{A0000000-0000-0000-0000-000000000001}" ||
		got[0].ADVersion == nil || *got[0].ADVersion != 4 ||
		got[0].SYSVOLVersion == nil || *got[0].SYSVOLVersion != 8 {
		t.Fatalf("unexpected version mismatch: %+v", got[0])
	}
	if got[1].ADVersion == nil || got[1].SYSVOLVersion != nil {
		t.Fatalf("AD-only GPO mismatch was not reported: %+v", got[1])
	}
	if got[2].ADVersion != nil || got[2].SYSVOLVersion == nil {
		t.Fatalf("SYSVOL-only GPO mismatch was not reported: %+v", got[2])
	}
	if !reflect.DeepEqual(got, findGPOVersionMismatches(ad, sysvol)) {
		t.Fatal("GPO mismatches are not deterministic")
	}
}
