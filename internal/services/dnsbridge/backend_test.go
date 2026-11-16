package dnsbridge

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodeNativeSambaDirectoryRecords(t *testing.T) {
	// Packed with Samba 4.23.6 dnsp.DnssrvRpcRecord, not the different DNS RPC wire type.
	cases := []struct {
		kind   string
		packed string
		target string
	}{
		{"A", "BAABAAXwAAAAAAAAAAADhAAAAAAAAAAAxhIAMg==", "198.18.0.50"},
		{"AAAA", "EAAcAAXwAAAAAAAAAAADhAAAAAAAAAAA/RIAAAAAAAAAAAAAAAAAUA==", "fd12::50"},
		{"TXT", "NQAQAAXwAAAAAAAAAAADhAAAAAAAAAAAFmhlcml0YWdlPWV4dGVybmFsLWRucywdZXh0ZXJuYWwtZG5zL293bmVyPXRlc3Qtb3duZXI=", `"heritage=external-dns,external-dns/owner=test-owner"`},
	}
	for _, test := range cases {
		t.Run(test.kind, func(t *testing.T) {
			data, err := base64.StdEncoding.DecodeString(test.packed)
			if err != nil {
				t.Fatal(err)
			}
			record, supported, err := decodeRecord("app.domain.home.arpa", data)
			if err != nil || !supported || record.RecordType != test.kind || record.RecordTTL != 900 ||
				len(record.Targets) != 1 || record.Targets[0] != test.target {
				t.Fatalf("native record decoded incorrectly: record=%+v supported=%t error=%v", record, supported, err)
			}
			if _, _, err := decodeRecord(record.DNSName, data[:len(data)-1]); err == nil {
				t.Fatal("truncated native record was accepted")
			}
		})
	}
}

func TestUpdateScriptUsesAtomicOwnershipAndExactValuePrerequisites(t *testing.T) {
	policy, backend, _, _ := fixture(t)
	a, txt := address(), ownership()
	backend.snapshot.Records[a.key()], backend.snapshot.Records[txt.key()] = a, txt
	backend.snapshot.Names[a.DNSName], backend.snapshot.Names[txt.DNSName] = true, true
	next := a
	next.Targets = []string{"198.18.0.51"}
	mutation, err := policy.plan(backend.snapshot, Changes{UpdateOld: []Endpoint{a}, UpdateNew: []Endpoint{next}})
	if err != nil {
		t.Fatal(err)
	}
	script, err := updateScript(policy.cfg, backend.snapshot, mutation)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"prereq yxrrset app.domain.home.arpa. A 198.18.0.50\n",
		"prereq yxrrset dib-edns-a-app.domain.home.arpa. TXT " + txt.Targets[0] + "\n",
		"update delete app.domain.home.arpa. A 198.18.0.50\n",
		"update add app.domain.home.arpa. 60 A 198.18.0.51\n",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("missing exact transaction instruction %q:\n%s", expected, script)
		}
	}
	if strings.Count(script, "\nsend\n") != 1 || strings.Contains(script, "update delete app.domain.home.arpa.\n") {
		t.Fatal("transaction is not a single exact-value update")
	}
}
