package dnsbridge

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestADBackendIntegration(t *testing.T) {
	path := os.Getenv("DIB_DNS_BRIDGE_INTEGRATION_CONFIG")
	if path == "" {
		t.Skip("requires disposable controller integration configuration")
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewPolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	backend, err := NewBackend(cfg, policy)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := backend.Records(ctx)
	if err != nil || !snapshot.Names["manual."+cfg.Zone] {
		t.Fatalf("verified baseline listing failed or omitted manual name: %v", err)
	}
	for _, failure := range []string{"password", "hostname", "CA"} {
		t.Run(failure, func(t *testing.T) {
			bad := cfg
			switch failure {
			case "password":
				bad.PasswordFile = filepath.Join(t.TempDir(), "wrong-password")
				if err := os.WriteFile(bad.PasswordFile, []byte("Incorrect-Disposable123!"), 0600); err != nil {
					t.Fatal(err)
				}
			case "hostname":
				bad.Controllers = append([]Controller(nil), cfg.Controllers...)
				for i := range bad.Controllers {
					bad.Controllers[i].Hostname = "wrong-" + strconv.Itoa(i) + "." + cfg.Zone
				}
			case "CA":
				bad.CAFile = os.Getenv("DIB_DNS_BRIDGE_INTEGRATION_BAD_CA")
				if bad.CAFile == "" {
					t.Fatal("wrong-CA fixture is required")
				}
			}
			client, err := NewBackend(bad, policy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Records(ctx); err == nil {
				t.Fatal("invalid authentication or trust produced a successful DNS listing")
			}
		})
	}
	t.Run("raw outside-zone update", func(t *testing.T) {
		outside := cfg
		outside.Zone = "_msdcs." + cfg.Zone
		client, err := NewBackend(outside, policy)
		if err != nil {
			t.Fatal(err)
		}
		record := Endpoint{DNSName: "forbidden." + outside.Zone, RecordType: "TXT", RecordTTL: 60, Targets: []string{"forbidden"}}
		mutation := Mutation{Names: []string{record.DNSName}, Before: map[RecordKey]Endpoint{}, After: map[RecordKey]Endpoint{record.key(): record}}
		adminConfig := outside
		adminConfig.Username = "Administrator"
		adminConfig.PasswordFile = os.Getenv("DIB_DNS_BRIDGE_INTEGRATION_ADMIN_PASSWORD")
		if adminConfig.PasswordFile == "" {
			t.Fatal("disposable administrator control fixture is required")
		}
		admin, err := NewBackend(adminConfig, policy)
		if err != nil {
			t.Fatal(err)
		}
		removeSnapshot := snapshot
		removeSnapshot.Names = map[string]bool{record.DNSName: true}
		remove := Mutation{Names: mutation.Names, Before: mutation.After, After: map[RecordKey]Endpoint{}}
		control := func() {
			t.Helper()
			if err := admin.Apply(ctx, snapshot, mutation); err != nil {
				t.Fatalf("identical administrator mutation did not succeed: %v", err)
			}
			if err := admin.Apply(ctx, removeSnapshot, remove); err != nil {
				t.Fatalf("administrator control cleanup failed: %v", err)
			}
		}
		control()
		// Samba can map an explicit zone ACL denial to SERVFAIL; prove the same
		// transaction succeeds for an administrator before and after the refusal.
		if err := client.Apply(ctx, snapshot, mutation); err == nil ||
			(!strings.Contains(err.Error(), "REFUSED") && !strings.Contains(err.Error(), "update failed: SERVFAIL")) {
			t.Fatalf("outside-zone mutation did not receive an actual DNS authorization refusal: %v", err)
		}
		control()
	})
	t.Run("raw manual-node update", func(t *testing.T) {
		key := RecordKey{Name: "manual." + cfg.Zone, Type: "A"}
		before, exists := snapshot.Records[key]
		if !exists {
			t.Fatal("manual A fixture is missing")
		}
		after := before
		after.Targets = []string{"198.18.80.50"}
		mutation := Mutation{Names: []string{key.Name}, Before: map[RecordKey]Endpoint{key: before}, After: map[RecordKey]Endpoint{key: after}}
		if err := backend.Apply(ctx, snapshot, mutation); err == nil || !strings.Contains(err.Error(), "REFUSED") {
			t.Fatalf("manual-node mutation did not receive an actual DNS authorization refusal: %v", err)
		}
	})
}
