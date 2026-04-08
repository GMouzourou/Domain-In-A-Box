package network

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
)

// DHCPLease captures the key options from a dhclient lease file.
type DHCPLease struct {
	FixedAddress string
	Routers      []string
	DNSServers   []string
	DomainSearch []string
	DomainName   string
}

// ReadLatestDHCPLease reads and parses the latest DHCP lease from the given paths.
func ReadLatestDHCPLease(paths ...string) (DHCPLease, error) {
	if len(paths) == 0 {
		paths = []string{"/var/lib/dhcp/dhclient.leases"}
	}

	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(content)) == "" {
			continue
		}
		return ParseDHCPLease(string(content))
	}

	return DHCPLease{}, fmt.Errorf("no DHCP lease data found in %v", paths)
}

// ParseDHCPLease parses the latest lease block from a dhclient lease file.
func ParseDHCPLease(content string) (DHCPLease, error) {
	blocks := regexp.MustCompile(`(?s)lease\s*\{(.*?)\}`).FindAllStringSubmatch(content, -1)
	if len(blocks) == 0 {
		return DHCPLease{}, fmt.Errorf("no DHCP lease blocks found")
	}

	block := blocks[len(blocks)-1][1]
	lease := DHCPLease{
		FixedAddress: captureLeaseValue(block, `fixed-address\s+([^;]+);`),
		Routers:      splitLeaseList(captureLeaseValue(block, `option routers\s+([^;]+);`)),
		DNSServers:   splitLeaseList(captureLeaseValue(block, `option domain-name-servers\s+([^;]+);`)),
		DomainSearch: splitLeaseList(captureLeaseValue(block, `option domain-search\s+([^;]+);`)),
		DomainName:   strings.Trim(captureLeaseValue(block, `option domain-name\s+([^;]+);`), `" `),
	}

	if lease.FixedAddress == "" {
		return DHCPLease{}, fmt.Errorf("lease is missing a fixed-address")
	}

	return lease, nil
}

// AddressInPool verifies that an IPv4 address falls inside a start-end DHCP pool.
func AddressInPool(ip, pool string) error {
	parts := strings.SplitN(pool, "-", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid DHCP pool %q", pool)
	}

	addr := net.ParseIP(strings.TrimSpace(ip)).To4()
	start := net.ParseIP(strings.TrimSpace(parts[0])).To4()
	end := net.ParseIP(strings.TrimSpace(parts[1])).To4()
	if addr == nil || start == nil || end == nil {
		return fmt.Errorf("failed to parse DHCP pool or IP (%q in %q)", ip, pool)
	}

	if compareIPv4(addr, start) < 0 || compareIPv4(addr, end) > 0 {
		return fmt.Errorf("IP %s is outside DHCP pool %s", ip, pool)
	}

	return nil
}

func captureLeaseValue(block, pattern string) string {
	match := regexp.MustCompile(pattern).FindStringSubmatch(block)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(strings.Trim(match[1], `" `))
}

func splitLeaseList(raw string) []string {
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(strings.Trim(part, `" `))
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func compareIPv4(a, b net.IP) int {
	for i := 0; i < net.IPv4len; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
