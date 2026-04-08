package cmd

import (
	"fmt"

	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/internal/helpers"
	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/internal/network"
	"github.com/spf13/cobra"
)

var dnsCmd = &cobra.Command{
	Use:   "dns",
	Short: "Run DNS resolution tests",
	RunE:  runDNS,
}

func runDNS(cmd *cobra.Command, args []string) error {
	cfg := getTestConfig(cmd)

	helpers.LogInfo("Starting DNS Resolution Tests...")
	helpers.LogInfo(fmt.Sprintf("DNS Server: %s", cfg.DomainControllerIP))
	helpers.LogInfo(fmt.Sprintf("Domain: %s", cfg.DNSDomain))

	dnsClient := network.NewDNSClient(cfg.DomainControllerIP)
	checks := []helpers.Check{
		{Name: fmt.Sprintf("Resolve domain controller hostname (%s.%s) to %s", cfg.ServerHostname, cfg.DNSDomain, cfg.DomainControllerIP), Run: func() error {
			resolvedIP, err := dnsClient.LookupA(fmt.Sprintf("%s.%s", cfg.ServerHostname, cfg.DNSDomain))
			if err != nil {
				return err
			}
			if resolvedIP != cfg.DomainControllerIP {
				return fmt.Errorf("resolved to %s, want %s", resolvedIP, cfg.DomainControllerIP)
			}
			return nil
		}},
		{Name: fmt.Sprintf("Resolve Kerberos SRV record (_kerberos._tcp.%s)", cfg.DNSDomain), Run: func() error {
			_, err := dnsClient.LookupSRV("kerberos", "tcp", cfg.DNSDomain)
			return err
		}},
		{Name: fmt.Sprintf("Resolve LDAP SRV record (_ldap._tcp.%s)", cfg.DNSDomain), Run: func() error {
			_, err := dnsClient.LookupSRV("ldap", "tcp", cfg.DNSDomain)
			return err
		}},
		{Name: fmt.Sprintf("Resolve DC SRV record (_ldap._tcp.dc._msdcs.%s)", cfg.DNSDomain), Run: func() error {
			_, err := dnsClient.LookupSRV("ldap", "tcp", "dc._msdcs."+cfg.DNSDomain)
			return err
		}},
		{Name: "Query reverse PTR record for the configured controller IP", Run: func() error {
			_, err := dnsClient.LookupPTR(cfg.DomainControllerIP)
			return err
		}},
	}

	passed, total := helpers.RunChecks("DNS Test Results", checks, cfg.Verbose)
	if passed < total {
		return TestFailureError{Suite: "dns", Message: fmt.Sprintf("%d/%d passed", passed, total)}
	}

	return nil
}
