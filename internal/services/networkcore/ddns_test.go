package networkcore

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// fakeZones applies the subset of nsupdate statements DIB generates, enforcing
// prerequisites atomically like a DNS server.
type fakeZones struct {
	records map[string][]string // "name TYPE" -> values
	refused map[string]bool
	updates int
}

func newFakeZones() *fakeZones {
	return &fakeZones{records: map[string][]string{}, refused: map[string]bool{}}
}

func (f *fakeZones) set(name, rrtype string, values ...string) {
	f.records[name+" "+rrtype] = values
}

func (f *fakeZones) get(name, rrtype string) []string {
	return f.records[name+" "+rrtype]
}

func (f *fakeZones) Update(_ context.Context, zone string, statements []string) (updateOutcome, error) {
	f.updates++
	if f.refused[zone] {
		return updateRefused, nil
	}
	pending := map[string][]string{}
	for key, values := range f.records {
		pending[key] = slices.Clone(values)
	}
	for _, statement := range statements {
		fields := strings.Fields(statement)
		switch {
		case fields[0] == "prereq" && fields[1] == "nxdomain":
			for key := range pending {
				if strings.HasPrefix(key, fields[2]+" ") && len(pending[key]) > 0 {
					return updatePrerequisiteFailed, nil
				}
			}
		case fields[0] == "prereq" && fields[1] == "yxrrset":
			if !slices.Contains(pending[fields[2]+" "+fields[3]], fields[4]) {
				return updatePrerequisiteFailed, nil
			}
		case fields[0] == "prereq" && fields[1] == "nxrrset":
			if len(pending[fields[2]+" "+fields[3]]) > 0 {
				return updatePrerequisiteFailed, nil
			}
		case fields[0] == "update" && fields[1] == "add":
			key := fields[2] + " " + fields[4]
			if !slices.Contains(pending[key], fields[5]) {
				pending[key] = append(pending[key], fields[5])
			}
		case fields[0] == "update" && fields[1] == "delete":
			key := fields[2] + " " + fields[3]
			if len(fields) == 4 {
				delete(pending, key)
			} else {
				pending[key] = slices.DeleteFunc(pending[key], func(value string) bool { return value == fields[4] })
			}
		default:
			return 0, fmt.Errorf("unsupported statement %q", statement)
		}
	}
	f.records = pending
	return updateApplied, nil
}

func (f *fakeZones) LookupTXT(_ context.Context, name string) ([]string, error) {
	var values []string
	for _, value := range f.get(name, "TXT") {
		values = append(values, strings.Trim(value, `"`))
	}
	return values, nil
}

func testDDNSSettings() ddnsSettings {
	return ddnsSettings{domain: "example.test", reverseZone: "2.0.192.in-addr.arpa", subnet: netip.MustParsePrefix("192.0.2.0/24"), hostname: "dib-0"}
}

func testDDNSChange(t *testing.T, changeType int, fqdn, address, dhcid string) ddnsChange {
	t.Helper()
	change, err := testDDNSSettings().validate(nameChangeRequest{ChangeType: changeType, ForwardChange: true, ReverseChange: true, FQDN: fqdn, IPAddress: address, DHCID: dhcid, LeaseLength: 3600})
	if err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	return change
}

func TestDecodeNameChangeRequestReadsKeaWireFormat(t *testing.T) {
	payload := `{"change-type":0,"forward-change":true,"reverse-change":false,"fqdn":"pc.example.test.","ip-address":"192.0.2.50","dhcid":"0102AB","lease-expires-on":"20260101120000","lease-length":7200,"use-conflict-resolution":true}`
	packet := binary.BigEndian.AppendUint16(nil, uint16(len(payload)))
	packet = append(packet, payload...)
	request, err := decodeNameChangeRequest(packet)
	if err != nil {
		t.Fatal(err)
	}
	if request.FQDN != "pc.example.test." || request.IPAddress != "192.0.2.50" || request.DHCID != "0102AB" || request.LeaseLength != 7200 || !request.ForwardChange || request.ReverseChange {
		t.Fatalf("decoded request = %#v", request)
	}
	if _, err := decodeNameChangeRequest(packet[:len(packet)-1]); err == nil {
		t.Fatal("decodeNameChangeRequest() accepted a truncated packet")
	}
}

