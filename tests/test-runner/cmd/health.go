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
	var passed, total int

	tests := []struct {
		name string
		fn   func() error
	}{
		{"DNS service (port 53)", func() error {
			return network.CheckPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "53", "5353"), 5*time.Second)
		}},
		{"LDAP service (port 389)", func() error {
			return network.CheckPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "389", "3389"), 5*time.Second)
		}},
		{"Kerberos service (port 88)", func() error {
			return network.CheckPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "88", "8888"), 5*time.Second)
		}},
		{"SMB service (port 445)", func() error {
			return network.CheckPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "445", "1445"), 5*time.Second)
		}},
		{"DHCP service (port 67)", func() error {
			return network.CheckUDPPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "67", "6767"), 5*time.Second)
		}},
	}

	for _, test := range tests {
		total++
		helpers.LogTest(test.name)
		if err := test.fn(); err == nil {
			helpers.LogPass(test.name)
			passed++
		} else {
			helpers.LogFail(test.name)
		}
	}

	fmt.Printf("\nHealth Check Results: %d/%d passed\n", passed, total)

	if passed < total {
		return TestFailureError{Suite: "health", Message: fmt.Sprintf("%d/%d services healthy", passed, total)}
	}

	return nil
}
