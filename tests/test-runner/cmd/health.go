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
		{Name: "DNS service (port 53)", Run: func() error {
			return network.CheckPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "53", "5353"), 5*time.Second)
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
		{Name: "DHCP service (port 67)", Run: func() error {
			return network.CheckUDPPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "67", "6767"), 5*time.Second)
		}},
	}

	passed, total := helpers.RunChecks("Health Check Results", checks, cfg.Verbose)
	if passed < total {
		return TestFailureError{Suite: "health", Message: fmt.Sprintf("%d/%d services healthy", passed, total)}
	}

	return nil
}
