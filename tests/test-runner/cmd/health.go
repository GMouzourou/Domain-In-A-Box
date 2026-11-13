package cmd

import (
	"fmt"
	"time"

	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/internal/helpers"
	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/internal/network"
	"github.com/spf13/cobra"
)

var healthCmd = &cobra.Command{
	Use:   "health",
	Short: "Run health checks on services",
	RunE:  runHealth,
}

func runHealth(cmd *cobra.Command, args []string) error {
	cfg := getTestConfig(cmd)

	helpers.LogInfo("Starting health checks...")
	checks := []helpers.Check{
		{Name: fmt.Sprintf("DNS query for %s.%s", cfg.ServerHostname, cfg.DNSDomain), Run: func() error {
			return checkDomainControllerDNS(cfg)
		}},
		{Name: "LDAP service (port 389)", Run: func() error {
			return network.CheckPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "389", "3389"), 5*time.Second)
		}},
		{Name: "Kerberos service (port 88)", Run: func() error {
			return network.CheckPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "88", "8888"), 5*time.Second)
		}},
		{Name: "SMB service (port 445)", Run: func() error {
			return network.CheckPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "445", "1445"), 5*time.Second)
		}},
		{Name: fmt.Sprintf("Stork REST service (port %s)", cfg.StorkServerPort), Run: func() error {
			return network.CheckPort(cfg.StorkServerIP, cfg.StorkServerPort, 5*time.Second)
		}},
		{Name: "Samba Prometheus endpoint (port 9922)", Run: func() error {
			return network.CheckPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "9922", "9922"), 5*time.Second)
		}},
		{Name: "Bind metrics endpoint (port 9119)", Run: func() error {
			return network.CheckPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "9119", "9119"), 5*time.Second)
		}},
		{Name: "Kea metrics endpoint (port 9547)", Run: func() error {
			return network.CheckPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "9547", "9547"), 5*time.Second)
		}},
	}
	if cfg.DHCPRequireLease {
		checks = append(checks, helpers.Check{
			Name: fmt.Sprintf("DHCP lease acquired on %s from pool %s", cfg.DHCPTestInterface, cfg.DHCPPool),
			Run: func() error {
				return checkDHCPLease(cfg)
			},
		})
	}

	passed, total := helpers.RunChecks("Health Check Results", checks, cfg.Verbose)
	if passed < total {
		return TestFailureError{Suite: "health", Message: fmt.Sprintf("%d/%d services healthy", passed, total)}
	}

	return nil
}
