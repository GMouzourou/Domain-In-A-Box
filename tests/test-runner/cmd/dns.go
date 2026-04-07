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
	var passed, total int

	tests := []struct {
		name string
		fn   func() error
	}{
		{fmt.Sprintf("Resolve domain controller hostname (%s.%s)", cfg.ServerHostname, cfg.DNSDomain), func() error {
			_, err := dnsClient.LookupA(fmt.Sprintf("%s.%s", cfg.ServerHostname, cfg.DNSDomain))
			return err
		}},
		{fmt.Sprintf("Resolve Kerberos SRV record (_kerberos._tcp.%s)", cfg.DNSDomain), func() error {
			_, err := dnsClient.LookupSRV("kerberos", "tcp", cfg.DNSDomain)
			return err
		}},
		{fmt.Sprintf("Resolve LDAP SRV record (_ldap._tcp.%s)", cfg.DNSDomain), func() error {
			_, err := dnsClient.LookupSRV("ldap", "tcp", cfg.DNSDomain)
			return err
		}},
		{fmt.Sprintf("Resolve DC SRV record (_ldap._tcp.dc._msdcs.%s)", cfg.DNSDomain), func() error {
			_, err := dnsClient.LookupSRV("ldap", "tcp", "dc._msdcs."+cfg.DNSDomain)
			return err
		}},
		{"Query A record for domain controller", func() error {
			_, err := dnsClient.LookupA(fmt.Sprintf("%s.%s", cfg.ServerHostname, cfg.DNSDomain))
			return err
		}},
		{"Query reverse PTR record", func() error {
			resolvedIP, err := dnsClient.LookupA(fmt.Sprintf("%s.%s", cfg.ServerHostname, cfg.DNSDomain))
			if err != nil {
				return err
			}
			_, err = dnsClient.LookupPTR(resolvedIP)
			return err
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

	fmt.Printf("\nDNS Test Results: %d/%d passed\n", passed, total)

	if passed < total {
		return TestFailureError{Suite: "dns", Message: fmt.Sprintf("%d/%d passed", passed, total)}
	}

	return nil
}
