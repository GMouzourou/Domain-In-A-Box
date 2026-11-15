package dnsbridge

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

var errProtectedName = errors.New("record is a protected application name")

type Property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Endpoint struct {
	DNSName          string            `json:"dnsName"`
	Targets          []string          `json:"targets"`
	RecordType       string            `json:"recordType"`
	RecordTTL        int64             `json:"recordTTL"`
	SetIdentifier    string            `json:"setIdentifier,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	ProviderSpecific []Property        `json:"providerSpecific,omitempty"`
}

type Changes struct {
	Create    []Endpoint `json:"create"`
	UpdateOld []Endpoint `json:"updateOld"`
	UpdateNew []Endpoint `json:"updateNew"`
	Delete    []Endpoint `json:"delete"`
}

type RecordKey struct {
	Name string
	Type string
}

func (e Endpoint) key() RecordKey {
	return RecordKey{Name: e.DNSName, Type: e.RecordType}
}

type Policy struct {
	cfg       Config
	networks  []netip.Prefix
	protected map[string]bool
}

func NewPolicy(cfg Config) (*Policy, error) {
	cfg, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	policy := &Policy{cfg: cfg, protected: make(map[string]bool)}
	for _, raw := range cfg.AllowedNetworks {
		network, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, err
		}
		policy.networks = append(policy.networks, network)
	}
	for _, name := range append([]string{"localhost", "wpad"}, cfg.ProtectedNames...) {
		name = normalizeName(name)
		if !strings.Contains(name, ".") {
			name += "." + cfg.Zone
		}
		if !validDomain(name) {
			return nil, fmt.Errorf("invalid protected DNS name %q", name)
		}
		policy.protected[name] = true
	}
	policy.protected[cfg.Zone] = true
	for _, controller := range cfg.Controllers {
		policy.protected[controller.Hostname] = true
	}
	return policy, nil
}

func (p *Policy) applicationName(name, recordType string) (string, error) {
	name = normalizeName(name)
	if !strings.HasSuffix(name, "."+p.cfg.Zone) {
		return "", errors.New("record is outside the managed zone")
	}
	label := strings.TrimSuffix(name, "."+p.cfg.Zone)
	if recordType == "TXT" {
		if !strings.HasPrefix(label, p.cfg.TXTPrefix) {
			return "", errors.New("only prefixed ownership TXT records are supported")
		}
		label = strings.TrimPrefix(label, p.cfg.TXTPrefix)
		if strings.HasPrefix(label, "aaaa-") {
			label = strings.TrimPrefix(label, "aaaa-")
		} else if strings.HasPrefix(label, "a-") {
			label = strings.TrimPrefix(label, "a-")
		} else {
			return "", errors.New("ownership TXT must identify its A or AAAA record family")
		}
	} else if strings.HasPrefix(label, p.cfg.TXTPrefix) {
		return "", errors.New("application name overlaps the ownership TXT namespace")
	}
	application := label + "." + p.cfg.Zone
	if !dnsLabelPattern.MatchString(label) || len(p.cfg.TXTPrefix)+len("aaaa-")+len(label) > 63 {
		return "", errors.New("record is not an eligible flat application name")
	}
	if p.protected[application] {
		return "", errProtectedName
	}
	return application, nil
}

func (p *Policy) normalize(endpoint Endpoint, desired bool) (Endpoint, error) {
	endpoint.DNSName = normalizeName(endpoint.DNSName)
	if endpoint.RecordType != "A" && endpoint.RecordType != "AAAA" && endpoint.RecordType != "TXT" {
		return endpoint, errors.New("only A, AAAA and ownership TXT records are supported")
	}
	if _, err := p.applicationName(endpoint.DNSName, endpoint.RecordType); err != nil {
		return endpoint, err
	}
	if endpoint.SetIdentifier != "" || len(endpoint.ProviderSpecific) != 0 {
		return endpoint, errors.New("provider-specific routing is unsupported")
	}
	if endpoint.RecordTTL < 0 || endpoint.RecordTTL > 86400 || len(endpoint.Targets) == 0 || len(endpoint.Targets) > 256 {
		return endpoint, errors.New("invalid record TTL or target count")
	}
	if desired && endpoint.RecordTTL == 0 {
		endpoint.RecordTTL = p.cfg.DefaultTTL
	}
	targets := make([]string, 0, len(endpoint.Targets))
	for _, target := range endpoint.Targets {
		if endpoint.RecordType == "TXT" {
			text, err := txtText(target)
			if err != nil || !p.ownedText(text) {
				return endpoint, errors.New("TXT metadata does not establish this bridge's ownership")
			}
			targets = append(targets, strconv.Quote(text))
			continue
		}
		address, err := netip.ParseAddr(target)
		if err != nil || !address.IsGlobalUnicast() || address.IsLoopback() || address.Is4In6() ||
			(endpoint.RecordType == "A") != address.Is4() {
			return endpoint, errors.New("target is not an address of the requested family")
		}
		allowed := false
		for _, network := range p.networks {
			allowed = allowed || network.Contains(address)
		}
		if !allowed {
			return endpoint, errors.New("target is outside allowed address networks")
		}
		targets = append(targets, address.String())
	}
	slices.Sort(targets)
	endpoint.Targets = slices.Compact(targets)
	return endpoint, nil
}

func txtText(target string) (string, error) {
	if strings.HasPrefix(target, `"`) {
		decoded, err := strconv.Unquote(target)
		if err != nil {
			return "", errors.New("invalid quoted TXT value")
		}
		target = decoded
	}
	if target == "" || len(target) > 2048 || strings.ContainsFunc(target, unicode.IsControl) {
		return "", errors.New("invalid TXT metadata")
	}
	return target, nil
}

