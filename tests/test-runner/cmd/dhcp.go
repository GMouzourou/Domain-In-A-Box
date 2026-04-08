package cmd

import (
	"fmt"
	"time"

	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/internal/helpers"
	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/internal/network"
	"github.com/spf13/cobra"
)

var dhcpCmd = &cobra.Command{
	Use:   "dhcp",
	Short: "Run DHCP reachability and DDNS readiness checks",
	RunE:  runDHCP,
}

func runDHCP(cmd *cobra.Command, args []string) error {
	cfg := getTestConfig(cmd)

	helpers.LogInfo("Starting DHCP reachability checks...")
	helpers.LogInfo(fmt.Sprintf("DHCP Server: %s", cfg.DomainControllerIP))

	checks := []helpers.Check{
		{Name: "DHCP server reachable on UDP port 67", Run: func() error {
			return network.CheckUDPPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "67", "6767"), 5*time.Second)
		}},
		{Name: "DNS service reachable for DDNS integration", Run: func() error {
			return network.CheckPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "53", "5353"), 5*time.Second)
		}},
	}

	passed, total := helpers.RunChecks("DHCP Test Results", checks, cfg.Verbose)
	if passed < total {
		return TestFailureError{Suite: "dhcp", Message: fmt.Sprintf("%d/%d passed", passed, total)}
	}

	return nil
}