func TestDDNSValidateRejectsUnsafeRequests(t *testing.T) {
	settings := testDDNSSettings()
	base := nameChangeRequest{ForwardChange: true, FQDN: "pc.example.test.", IPAddress: "192.0.2.50", DHCID: "0102ab", LeaseLength: 3600}
	tests := map[string]func(*nameChangeRequest){
		"other domain":      func(r *nameChangeRequest) { r.FQDN = "pc.example.org." },
		"nested name":       func(r *nameChangeRequest) { r.FQDN = "a.pc.example.test." },
		"zone apex":         func(r *nameChangeRequest) { r.FQDN = "example.test." },
		"reserved name":     func(r *nameChangeRequest) { r.FQDN = "wpad.example.test." },
		"controller name":   func(r *nameChangeRequest) { r.FQDN = "DIB-0.example.test." },
		"address off-link":  func(r *nameChangeRequest) { r.IPAddress = "198.51.100.5" },
		"IPv6 address":      func(r *nameChangeRequest) { r.IPAddress = "2001:db8::5" },
		"non-hex DHCID":     func(r *nameChangeRequest) { r.DHCID = "zz0102" },
		"unknown operation": func(r *nameChangeRequest) { r.ChangeType = 2 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := settings.validate(request); err == nil {
				t.Fatalf("validate() accepted %#v", request)
			}
		})
	}

	change, err := settings.validate(base)
	if err != nil {
		t.Fatal(err)
	}
	if change.ttl != 1200 || change.ptrName != "50.2.0.192.in-addr.arpa" || change.marker != "dib-dhcp=0102ab" {
		t.Fatalf("validated change = %#v", change)
	}
	base.ReverseChange = true
	base.IPAddress = "192.0.2.50"
	other := settings
	other.reverseZone = "3.0.192.in-addr.arpa"
	if change, _ := other.validate(base); change.reverse {
		t.Fatal("validate() requested a reverse update outside the managed reverse zone")
	}
}

func TestApplyDDNSChangeRegistersRenewsAndRemovesOwnedRecords(t *testing.T) {
	ctx := context.Background()
	zones := newFakeZones()
	settings := testDDNSSettings()
	add := testDDNSChange(t, 0, "pc.example.test.", "192.0.2.50", "0102ab")

	result, err := applyDDNSChange(ctx, zones, settings, add)
	if err != nil || result.forward != "applied" || result.reverse != "applied" {
		t.Fatalf("register = %#v, %v", result, err)
	}
	if got := zones.get("pc.example.test.", "A"); !slices.Equal(got, []string{"192.0.2.50"}) {
		t.Fatalf("A = %v", got)
	}
	if got := zones.get("50.2.0.192.in-addr.arpa.", "PTR"); !slices.Equal(got, []string{"pc.example.test."}) {
		t.Fatalf("PTR = %v", got)
	}

	moved := testDDNSChange(t, 0, "pc.example.test.", "192.0.2.51", "0102ab")
	if result, err := applyDDNSChange(ctx, zones, settings, moved); err != nil || result.forward != "applied" || result.reverse != "applied" {
		t.Fatalf("re-register at new address = %#v, %v", result, err)
	}
	if got := zones.get("pc.example.test.", "A"); !slices.Equal(got, []string{"192.0.2.51"}) {
		t.Fatalf("A after address change = %v", got)
	}

	remove := testDDNSChange(t, 1, "pc.example.test.", "192.0.2.51", "0102ab")
	if result, err := applyDDNSChange(ctx, zones, settings, remove); err != nil || result.forward != "applied" || result.reverse != "applied" {
		t.Fatalf("remove = %#v, %v", result, err)
	}
	for _, key := range []string{"pc.example.test. A", "pc.example.test. TXT", "51.2.0.192.in-addr.arpa. PTR", "51.2.0.192.in-addr.arpa. TXT"} {
		if len(zones.records[key]) > 0 {
			t.Fatalf("%s left after removal: %v", key, zones.records[key])
		}
	}
}

