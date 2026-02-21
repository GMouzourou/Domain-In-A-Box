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
	Short: "Run DHCP functionality tests",
	RunE:  runDHCP,
}

func runDHCP(cmd *cobra.Command, args []string) error {
	cfg := getTestConfig(cmd)

	helpers.LogInfo("Starting DHCP Functionality Tests...")
	helpers.LogInfo(fmt.Sprintf("DHCP Server: %s", cfg.DomainControllerIP))

	var passed, total int

	tests := []struct {
		name string
		fn   func() error
	}{
		{"DHCP server listening on port 67", func() error {
			return network.CheckUDPPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "67", "6767"), 5*time.Second)
		}},
		{"DNS accessible (DDNS capability)", func() error {
			return network.CheckPort(cfg.DomainControllerIP, servicePort(cfg.DomainControllerIP, "53", "5353"), 5*time.Second)
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

	fmt.Printf("\nDHCP Test Results: %d/%d passed\n", passed, total)

	if passed < total {
		return TestFailureError{Suite: "dhcp", Message: fmt.Sprintf("%d/%d passed", passed, total)}
	}

	return nil
}
