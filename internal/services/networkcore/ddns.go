package networkcore

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Kea's DHCPv4 server sends NameChangeRequests to this listener instead of
// kea-dhcp-ddns. Samba's internal DNS answers NOTIMP for DHCID records, so
// kea-dhcp-ddns conflict detection cannot work against it, and its GSS-TSIG
// hook is not packaged. DIB applies updates with nsupdate -g and marks the
// records it owns with a TXT record carrying the client's DHCID.
const (
	ddnsListenPort       = 53001
	ddnsListenAddress    = "127.0.0.1:53001"
	ddnsCredentialsCache = "FILE:/run/kea/keaddns.ccache" //nolint:gosec // G101: a cache path, not a credential.
	ddnsKeytab           = "/var/lib/kea/keaddns.keytab"
	ddnsMarkerPrefix     = "dib-dhcp="
	ddnsQueueSize        = 1024
	ddnsTicketLifetime   = time.Hour
)

var (
	ddnsLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	ddnsDHCIDPattern = regexp.MustCompile(`^[0-9a-f]{6,256}$`)
	ddnsReserved     = map[string]bool{"localhost": true, "wpad": true, "isatap": true, "gc": true, "domaindnszones": true, "forestdnszones": true}
)

type nameChangeRequest struct {
	ChangeType    int    `json:"change-type"`
	ForwardChange bool   `json:"forward-change"`
	ReverseChange bool   `json:"reverse-change"`
	FQDN          string `json:"fqdn"`
	IPAddress     string `json:"ip-address"`
	DHCID         string `json:"dhcid"`
	LeaseLength   uint32 `json:"lease-length"`
}

func decodeNameChangeRequest(packet []byte) (nameChangeRequest, error) {
	if len(packet) < 2 {
		return nameChangeRequest{}, errors.New("NameChangeRequest is shorter than its length prefix")
	}
	length := int(binary.BigEndian.Uint16(packet))
	if length != len(packet)-2 {
		return nameChangeRequest{}, fmt.Errorf("NameChangeRequest length prefix %d does not match payload size %d", length, len(packet)-2)
	}
	var request nameChangeRequest
	if err := json.Unmarshal(packet[2:], &request); err != nil {
		return nameChangeRequest{}, fmt.Errorf("parse NameChangeRequest: %w", err)
	}
	return request, nil
}

type ddnsSettings struct {
	domain      string
	reverseZone string
	subnet      netip.Prefix
	hostname    string
}

// ddnsChange is a validated request limited to one flat name in the managed
// domain and an address inside the served subnet.
type ddnsChange struct {
	add     bool
	forward bool
	reverse bool
	name    string
	label   string
	address netip.Addr
	ptrName string
	marker  string
	ttl     uint32
}

func (s ddnsSettings) validate(request nameChangeRequest) (ddnsChange, error) {
	if request.ChangeType != 0 && request.ChangeType != 1 {
		return ddnsChange{}, fmt.Errorf("unsupported change type %d", request.ChangeType)
	}
	name := strings.TrimSuffix(strings.ToLower(request.FQDN), ".")
	label, found := strings.CutSuffix(name, "."+s.domain)
	if !found || !ddnsLabelPattern.MatchString(label) {
		return ddnsChange{}, fmt.Errorf("name %q is not a single host label in %s", request.FQDN, s.domain)
	}
	if ddnsReserved[label] || label == s.hostname {
		return ddnsChange{}, fmt.Errorf("name %q is reserved", request.FQDN)
	}
	address, err := netip.ParseAddr(request.IPAddress)
	if err != nil || !address.Is4() || !s.subnet.Contains(address) {
		return ddnsChange{}, fmt.Errorf("address %q is not an IPv4 address in %s", request.IPAddress, s.subnet)
	}
	dhcid := strings.ToLower(request.DHCID)
	if len(dhcid)%2 != 0 || !ddnsDHCIDPattern.MatchString(dhcid) {
		return ddnsChange{}, fmt.Errorf("DHCID for %q is not hexadecimal", request.FQDN)
	}
	octets := address.As4()
	ptrName := fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa", octets[3], octets[2], octets[1], octets[0])
	reverse := request.ReverseChange && strings.HasSuffix(ptrName, "."+s.reverseZone)
	ttl := request.LeaseLength / 3
	if ttl < 300 {
		ttl = 300
	} else if ttl > 86400 {
		ttl = 86400
	}
	return ddnsChange{
		add:     request.ChangeType == 0,
		forward: request.ForwardChange,
		reverse: reverse,
		name:    name,
		label:   label,
		address: address,
		ptrName: ptrName,
		marker:  ddnsMarkerPrefix + dhcid,
		ttl:     ttl,
	}, nil
}

type updateOutcome int

const (
	updateApplied updateOutcome = iota
	updatePrerequisiteFailed
	updateRefused
)