func (p *Policy) ownedText(text string) bool {
	reader := csv.NewReader(strings.NewReader(text))
	reader.FieldsPerRecord = -1
	fields, err := reader.Read()
	if err != nil {
		return false
	}
	values := make(map[string]string)
	for _, field := range fields {
		key, value, found := strings.Cut(field, "=")
		_, duplicate := values[key]
		if !found || duplicate {
			return false
		}
		values[key] = value
	}
	return values["heritage"] == "external-dns" && values["external-dns/owner"] == p.cfg.OwnerID
}

func (p *Policy) owned(record Endpoint) bool {
	if len(record.Targets) == 0 {
		return false
	}
	for _, target := range record.Targets {
		text, err := txtText(target)
		if err != nil || !p.ownedText(text) {
			return false
		}
	}
	return true
}

func (p *Policy) ownershipKey(application, recordType string) RecordKey {
	return RecordKey{Name: p.cfg.TXTPrefix + strings.ToLower(recordType) + "-" + application, Type: "TXT"}
}

func (p *Policy) applicationKeys(application string) []RecordKey {
	return []RecordKey{{Name: application, Type: "A"}, {Name: application, Type: "AAAA"},
		p.ownershipKey(application, "A"), p.ownershipKey(application, "AAAA")}
}

func (p *Policy) applicationConflict(snapshot Snapshot, application string) bool {
	keys := p.applicationKeys(application)
	existing, owned := false, false
	for _, key := range keys {
		if snapshot.Foreign[key.Name] {
			return true
		}
		existing = existing || snapshot.Names[key.Name]
	}
	for i := 0; i < 2; i++ {
		_, hasAddress := snapshot.Records[keys[i]]
		txt, hasTXT := snapshot.Records[keys[i+2]]
		familyOwned := p.owned(txt)
		if (hasAddress || hasTXT) && !familyOwned {
			return true
		}
		owned = owned || familyOwned
	}
	return existing && !owned
}

func sameValues(a, b Endpoint) bool {
	return a.key() == b.key() && slices.Equal(a.Targets, b.Targets)
}

func sameRecord(a, b Endpoint) bool {
	return sameValues(a, b) && a.RecordTTL == b.RecordTTL
}
