package cmd

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/internal/helpers"
	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/internal/network"
	"github.com/spf13/cobra"
)

var dhcpCmd = &cobra.Command{
	Use:   "dhcp",
	Short: "Validate DNS and DHCP lease readiness",
	RunE:  runDHCP,
}

func runDHCP(cmd *cobra.Command, args []string) error {
	cfg := getTestConfig(cmd)

	helpers.LogInfo("Starting DHCP and DNS checks...")
	helpers.LogInfo(fmt.Sprintf("DHCP Server: %s", cfg.DomainControllerIP))

	checks := []helpers.Check{
		{Name: fmt.Sprintf("Resolve domain controller hostname (%s.%s)", cfg.ServerHostname, cfg.DNSDomain), Run: func() error {
			return checkDomainControllerDNS(cfg)
		}},
	}

	// Only meaningful on a segment that carries broadcast traffic, so the
	// compose suite leaves this off and the macvlan suite turns it on.
	if cfg.DHCPRequireLease {
		checks = append(checks, helpers.Check{
			Name: fmt.Sprintf("DHCP lease acquired on %s from pool %s", cfg.DHCPTestInterface, cfg.DHCPPool),
			Run: func() error {
				return checkDHCPLease(cfg)
			},
		}, helpers.Check{
			Name: "Leased address and host name registered in DNS (A and PTR)",
			Run: func() error {
				return checkDHCPDNSRegistration(cfg, 90*time.Second)
			},
		})
	}

	passed, total := helpers.RunChecks("DHCP Test Results", checks, cfg.Verbose)
	if passed < total {
		return TestFailureError{Suite: "dhcp", Message: fmt.Sprintf("%d/%d passed", passed, total)}
	}

	return nil
}

func checkDHCPLease(cfg testConfig) error {
	if !cfg.DHCPLeaseSuccess {
		return fmt.Errorf("no lease was acquired on %q", cfg.DHCPTestInterface)
	}
	_, err := leaseWithinPool(cfg.DHCPTestInterface, cfg.DHCPPool)
	return err
}

// checkDHCPDNSRegistration waits for the controller's dynamic DNS update for
// this client's lease, which happens asynchronously after the lease is granted.
func checkDHCPDNSRegistration(cfg testConfig, timeout time.Duration) error {
	if !cfg.DHCPLeaseSuccess {
		return fmt.Errorf("no lease was acquired on %q", cfg.DHCPTestInterface)
	}
	address, err := leaseWithinPool(cfg.DHCPTestInterface, cfg.DHCPPool)
	if err != nil {
		return err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}
	label, _, _ := strings.Cut(strings.ToLower(hostname), ".")
	fqdn := label + "." + strings.ToLower(cfg.DNSDomain)
	dnsClient := network.NewDNSClient(cfg.DomainControllerIP)

	deadline := time.Now().Add(timeout)
	for {
		err = dnsRegistrationMatches(dnsClient, fqdn, address.String())
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(3 * time.Second)
	}
}

func dnsRegistrationMatches(dnsClient *network.DNSClient, fqdn, address string) error {
	resolved, err := dnsClient.LookupA(fqdn)
	if err != nil {
		return err
	}
	if resolved != address {
		return fmt.Errorf("%s resolves to %s, want leased address %s", fqdn, resolved, address)
	}
	ptr, err := dnsClient.LookupPTR(address)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSuffix(ptr, "."), fqdn) {
		return fmt.Errorf("PTR for %s is %s, want %s", address, ptr, fqdn)
	}
	return nil
}

// leaseWithinPool verifies the interface holds an address from "start-end".
func leaseWithinPool(interfaceName, pool string) (net.IP, error) {
	if interfaceName == "" {
		return nil, fmt.Errorf("no DHCP test interface was configured")
	}

	start, end, found := strings.Cut(pool, "-")
	first := net.ParseIP(strings.TrimSpace(start)).To4()
	last := net.ParseIP(strings.TrimSpace(end)).To4()
	if !found || first == nil || last == nil {
		return nil, fmt.Errorf("invalid DHCP pool %q", pool)
	}

	iface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return nil, fmt.Errorf("look up %s: %w", interfaceName, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("read addresses of %s: %w", interfaceName, err)
	}

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		if ip := ipNet.IP.To4(); ip != nil &&
			bytes.Compare(ip, first) >= 0 && bytes.Compare(ip, last) <= 0 {
			return ip, nil
		}
	}

	return nil, fmt.Errorf("%s holds no address within %s (has %v)", interfaceName, pool, addrs)
}
