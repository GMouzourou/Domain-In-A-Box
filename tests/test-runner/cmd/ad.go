package cmd

import (
	"fmt"

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
	var passed, total int

	tests := []struct {
		name string
		fn   func() error
	}{
		{"SMB/CIFS service accessible (port 445)", func() error {
			ldapClient := network.NewLDAPClient(cfg.DomainControllerIP)
			return ldapClient.CheckConnectivity()
		}},
		{"LDAP service accessible (port 389)", func() error {
			ldapClient := network.NewLDAPClient(cfg.DomainControllerIP)
			return ldapClient.CheckConnectivity()
		}},
		{"Query domain object via LDAP", func() error {
			ldapClient := network.NewLDAPClient(cfg.DomainControllerIP).WithCredentials(
				fmt.Sprintf("%s@%s", cfg.AdminUser, cfg.DNSDomain), cfg.AdminPassword)
			_, err := ldapClient.SearchBase(ldapDN, "(objectClass=domain)")
			return err
		}},
		{"Query Administrator user exists", func() error {
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

	for _, test := range tests {
		total++
		helpers.LogTest(test.name)
		if err := test.fn(); err == nil {
			helpers.LogPass(test.name)
			passed++
		} else {
			helpers.LogFail(test.name)
			if cfg.Verbose {
				fmt.Printf("Error: %v\n", err)
			}
		}
	}

	fmt.Printf("\nAD Test Results: %d/%d passed\n", passed, total)

	if passed < total {
		return TestFailureError{Suite: "ad", Message: fmt.Sprintf("%d/%d passed", passed, total)}
	}

	return nil
}
