package dnsbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformlog "github.com/GMouzourou/domain-in-a-box/internal/platform/log"
)

type fakeBackend struct {
	snapshot       Snapshot
	readError      error
	committedError error
	writes         int
}

func (b *fakeBackend) Records(context.Context) (Snapshot, error) {
	return Snapshot{
		Controller: b.snapshot.Controller, Records: maps.Clone(b.snapshot.Records),
		Names: maps.Clone(b.snapshot.Names), Foreign: maps.Clone(b.snapshot.Foreign),
	}, b.readError
}

func (b *fakeBackend) Apply(_ context.Context, _ Snapshot, mutation Mutation) error {
	b.writes++
	for key := range mutation.Before {
		delete(b.snapshot.Records, key)
	}
	for key, record := range mutation.After {
		b.snapshot.Records[key] = record
	}
	for _, name := range mutation.Names {
		delete(b.snapshot.Names, name)
		for key := range b.snapshot.Records {
			if key.Name == name {
				b.snapshot.Names[name] = true
			}
		}
	}
	return b.committedError
}

func testConfig() Config {
	return Config{
		Realm: "DOMAIN.HOME.ARPA", Zone: "domain.home.arpa", Username: "dib-edns",
		PasswordFile: "/credentials/password", CAFile: "/trust/ca.crt", OwnerID: "test-owner",
		Controllers:     []Controller{{Hostname: "dc1.domain.home.arpa", Address: "198.18.0.1"}},
		AllowedNetworks: []string{"198.18.0.0/24", "fd12::/64"},
	}
}

func fixture(t *testing.T) (*Policy, *fakeBackend, http.Handler, *bytes.Buffer) {
	t.Helper()
	policy, err := NewPolicy(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{snapshot: Snapshot{
		Controller: policy.cfg.Controllers[0], Records: make(map[RecordKey]Endpoint),
		Names: make(map[string]bool), Foreign: make(map[string]bool),
	}}
	logs := new(bytes.Buffer)
	server := NewServer(policy, backend, platformlog.New("test").WithWriter(logs))
	return policy, backend, server.Handler(), logs
}

func request(t *testing.T, handler http.Handler, method, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if value != nil {
		if err := json.NewEncoder(&body).Encode(value); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &body)
	req.Header.Set("Accept", MediaType)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func address() Endpoint {
	return Endpoint{DNSName: "app.domain.home.arpa", RecordType: "A", Targets: []string{"198.18.0.50"}, RecordTTL: 60}
}

func ownership() Endpoint {
	return Endpoint{
		DNSName: "dib-edns-a-app.domain.home.arpa", RecordType: "TXT", RecordTTL: 60,
		Targets: []string{`"heritage=external-dns,external-dns/owner=test-owner,external-dns/resource=ingress/default/app"`},
	}
}

func TestWebhookOwnedLifecycleAndCommittedRetry(t *testing.T) {
	_, backend, handler, _ := fixture(t)
	a, txt := address(), ownership()
	create := Changes{Create: []Endpoint{a, txt}}
	backend.committedError = errors.New("response lost after commit")
	if response := request(t, handler, http.MethodPost, "/records", create); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("ambiguous send must be an explicit failure: %s", response.Body)
	}
	backend.committedError = nil
	if response := request(t, handler, http.MethodPost, "/records", create); response.Code != http.StatusNoContent || backend.writes != 1 {
		t.Fatalf("retry must confirm committed state without a second write: code=%d writes=%d", response.Code, backend.writes)
	}
	newAddress := a
	newAddress.Targets = []string{"198.18.0.51"}
	if response := request(t, handler, http.MethodPost, "/records", Changes{UpdateOld: []Endpoint{a}, UpdateNew: []Endpoint{newAddress}}); response.Code != http.StatusNoContent {
		t.Fatalf("owned update failed: %s", response.Body)
	}
	if response := request(t, handler, http.MethodPost, "/records", Changes{UpdateOld: []Endpoint{a}, UpdateNew: []Endpoint{address()}}); response.Code != http.StatusConflict {
		t.Fatalf("stale expected value was accepted: code=%d", response.Code)
	}
	if response := request(t, handler, http.MethodPost, "/records", Changes{Delete: []Endpoint{newAddress, txt}}); response.Code != http.StatusNoContent {
		t.Fatalf("owned deletion failed: %s", response.Body)
	}
	if response := request(t, handler, http.MethodPost, "/records", Changes{Delete: []Endpoint{newAddress, txt}}); response.Code != http.StatusNoContent || backend.writes != 3 {
		t.Fatalf("repeated deletion must confirm absence without another write: code=%d writes=%d", response.Code, backend.writes)
	}
	if len(backend.snapshot.Records) != 0 {
		t.Fatal("owned records remain after deletion")
	}
}

func TestWebhookNeverAdoptsManualRecordsAndDoesNotBlockNewNames(t *testing.T) {
	_, backend, handler, logs := fixture(t)
	manual := address()
	backend.snapshot.Records[manual.key()] = manual
	backend.snapshot.Names[manual.DNSName] = true
	if response := request(t, handler, http.MethodPost, "/records", Changes{Create: []Endpoint{manual, ownership()}}); response.Code != http.StatusConflict {
		t.Fatalf("matching existing address was adopted using forged ownership: %s", response.Body)
	}
	fresh := manual
	fresh.DNSName = "new.domain.home.arpa"
	response := request(t, handler, http.MethodPost, "/adjustendpoints", []Endpoint{manual, fresh})
	var adjusted []Endpoint
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &adjusted) != nil || len(adjusted) != 1 || adjusted[0].DNSName != fresh.DNSName {
		t.Fatalf("manual collision blocked unrelated new registrations: %s", response.Body)
	}
	if backend.writes != 0 || !strings.Contains(logs.String(), "explicit migration is required") {
		t.Fatal("manual conflict was not reported without mutation")
	}
}

