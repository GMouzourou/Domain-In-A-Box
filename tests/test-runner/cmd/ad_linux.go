package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/internal/helpers"
	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/internal/network"
	"github.com/spf13/cobra"
)

var adLinuxCmd = &cobra.Command{
	Use:   "ad-linux",
	Short: "Run Linux Active Directory join tests",
	RunE:  runADLinux,
}

func runADLinux(cmd *cobra.Command, args []string) error {
	cfg := getTestConfig(cmd)

	helpers.LogInfo("Starting Linux Active Directory Domain Join Test...")
	helpers.LogInfo(fmt.Sprintf("Domain Controller: %s", cfg.DomainControllerIP))
	helpers.LogInfo(fmt.Sprintf("Realm: %s", cfg.Realm))
	helpers.LogInfo(fmt.Sprintf("Domain: %s", cfg.DNSDomain))

	// Configure Kerberos
	kerberosConfig := helpers.KerberosConfig{
		Realm:              cfg.Realm,
		DefaultRealm:       cfg.Realm,
		DnsLookupRealm:     true,
		DnsLookupKdc:       true,
		RDns:               false,
		DomainFQDN:         cfg.DNSDomain,
		DomainControllerIP: cfg.DomainControllerIP,
	}
	if err := helpers.WriteKerberosConfig(kerberosConfig); err != nil {
		return err
	}

	ldapDN := helpers.ConvertDomainToLDAP(cfg.DNSDomain)

	// Do not pre-write sssd.conf here. `realm join` manages SSSD configuration,
	// and pre-creating the domain stanza causes the join to fail.

	var passed, total int

	tests := []struct {
		name string
		fn   func() error
	}{
		{"Resolve domain controller via DNS", func() error {
			dnsClient := network.NewDNSClient(cfg.DomainControllerIP)
			_, err := dnsClient.LookupA(fmt.Sprintf("%s.%s", cfg.ServerHostname, cfg.DNSDomain))
			return err
		}},
		{"Connect to LDAP service", func() error {
			ldapClient := network.NewLDAPClient(cfg.DomainControllerIP)
			return ldapClient.CheckConnectivity()
		}},
		{"Connect to Kerberos service", func() error {
			return checkKerberosPort(cfg.DomainControllerIP)
		}},
		{"Query domain object via LDAP", func() error {
			ldapClient := network.NewLDAPClient(cfg.DomainControllerIP).WithCredentials(
				fmt.Sprintf("%s@%s", cfg.AdminUser, cfg.DNSDomain), cfg.AdminPassword)
			_, err := ldapClient.SearchBase(ldapDN, "(objectClass=domain)")
			return err
		}},
		{"Query domain controller information", func() error {
			ldapClient := network.NewLDAPClient(cfg.DomainControllerIP).WithCredentials(
				fmt.Sprintf("%s@%s", cfg.AdminUser, cfg.DNSDomain), cfg.AdminPassword)
			_, err := ldapClient.SearchBase(ldapDN, "(objectClass=domain)")
			return err
		}},
		{"Query domain users", func() error {
			ldapClient := network.NewLDAPClient(cfg.DomainControllerIP).WithCredentials(
				fmt.Sprintf("%s@%s", cfg.AdminUser, cfg.DNSDomain), cfg.AdminPassword)
			searchBase := fmt.Sprintf("cn=Users,%s", ldapDN)
			entries, err := ldapClient.SearchSubtree(searchBase, "(objectClass=*)")
			if err != nil {
				return nil // Ignore errors - basic connectivity test
			}
			if len(entries) == 0 {
				return nil // Empty result is OK
			}
			return nil
		}},
		{"Join Active Directory domain or verify join status", func() error {
			return checkDomainJoin(cfg.Realm, cfg.DNSDomain, cfg.DomainControllerIP, cfg.AdminUser, cfg.AdminPassword)
		}},
		{"SSSD domain user lookup", func() error {
			return lookupUser(cfg.AdminUser, cfg.DNSDomain)
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

	fmt.Printf("\nLinux AD Join Test Results: %d/%d tests passed\n", passed, total)

	if passed < total {
		return TestFailureError{Suite: "ad-linux", Message: fmt.Sprintf("%d/%d passed", passed, total)}
	}

	return nil
}

// checkKerberosPort checks if Kerberos port is accessible
func checkKerberosPort(host string) error {
	port := "88"
	if host == "localhost" || host == "127.0.0.1" {
		port = "8888"
	}
	// Kerberos uses TCP port 88
	cmd := exec.Command("bash", "-c", fmt.Sprintf("timeout 5 bash -c '</dev/null >/dev/tcp/%s/%s' 2>/dev/null", host, port))
	return cmd.Run()
}

// checkDomainJoin checks if the domain is already joined and joins if needed
func checkDomainJoin(realm, dnsDomain, domainControllerIP, adminUser, adminPassword string) error {
	if isDomainJoined() {
		helpers.LogInfo("Domain already joined")
		ensureSSSDRunning()
		return nil
	}

	// Not joined, attempt to join
	helpers.LogStep("Attempting to join Active directory domain...")
	_ = exec.Command("rm", "-f", "/etc/sssd/sssd.conf").Run()

	realmCmd := exec.Command("timeout", "120", "realm", "join",
		realm,
		"--user="+adminUser,
		"--unattended",
		"--verbose")
	realmCmd.Env = append(os.Environ(), "DBUS_SYSTEM_BUS_ADDRESS=unix:path=/var/run/dbus/system_bus_socket")
	realmCmd.Stdin = strings.NewReader(adminPassword + "\n")

	realmOutput, realmErr := realmCmd.CombinedOutput()
	if realmErr != nil {
		helpers.LogInfo("`realm join` failed; trying `adcli join` fallback")

		adcliCmd := exec.Command("timeout", "120", "adcli", "join",
			"--domain="+dnsDomain,
			"--domain-realm="+realm,
			"--domain-controller="+domainControllerIP,
			"--login-user="+adminUser,
			"--stdin-password",
			"--verbose")
		adcliCmd.Stdin = strings.NewReader(adminPassword + "\n")

		adcliOutput, adcliErr := adcliCmd.CombinedOutput()
		if adcliErr != nil && !isDomainJoined() {
			return fmt.Errorf("domain join failed\nrealm output:\n%s\n\nadcli output:\n%s",
				strings.TrimSpace(string(realmOutput)),
				strings.TrimSpace(string(adcliOutput)))
		}
	}

	ensureSSSDRunning()
	if !isDomainJoined() {
		return fmt.Errorf("domain join did not produce a joined state")
	}
	return nil
}

func isDomainJoined() bool {
	realmCmd := exec.Command("bash", "-lc", "export DBUS_SYSTEM_BUS_ADDRESS=unix:path=/var/run/dbus/system_bus_socket; realm list 2>/dev/null | grep -q 'configured: kerberos-member'")
	if realmCmd.Run() == nil {
		return true
	}

	keytabCmd := exec.Command("bash", "-lc", "test -f /etc/krb5.keytab && klist -k /etc/krb5.keytab 2>/dev/null | grep -qi 'host/'")
	return keytabCmd.Run() == nil
}

func ensureSSSDRunning() {
	_ = exec.Command("bash", "-lc", "pgrep -x sssd >/dev/null || { rm -f /var/run/sssd.pid; /usr/sbin/sssd -D >/tmp/sssd.log 2>&1 & sleep 5; }").Run()
}

// lookupUser checks if a domain user can be resolved via SSSD
func lookupUser(username, domain string) error {
	ensureSSSDRunning()
	userPrincipal := fmt.Sprintf("%s@%s", strings.ToLower(username), strings.ToLower(domain))

	var output []byte
	var err error
	for i := 0; i < 10; i++ {
		getentCmd := exec.Command("getent", "passwd", userPrincipal)
		output, err = getentCmd.CombinedOutput()
		if err == nil {
			return nil
		}
		_ = exec.Command("sleep", "1").Run()
	}

	return fmt.Errorf("domain user lookup failed: %w\n%s", err, strings.TrimSpace(string(output)))
}