type ddnsUpdater interface {
	Update(ctx context.Context, zone string, statements []string) (updateOutcome, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

type ddnsResult struct {
	forward string
	reverse string
}

// applyDDNSChange registers or removes a lease's records. Each record set is
// paired with a TXT ownership marker carrying the client's DHCID, so records
// created manually, by AD clients or for another DHCP client are never changed.
func applyDDNSChange(ctx context.Context, updater ddnsUpdater, s ddnsSettings, change ddnsChange) (ddnsResult, error) {
	var result ddnsResult
	marker := quoteTXT(change.marker)
	name := change.name + "."
	if change.forward {
		var transactions [][]string
		if change.add {
			transactions = [][]string{
				{"prereq nxdomain " + name, fmt.Sprintf("update add %s %d A %s", name, change.ttl, change.address), fmt.Sprintf("update add %s %d TXT %s", name, change.ttl, marker)},
				{"prereq yxrrset " + name + " TXT " + marker, "update delete " + name + " A", fmt.Sprintf("update add %s %d A %s", name, change.ttl, change.address)},
			}
		} else {
			transactions = [][]string{
				{"prereq yxrrset " + name + " TXT " + marker, fmt.Sprintf("update delete %s A %s", name, change.address)},
			}
		}
		status, err := firstApplied(ctx, updater, s.domain, transactions)
		if err != nil {
			return result, err
		}
		result.forward = status
		if !change.add && status == "applied" {
			if _, err := updater.Update(ctx, s.domain, []string{
				"prereq yxrrset " + name + " TXT " + marker,
				"prereq nxrrset " + name + " A",
				"prereq nxrrset " + name + " AAAA",
				"update delete " + name + " TXT " + marker,
			}); err != nil {
				return result, err
			}
		}
		if change.add && status != "applied" {
			return result, nil
		}
	}
	if change.reverse {
		ptr := change.ptrName + "."
		var transactions [][]string
		if change.add {
			transactions = [][]string{
				{"prereq nxdomain " + ptr, fmt.Sprintf("update add %s %d PTR %s", ptr, change.ttl, name), fmt.Sprintf("update add %s %d TXT %s", ptr, change.ttl, marker)},
			}
			values, err := updater.LookupTXT(ctx, ptr)
			if err != nil {
				return result, err
			}
			var owned []string
			for _, value := range values {
				if strings.HasPrefix(value, ddnsMarkerPrefix) {
					owned = append(owned, value)
				}
			}
			if len(owned) == 1 {
				previous := quoteTXT(owned[0])
				transactions = append(transactions, []string{
					"prereq yxrrset " + ptr + " TXT " + previous,
					"update delete " + ptr + " PTR",
					"update delete " + ptr + " TXT " + previous,
					fmt.Sprintf("update add %s %d PTR %s", ptr, change.ttl, name),
					fmt.Sprintf("update add %s %d TXT %s", ptr, change.ttl, marker),
				})
			}
		} else {
			transactions = [][]string{
				{"prereq yxrrset " + ptr + " TXT " + marker, "update delete " + ptr + " PTR", "update delete " + ptr + " TXT " + marker},
			}
		}
		status, err := firstApplied(ctx, updater, s.reverseZone, transactions)
		if err != nil {
			return result, err
		}
		result.reverse = status
	}
	return result, nil
}

func firstApplied(ctx context.Context, updater ddnsUpdater, zone string, transactions [][]string) (string, error) {
	for _, statements := range transactions {
		outcome, err := updater.Update(ctx, zone, statements)
		if err != nil {
			return "", err
		}
		switch outcome {
		case updateApplied:
			return "applied", nil
		case updateRefused:
			return "refused", nil
		}
	}
	return "not-owned", nil
}

func quoteTXT(value string) string {
	return `"` + value + `"`
}

type nsupdateUpdater struct {
	server   string
	resolver *net.Resolver
	tickets  *ddnsTickets
}

func (u *nsupdateUpdater) Update(ctx context.Context, zone string, statements []string) (updateOutcome, error) {
	var script strings.Builder
	fmt.Fprintf(&script, "server %s\nzone %s.\n", u.server, zone)
	for _, statement := range statements {
		script.WriteString(statement + "\n")
	}
	script.WriteString("send\n")
	for attempt := 0; ; attempt++ {
		if err := u.tickets.ensure(ctx, attempt > 0); err != nil {
			return 0, err
		}
		updateContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		command := exec.CommandContext(updateContext, "nsupdate", "-g", "-v", "-t", "20")
		command.Env = append(os.Environ(), "KRB5CCNAME="+ddnsCredentialsCache)
		command.Stdin = strings.NewReader(script.String())
		output, err := command.CombinedOutput()
		cancel()
		if err == nil {
			return updateApplied, nil
		}
		outcome, classified := classifyNSUpdateFailure(string(output))
		if classified {
			return outcome, nil
		}
		if attempt == 0 && strings.Contains(strings.ToLower(string(output)), "gss") {
			continue
		}
		return 0, fmt.Errorf("nsupdate failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
}

func classifyNSUpdateFailure(output string) (updateOutcome, bool) {
	for _, code := range []string{"YXDOMAIN", "NXDOMAIN", "YXRRSET", "NXRRSET"} {
		if strings.Contains(output, "update failed: "+code) {
			return updatePrerequisiteFailed, true
		}
	}
	if strings.Contains(output, "update failed: REFUSED") || strings.Contains(output, "update failed: NOTAUTH") {
		return updateRefused, true
	}
	return 0, false
}

func (u *nsupdateUpdater) LookupTXT(ctx context.Context, name string) ([]string, error) {
	values, err := u.resolver.LookupTXT(ctx, name)
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) && dnsError.IsNotFound {
		return nil, nil
	}
	return values, err
}

type ddnsTickets struct {
	principal string
	acquired  time.Time
}

func (t *ddnsTickets) ensure(ctx context.Context, force bool) error {
	if !force && time.Since(t.acquired) < ddnsTicketLifetime {
		return nil
	}
	if _, err := os.Stat(ddnsKeytab); err != nil {
		return fmt.Errorf("Kea DDNS keytab is not available yet: %w", err)
	}
	command := exec.CommandContext(ctx, "kinit", "-kt", ddnsKeytab, t.principal)
	command.Env = append(os.Environ(), "KRB5CCNAME="+ddnsCredentialsCache)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("obtain DDNS Kerberos ticket: %w: %s", err, strings.TrimSpace(string(output)))
	}
	t.acquired = time.Now()
	return nil
}

func ddnsSettingsFromEnvironment() (ddnsSettings, string, error) {
	values := map[string]string{}
	for _, key := range []string{"DNS_DOMAIN", "REVERSE_ZONE", "SUBNET", "CONTAINER_HOSTNAME", "DIB_REALM"} {
		value, err := environment(key)
		if err != nil {
			return ddnsSettings{}, "", err
		}
		values[key] = value
	}
	subnet, err := netip.ParsePrefix(values["SUBNET"])
	if err != nil || !subnet.Addr().Is4() {
		return ddnsSettings{}, "", fmt.Errorf("SUBNET must be an IPv4 prefix, got %q", values["SUBNET"])
	}
	settings := ddnsSettings{
		domain:      strings.ToLower(values["DNS_DOMAIN"]),
		reverseZone: strings.ToLower(values["REVERSE_ZONE"]),
		subnet:      subnet.Masked(),
		hostname:    strings.ToLower(values["CONTAINER_HOSTNAME"]),
	}
	return settings, keaDDNSAccount(settings.hostname) + "@" + strings.ToUpper(values["DIB_REALM"]), nil
}

// runDDNS receives Kea NameChangeRequests and applies them in order. The queue
// is bounded like kea-dhcp-ddns; a lost request is repaired at lease renewal
// because Kea is configured to send updates on renew.
func (r *Runner) runDDNS(ctx context.Context) error {
	settings, principal, err := ddnsSettingsFromEnvironment()
	if err != nil {
		return err
	}
	connection, err := net.ListenPacket("udp4", ddnsListenAddress)
	if err != nil {
		return fmt.Errorf("listen for Kea NameChangeRequests: %w", err)
	}
	defer connection.Close()
	go func() {
		<-ctx.Done()
		connection.Close()
	}()
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, "127.0.0.1:53")
	}}
	updater := &nsupdateUpdater{server: "127.0.0.1", resolver: resolver, tickets: &ddnsTickets{principal: principal}}
	queue := make(chan nameChangeRequest, ddnsQueueSize)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for request := range queue {
			r.processDDNSRequest(ctx, updater, settings, request)
		}
	}()
	r.log.Infof("DHCP DDNS listening on %s for %s and %s", ddnsListenAddress, settings.domain, settings.reverseZone)
	buffer := make([]byte, 65535)
	for {
		size, _, err := connection.ReadFrom(buffer)
		if err != nil {
			close(queue)
			<-done
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("receive Kea NameChangeRequest: %w", err)
		}
		request, err := decodeNameChangeRequest(buffer[:size])
		if err != nil {
			r.log.Warnf("ignoring malformed NameChangeRequest: %v", err)
			continue
		}
		select {
		case queue <- request:
		default:
			r.log.Warnf("DDNS queue is full; dropping update for %s (it is retried at lease renewal)", request.FQDN)
		}
	}
}

func (r *Runner) processDDNSRequest(ctx context.Context, updater ddnsUpdater, settings ddnsSettings, request nameChangeRequest) {
	change, err := settings.validate(request)
	if err != nil {
		r.log.Warnf("ignoring DDNS request: %v", err)
		return
	}
	action := "remove"
	if change.add {
		action = "register"
	}
	delays := []time.Duration{time.Second, 5 * time.Second, 15 * time.Second}
	for attempt := 0; ; attempt++ {
		result, err := applyDDNSChange(ctx, updater, settings, change)
		if err == nil {
			r.log.Infof("DDNS %s %s %s: forward=%s reverse=%s", action, change.name, change.address, valueOr(result.forward, "skipped"), valueOr(result.reverse, "skipped"))
			return
		}
		if attempt >= len(delays) || ctx.Err() != nil {
			r.log.Errorf("DDNS %s %s %s failed: %v", action, change.name, change.address, err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delays[attempt]):
		}
	}
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