func TestWebhookProtectedNamesDoNotBlockEligibleEndpoints(t *testing.T) {
	policy, backend, handler, logs := fixture(t)
	policy.protected["reserved.domain.home.arpa"] = true
	protected := address()
	protected.DNSName = "Reserved.DOMAIN.HOME.ARPA."
	protectedTXT := ownership()
	protectedTXT.DNSName = "dib-edns-a-reserved.domain.home.arpa"
	response := request(t, handler, http.MethodPost, "/adjustendpoints", []Endpoint{protected, address(), protectedTXT})
	var adjusted []Endpoint
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &adjusted) != nil ||
		len(adjusted) != 1 || !sameRecord(adjusted[0], address()) {
		t.Fatalf("protected name blocked the eligible endpoint: %s", response.Body)
	}
	if backend.writes != 0 || !strings.Contains(logs.String(), "application name is protected") {
		t.Fatal("protected names were not reported without mutation")
	}
	protected.DNSName = normalizeName(protected.DNSName)
	if response := request(t, handler, http.MethodPost, "/records", Changes{Create: []Endpoint{protected}}); response.Code != http.StatusBadRequest || backend.writes != 0 {
		t.Fatalf("direct mutation of a protected name was allowed: %s", response.Body)
	}
}

func TestWebhookProtectedNamesDoNotHideMalformedEndpoints(t *testing.T) {
	policy, backend, handler, _ := fixture(t)
	policy.protected["reserved.domain.home.arpa"] = true
	protected, malformed := address(), address()
	protected.DNSName = "reserved.domain.home.arpa"
	malformed.DNSName = "nested.app.domain.home.arpa"
	if response := request(t, handler, http.MethodPost, "/adjustendpoints", []Endpoint{protected, malformed}); response.Code != http.StatusBadRequest || backend.writes != 0 {
		t.Fatalf("malformed endpoint was hidden by protected-name filtering: %s", response.Body)
	}
}

func TestWebhookDualStackUsesIndependentTypedOwnership(t *testing.T) {
	_, backend, handler, _ := fixture(t)
	a, txtA := address(), ownership()
	aaaa, txtAAAA := a, txtA
	aaaa.RecordType, aaaa.Targets = "AAAA", []string{"fd12::50"}
	txtAAAA.DNSName = "dib-edns-aaaa-app.domain.home.arpa"
	create := Changes{Create: []Endpoint{a, aaaa, txtA, txtAAAA}}
	if response := request(t, handler, http.MethodPost, "/records", create); response.Code != http.StatusNoContent {
		t.Fatalf("official typed dual-stack ownership was rejected: %s", response.Body)
	}
	if response := request(t, handler, http.MethodPost, "/records", Changes{Delete: []Endpoint{aaaa, txtAAAA}}); response.Code != http.StatusNoContent {
		t.Fatalf("independent IPv6 removal failed: %s", response.Body)
	}
	if len(backend.snapshot.Records) != 2 || !sameRecord(backend.snapshot.Records[a.key()], a) ||
		!sameRecord(backend.snapshot.Records[txtA.key()], txtA) {
		t.Fatal("removing IPv6 changed the independently owned IPv4 records")
	}
	backend.snapshot.Records[aaaa.key()] = aaaa
	if response := request(t, handler, http.MethodPost, "/records", Changes{Create: []Endpoint{txtAAAA}}); response.Code != http.StatusConflict {
		t.Fatal("IPv4 ownership was used to adopt an unowned IPv6 record")
	}
}

