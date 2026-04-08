package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/internal/helpers"
	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/internal/network"
	"github.com/spf13/cobra"
)

var adCmd = &cobra.Command{
	Use:   "ad",
	Short: "Run Active Directory connectivity tests",
	RunE:  runAD,
}

func runAD(cmd *cobra.Command, args []string) error {
	cfg := getTestConfig(cmd)

	helpers.LogInfo("Starting Active Directory Integration Tests...")
	helpers.LogInfo(fmt.Sprintf("Domain Controller: %s", cfg.DomainControllerIP))
	helpers.LogInfo(fmt.Sprintf("Domain: %s", cfg.DNSDomain))

	ldapDN := helpers.ConvertDomainToLDAP(cfg.DNSDomain)
	smbPort := servicePort(cfg.DomainControllerIP, "445", "1445")
	workgroup := strings.Split(cfg.Realm, ".")[0]
	checks := []helpers.Check{
		{Name: "SMB/CIFS service reachable (port 445)", Run: func() error {
			return network.CheckPort(cfg.DomainControllerIP, smbPort, 5*time.Second)
		}},
		{Name: "List SMB shares as Administrator", Run: func() error {
			shares, err := network.ListSMBShares(cfg.DomainControllerIP, smbPort, cfg.AdminUser, cfg.AdminPassword, workgroup)
			if err != nil {
				return err
			}
			normalizedShares := strings.ToLower(shares)
			if !strings.Contains(normalizedShares, "disk|sysvol|") || !strings.Contains(normalizedShares, "disk|netlogon|") {
				return fmt.Errorf("expected SYSVOL and NETLOGON shares, got:\n%s", shares)
			}
			return nil
		}},
		{Name: "Read SYSVOL over SMB", Run: func() error {
			_, err := network.ReadSMBShare(cfg.DomainControllerIP, smbPort, "SYSVOL", cfg.AdminUser, cfg.AdminPassword, workgroup)
			return err
		}},
		{Name: "LDAP service accessible (port 389)", Run: func() error {
			ldapClient := network.NewLDAPClient(cfg.DomainControllerIP)
			return ldapClient.CheckConnectivity()
		}},
		{Name: "Query domain object via LDAP", Run: func() error {
			ldapClient := network.NewLDAPClient(cfg.DomainControllerIP).WithCredentials(
				fmt.Sprintf("%s@%s", cfg.AdminUser, cfg.DNSDomain), cfg.AdminPassword)
			_, err := ldapClient.SearchBase(ldapDN, "(objectClass=domain)")
			return err
		}},
		{Name: "Query Administrator user exists", Run: func() error {
			ldapClient := network.NewLDAPClient(cfg.DomainControllerIP).WithCredentials(
				fmt.Sprintf("%s@%s", cfg.AdminUser, cfg.DNSDomain), cfg.AdminPassword)
			searchBase := fmt.Sprintf("cn=Users,%s", ldapDN)
			entries, err := ldapClient.SearchSubtree(searchBase, "(cn=Administrator)")
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				return fmt.Errorf("Administrator user not found")
			}
			return nil
		}},
	}

	passed, total := helpers.RunChecks("AD Test Results", checks, cfg.Verbose)
	if passed < total {
		return TestFailureError{Suite: "ad", Message: fmt.Sprintf("%d/%d passed", passed, total)}
	}

	return nil
}
