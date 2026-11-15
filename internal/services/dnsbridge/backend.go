package dnsbridge

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-ldap/ldap/v3"
)

type DNSBackend struct {
	cfg    Config
	policy *Policy
	roots  *x509.CertPool
}

func NewBackend(cfg Config, policy *Policy) (*DNSBackend, error) {
	data, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read trusted DNS CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, errors.New("trusted DNS CA file contains no PEM certificates")
	}
	if _, err := readPassword(cfg.PasswordFile); err != nil {
		return nil, err
	}
	return &DNSBackend{cfg: cfg, policy: policy, roots: roots}, nil
}

func (b *DNSBackend) Records(ctx context.Context) (Snapshot, error) {
	password, err := readPassword(b.cfg.PasswordFile)
	if err != nil {
		return Snapshot{}, err
	}
	var failures []error
	for _, controller := range b.cfg.Controllers {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		snapshot, err := b.readController(ctx, controller, password)
		if err == nil {
			return snapshot, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", controller.Hostname, err))
	}
	return Snapshot{}, fmt.Errorf("authenticated DNS listing failed: %w", errors.Join(failures...))
}

func (b *DNSBackend) readController(ctx context.Context, controller Controller, password string) (Snapshot, error) {
	connection, err := ldap.DialURL("ldaps://"+net.JoinHostPort(controller.Address, "636"),
		ldap.DialWithDialer(&net.Dialer{Timeout: 8 * time.Second}),
		ldap.DialWithTLSConfig(&tls.Config{
			RootCAs: b.roots, ServerName: controller.Hostname, MinVersion: tls.VersionTLS12,
		}))
	if err != nil {
		return Snapshot{}, fmt.Errorf("verified LDAPS connection failed: %w", err)
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	connection.SetTimeout(8 * time.Second)
	if err := connection.Bind(b.cfg.Username+"@"+b.cfg.Realm, password); err != nil {
		return Snapshot{}, fmt.Errorf("LDAPS authentication failed: %w", err)
	}
	var components []string
	for _, label := range strings.Split(strings.ToLower(b.cfg.Realm), ".") {
		components = append(components, "DC="+ldap.EscapeDN(label))
	}
	base := "DC=" + ldap.EscapeDN(b.cfg.Zone) + ",CN=MicrosoftDNS,DC=DomainDnsZones," + strings.Join(components, ",")
	request := ldap.NewSearchRequest(base, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, 8, false, "(&(objectClass=dnsNode)(!(dnsTombstoned=TRUE)))", []string{"name", "dnsRecord"}, nil)
	result, err := connection.SearchWithPaging(request, 500)
	if err != nil {
		return Snapshot{}, fmt.Errorf("paged DNS record enumeration failed: %w", err)
	}
	snapshot := Snapshot{
		Controller: controller, Records: make(map[RecordKey]Endpoint),
		Names: make(map[string]bool), Foreign: make(map[string]bool),
	}
	for _, entry := range result.Entries {
		label := entry.GetAttributeValue("name")
		if label == "" {
			return Snapshot{}, errors.New("DNS node is missing its name")
		}
		name := normalizeName(label + "." + b.cfg.Zone)
		if label == "@" {
			name = b.cfg.Zone
		}
		snapshot.Names[name] = true
		for _, value := range entry.GetRawAttributeValues("dnsRecord") {
			record, supported, err := decodeRecord(name, value)
			if err != nil {
				return Snapshot{}, fmt.Errorf("decode DNS record at %s: %w", name, err)
			}
			if !supported {
				snapshot.Foreign[name] = true
				continue
			}
			if _, err := b.policy.applicationName(name, record.RecordType); err != nil {
				snapshot.Foreign[name] = true
				continue
			}
			key := record.key()
			if previous, exists := snapshot.Records[key]; exists {
				record.Targets = append(previous.Targets, record.Targets...)
				record.RecordTTL = min(previous.RecordTTL, record.RecordTTL)
			}
			slices.Sort(record.Targets)
			record.Targets = slices.Compact(record.Targets)
			snapshot.Records[key] = record
		}
	}
	return snapshot, nil
}

func decodeRecord(name string, data []byte) (Endpoint, bool, error) {
	record := Endpoint{DNSName: name}
	if len(data) < 24 || int(binary.LittleEndian.Uint16(data[:2])) != len(data)-24 || data[4] != 5 {
		return record, false, errors.New("invalid AD DNS record header")
	}
	record.RecordTTL = int64(binary.BigEndian.Uint32(data[12:16]))
	body := data[24:]
	switch binary.LittleEndian.Uint16(data[2:4]) {
	case 1:
		if len(body) != 4 {
			return record, false, errors.New("invalid A record length")
		}
		record.RecordType = "A"
		record.Targets = []string{netip.AddrFrom4([4]byte(body)).String()}
	case 28:
		if len(body) != 16 {
			return record, false, errors.New("invalid AAAA record length")
		}
		record.RecordType = "AAAA"
		record.Targets = []string{netip.AddrFrom16([16]byte(body)).String()}
	case 16:
		record.RecordType = "TXT"
		if len(body) == 0 {
			return record, false, errors.New("missing TXT string")
		}
		rest := body
		var text strings.Builder
		for len(rest) > 0 {
			if len(rest)-1 < int(rest[0]) {
				return record, false, errors.New("truncated TXT string")
			}
			length := int(rest[0])
			text.Write(rest[1 : 1+length])
			rest = rest[1+length:]
		}
		if !utf8.ValidString(text.String()) {
			return record, false, errors.New("invalid TXT encoding")
		}
		record.Targets = []string{strconv.Quote(text.String())}
	default:
		return record, false, nil
	}
	return record, true, nil
}

func (b *DNSBackend) Apply(ctx context.Context, snapshot Snapshot, mutation Mutation) (err error) {
	directory, err := os.MkdirTemp("", "dib-dns-ticket-")
	if err != nil {
		return fmt.Errorf("create protected Kerberos work directory: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(directory)) }()
	var configuration strings.Builder
	fmt.Fprintf(&configuration, "[libdefaults]\n default_realm = %s\n dns_lookup_kdc = false\n dns_lookup_realm = false\n rdns = false\n dns_canonicalize_hostname = false\n[realms]\n %s = {\n", b.cfg.Realm, b.cfg.Realm)
	fmt.Fprintf(&configuration, " kdc = %s\n", net.JoinHostPort(snapshot.Controller.Address, "88"))
	for _, controller := range b.cfg.Controllers {
		if controller != snapshot.Controller {
			fmt.Fprintf(&configuration, " kdc = %s\n", net.JoinHostPort(controller.Address, "88"))
		}
	}
	configuration.WriteString(" }\n")
	path := filepath.Join(directory, "krb5.conf")
	if err := os.WriteFile(path, []byte(configuration.String()), 0600); err != nil {
		return fmt.Errorf("write Kerberos configuration: %w", err)
	}
	environment := append(os.Environ(), "KRB5_CONFIG="+path, "KRB5CCNAME=FILE:"+filepath.Join(directory, "ticket"))
	password, err := readPassword(b.cfg.PasswordFile)
	if err != nil {
		return err
	}
	kinit := exec.CommandContext(ctx, "kinit", b.cfg.Username+"@"+b.cfg.Realm)
	kinit.Env, kinit.Stdin = environment, strings.NewReader(password+"\n")
	if output, err := kinit.CombinedOutput(); err != nil {
		return fmt.Errorf("Kerberos authentication failed: %w: %s", err, strings.ReplaceAll(string(output), password, "[REDACTED]"))
	}
	payload, err := updateScript(b.cfg, snapshot, mutation)
	if err != nil {
		return err
	}
	update := exec.CommandContext(ctx, "nsupdate", "-g", "-v")
	update.Env, update.Stdin = environment, strings.NewReader(payload)
	if output, err := update.CombinedOutput(); err != nil {
		return fmt.Errorf("GSS-TSIG update failed: %w: %s", err, strings.ReplaceAll(string(output), password, "[REDACTED]"))
	}
	return nil
}

func updateScript(cfg Config, snapshot Snapshot, mutation Mutation) (string, error) {
	var script strings.Builder
	fmt.Fprintf(&script, "server %s\nrealm %s\nzone %s.\n", snapshot.Controller.Hostname, cfg.Realm, cfg.Zone)
	keys := make([]RecordKey, 0, len(mutation.Before)+len(mutation.After))
	seen := make(map[RecordKey]bool)
	for _, name := range mutation.Names {
		if !snapshot.Names[name] {
			fmt.Fprintf(&script, "prereq nxdomain %s.\n", name)
		} else {
			for _, kind := range []string{"A", "AAAA", "TXT"} {
				key := RecordKey{Name: name, Type: kind}
				if record, exists := mutation.Before[key]; exists {
					for _, target := range record.Targets {
						presentation, err := presentationTarget(record.RecordType, target)
						if err != nil {
							return "", err
						}
						fmt.Fprintf(&script, "prereq yxrrset %s. %s %s\n", name, kind, presentation)
					}
				} else {
					fmt.Fprintf(&script, "prereq nxrrset %s. %s\n", name, kind)
				}
			}
		}
	}
	for key := range mutation.Before {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range mutation.After {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b RecordKey) int {
		if order := strings.Compare(a.Name, b.Name); order != 0 {
			return order
		}
		return strings.Compare(a.Type, b.Type)
	})
	for _, key := range keys {
		before, existed := mutation.Before[key]
		after, remains := mutation.After[key]
		if existed && remains && sameRecord(before, after) {
			continue
		}
		if existed {
			for _, target := range before.Targets {
				presentation, err := presentationTarget(key.Type, target)
				if err != nil {
					return "", err
				}
				fmt.Fprintf(&script, "update delete %s. %s %s\n", key.Name, key.Type, presentation)
			}
		}
		if remains {
			for _, target := range after.Targets {
				presentation, err := presentationTarget(key.Type, target)
				if err != nil {
					return "", err
				}
				fmt.Fprintf(&script, "update add %s. %d %s %s\n", key.Name, after.RecordTTL, key.Type, presentation)
			}
		}
	}
	script.WriteString("send\n")
	return script.String(), nil
}

func presentationTarget(kind, target string) (string, error) {
	if kind != "TXT" {
		address, err := netip.ParseAddr(target)
		if err != nil {
			return "", err
		}
		return address.String(), nil
	}
	text, err := txtText(target)
	if err != nil {
		return "", err
	}
	var chunks []string
	data := []byte(text)
	for len(data) > 0 {
		end := min(255, len(data))
		for end > 0 && !utf8.Valid(data[:end]) {
			end--
		}
		if end == 0 {
			return "", errors.New("invalid TXT encoding")
		}
		var quoted bytes.Buffer
		quoted.WriteByte('"')
		for _, value := range data[:end] {
			if value == '"' || value == '\\' {
				quoted.WriteByte('\\')
			}
			quoted.WriteByte(value)
		}
		quoted.WriteByte('"')
		chunks = append(chunks, quoted.String())
		data = data[end:]
	}
	return strings.Join(chunks, " "), nil
}