func TestApplyDDNSChangeNeverOverwritesRecordsItDoesNotOwn(t *testing.T) {
	ctx := context.Background()
	settings := testDDNSSettings()

	t.Run("manual forward record", func(t *testing.T) {
		zones := newFakeZones()
		zones.set("server.example.test.", "A", "192.0.2.5")
		result, err := applyDDNSChange(ctx, zones, settings, testDDNSChange(t, 0, "server.example.test.", "192.0.2.60", "0102ab"))
		if err != nil || result.forward != "not-owned" || result.reverse != "" {
			t.Fatalf("result = %#v, %v", result, err)
		}
		if got := zones.get("server.example.test.", "A"); !slices.Equal(got, []string{"192.0.2.5"}) {
			t.Fatalf("manual A changed to %v", got)
		}
	})

	t.Run("another DHCP client", func(t *testing.T) {
		zones := newFakeZones()
		if _, err := applyDDNSChange(ctx, zones, settings, testDDNSChange(t, 0, "pc.example.test.", "192.0.2.50", "0102ab")); err != nil {
			t.Fatal(err)
		}
		result, err := applyDDNSChange(ctx, zones, settings, testDDNSChange(t, 0, "pc.example.test.", "192.0.2.70", "0a0b0c"))
		if err != nil || result.forward != "not-owned" {
			t.Fatalf("result = %#v, %v", result, err)
		}
		removal, err := applyDDNSChange(ctx, zones, settings, testDDNSChange(t, 1, "pc.example.test.", "192.0.2.50", "0a0b0c"))
		if err != nil || removal.forward != "not-owned" || removal.reverse != "not-owned" {
			t.Fatalf("foreign removal = %#v, %v", removal, err)
		}
		if got := zones.get("pc.example.test.", "A"); !slices.Equal(got, []string{"192.0.2.50"}) {
			t.Fatalf("A = %v", got)
		}
	})

	t.Run("manual reverse record", func(t *testing.T) {
		zones := newFakeZones()
		zones.set("50.2.0.192.in-addr.arpa.", "PTR", "printer.example.test.")
		result, err := applyDDNSChange(ctx, zones, settings, testDDNSChange(t, 0, "pc.example.test.", "192.0.2.50", "0102ab"))
		if err != nil || result.forward != "applied" || result.reverse != "not-owned" {
			t.Fatalf("result = %#v, %v", result, err)
		}
		if got := zones.get("50.2.0.192.in-addr.arpa.", "PTR"); !slices.Equal(got, []string{"printer.example.test."}) {
			t.Fatalf("manual PTR changed to %v", got)
		}
	})

	t.Run("reverse address reused by another client", func(t *testing.T) {
		zones := newFakeZones()
		if _, err := applyDDNSChange(ctx, zones, settings, testDDNSChange(t, 0, "old.example.test.", "192.0.2.50", "0102ab")); err != nil {
			t.Fatal(err)
		}
		result, err := applyDDNSChange(ctx, zones, settings, testDDNSChange(t, 0, "new.example.test.", "192.0.2.50", "0a0b0c"))
		if err != nil || result.reverse != "applied" {
			t.Fatalf("result = %#v, %v", result, err)
		}
		if got := zones.get("50.2.0.192.in-addr.arpa.", "PTR"); !slices.Equal(got, []string{"new.example.test."}) {
			t.Fatalf("PTR = %v", got)
		}
		if got := zones.get("50.2.0.192.in-addr.arpa.", "TXT"); !slices.Equal(got, []string{`"dib-dhcp=0a0b0c"`}) {
			t.Fatalf("reverse marker = %v", got)
		}
	})
}

func TestApplyDDNSChangeKeepsMarkerWhileOtherAddressesRemain(t *testing.T) {
	zones := newFakeZones()
	settings := testDDNSSettings()
	zones.set("pc.example.test.", "A", "192.0.2.50", "192.0.2.52")
	zones.set("pc.example.test.", "TXT", `"dib-dhcp=0102ab"`)
	change := testDDNSChange(t, 1, "pc.example.test.", "192.0.2.50", "0102ab")
	change.reverse = false
	if _, err := applyDDNSChange(context.Background(), zones, settings, change); err != nil {
		t.Fatal(err)
	}
	if got := zones.get("pc.example.test.", "A"); !slices.Equal(got, []string{"192.0.2.52"}) {
		t.Fatalf("A = %v", got)
	}
	if got := zones.get("pc.example.test.", "TXT"); len(got) != 1 {
		t.Fatalf("marker removed while an address remains: %v", got)
	}
}

func TestClassifyNSUpdateFailure(t *testing.T) {
	tests := map[string]updateOutcome{
		"update failed: YXDOMAIN": updatePrerequisiteFailed,
		"update failed: NXRRSET":  updatePrerequisiteFailed,
		"update failed: REFUSED":  updateRefused,
	}
	for output, want := range tests {
		if got, ok := classifyNSUpdateFailure(output); !ok || got != want {
			t.Fatalf("classifyNSUpdateFailure(%q) = %v, %v", output, got, ok)
		}
	}
	if _, ok := classifyNSUpdateFailure("; Communication with 127.0.0.1#53 failed: timed out"); ok {
		t.Fatal("transport failure was classified as a DNS response")
	}
}
