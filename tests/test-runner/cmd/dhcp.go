package cmd

import (
	"bytes"
	"fmt"
	"net"
	"strings"

	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/internal/helpers"
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
	return leaseWithinPool(cfg.DHCPTestInterface, cfg.DHCPPool)
}

// leaseWithinPool verifies the interface holds an address from "start-end".
func leaseWithinPool(interfaceName, pool string) error {
	if interfaceName == "" {
		return fmt.Errorf("no DHCP test interface was configured")
	}

	start, end, found := strings.Cut(pool, "-")
	first := net.ParseIP(strings.TrimSpace(start)).To4()
	last := net.ParseIP(strings.TrimSpace(end)).To4()
	if !found || first == nil || last == nil {
		return fmt.Errorf("invalid DHCP pool %q", pool)
	}

	iface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return fmt.Errorf("look up %s: %w", interfaceName, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return fmt.Errorf("read addresses of %s: %w", interfaceName, err)
	}

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		if ip := ipNet.IP.To4(); ip != nil &&
			bytes.Compare(ip, first) >= 0 && bytes.Compare(ip, last) <= 0 {
			return nil
		}
	}

	return fmt.Errorf("%s holds no address within %s (has %v)", interfaceName, pool, addrs)
}