func TestWebhookLabelLimitReservesLongestTypedOwnershipName(t *testing.T) {
	for length, expected := range map[int]int{49: http.StatusNoContent, 50: http.StatusBadRequest} {
		_, _, handler, _ := fixture(t)
		a, txt := address(), ownership()
		a.DNSName = strings.Repeat("a", length) + ".domain.home.arpa"
		txt.DNSName = "dib-edns-a-" + a.DNSName
		if response := request(t, handler, http.MethodPost, "/records", Changes{Create: []Endpoint{a, txt}}); response.Code != expected {
			t.Fatalf("wrong typed ownership label limit at %d: code=%d body=%s", length, response.Code, response.Body)
		}
	}
}

func TestWebhookRejectsUnsafeInputsBeforeMutation(t *testing.T) {
	cases := map[string]func(*Endpoint, *Endpoint){
		"outside zone":     func(a, _ *Endpoint) { a.DNSName = "app.other.home.arpa" },
		"nested name":      func(a, _ *Endpoint) { a.DNSName = "deep.app.domain.home.arpa" },
		"controller name":  func(a, _ *Endpoint) { a.DNSName = "dc1.domain.home.arpa" },
		"reserved name":    func(a, _ *Endpoint) { a.DNSName = "wpad.domain.home.arpa" },
		"apex":             func(a, _ *Endpoint) { a.DNSName = "domain.home.arpa" },
		"outside network":  func(a, _ *Endpoint) { a.Targets = []string{"203.0.113.1"} },
		"loopback":         func(a, _ *Endpoint) { a.Targets = []string{"127.0.0.1"} },
		"wrong family":     func(a, _ *Endpoint) { a.Targets = []string{"fd12::1"} },
		"unsupported type": func(a, _ *Endpoint) { a.RecordType = "NS" },
		"DNS injection":    func(a, _ *Endpoint) { a.Targets = []string{"198.18.0.1\nsend"} },
		"wrong owner": func(_, txt *Endpoint) {
			txt.Targets = []string{`"heritage=external-dns,external-dns/owner=other"`}
		},
		"duplicate owner": func(_, txt *Endpoint) {
			txt.Targets = []string{`"heritage=external-dns,external-dns/owner=,external-dns/owner=test-owner"`}
		},
	}
	for name, modify := range cases {
		t.Run(name, func(t *testing.T) {
			_, backend, handler, _ := fixture(t)
			a, txt := address(), ownership()
			modify(&a, &txt)
			response := request(t, handler, http.MethodPost, "/records", Changes{Create: []Endpoint{a, txt}})
			if response.Code != http.StatusBadRequest || backend.writes != 0 {
				t.Fatalf("unsafe request accepted: code=%d writes=%d body=%s", response.Code, backend.writes, response.Body)
			}
		})
	}
	t.Run("orphan address", func(t *testing.T) {
		_, backend, handler, _ := fixture(t)
		response := request(t, handler, http.MethodPost, "/records", Changes{Create: []Endpoint{address()}})
		if response.Code != http.StatusBadRequest || backend.writes != 0 {
			t.Fatal("address was created without ownership")
		}
	})
}

func TestWebhookListingFailureIsNotSuccessfulEmptyList(t *testing.T) {
	_, backend, handler, logs := fixture(t)
	backend.readError = errors.New("verified LDAPS authentication failed")
	for _, path := range []string{"/records", "/healthz"} {
		response := request(t, handler, http.MethodGet, path, nil)
		if response.Code != http.StatusServiceUnavailable || response.Body.String() == "[]\n" {
			t.Fatalf("backend failure hidden at %s: code=%d body=%s", path, response.Code, response.Body)
		}
	}
	if !strings.Contains(logs.String(), "authentication failed") {
		t.Fatal("backend failure was not logged")
	}
	backend.readError = nil
	response := request(t, handler, http.MethodGet, "/records", nil)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != MediaType || response.Body.String() != "[]\n" {
		t.Fatalf("genuine empty listing has wrong protocol shape: %s", response.Body)
	}
}

func TestWebhookBodyLimitAndTrailingJSON(t *testing.T) {
	_, backend, handler, _ := fixture(t)
	for name, body := range map[string]string{
		"trailing JSON":     "{} {}",
		"null request":      "null",
		"oversized payload": `{"create":[]}` + strings.Repeat(" ", 1024*1024),
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/records", strings.NewReader(body)))
			expected := http.StatusBadRequest
			if name == "oversized payload" {
				expected = http.StatusRequestEntityTooLarge
			}
			if response.Code != expected || backend.writes != 0 {
				t.Fatalf("wrong rejection: expected=%d actual=%d body=%s", expected, response.Code, response.Body)
			}
		})
	}
}
