package cmd

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
)

const (
	defaultDomainControllerIP = "192.168.1.1"
	defaultRealm              = "HOME.ARPA"
	defaultDNSDomain          = "home.arpa"
	defaultAdminUser          = "Administrator"
	defaultAdminPassword      = "P@ssw0rd"
	defaultHostname           = "domain-server"
)

type testConfig struct {
	DomainControllerIP string
	Realm              string
	DNSDomain          string
	AdminUser          string
	AdminPassword      string
	ServerHostname     string
	DHCPPool           string
	Gateway            string
	DHCPTestInterface  string
	DHCPLeaseFile      string
	DHCPLeaseSuccess   bool
	Verbose            bool
}

func envOrDefault(fallback string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return fallback
}

func getTestConfig(cmd *cobra.Command) testConfig {
	verbose := false
	if flag := cmd.Flag("verbose"); flag != nil {
		verbose = strings.EqualFold(flag.Value.String(), "true")
	}
	return testConfig{
		DomainControllerIP: cmd.Flag("domain-controller").Value.String(),
		Realm:              cmd.Flag("realm").Value.String(),
		DNSDomain:          cmd.Flag("dns-domain").Value.String(),
		AdminUser:          cmd.Flag("admin-user").Value.String(),
		AdminPassword:      cmd.Flag("admin-password").Value.String(),
		ServerHostname:     cmd.Flag("server-hostname").Value.String(),
		DHCPPool:           envOrDefault("", "DIB_DHCP_POOL", "TESTRUNNER_DHCP_POOL"),
		Gateway:            envOrDefault("", "GATEWAY", "TESTRUNNER_GATEWAY"),
		DHCPTestInterface:  envOrDefault("", "DIB_DHCP_TEST_INTERFACE", "TESTRUNNER_DHCP_INTERFACE"),
		DHCPLeaseFile:      envOrDefault("/var/lib/dhcp/dhclient.leases", "DIB_DHCP_LEASE_FILE", "TESTRUNNER_DHCP_LEASE_FILE"),
		DHCPLeaseSuccess:   strings.EqualFold(envOrDefault("false", "DIB_DHCP_LEASE_SUCCESS", "TESTRUNNER_DHCP_LEASE_SUCCESS"), "true"),
		Verbose:            verbose,
	}
}

func isLocalTarget(host string) bool {
	return host == "localhost" || host == "127.0.0.1"
}

func servicePort(host, defaultPort, mappedPort string) string {
	if isLocalTarget(host) {
		return mappedPort
	}
	return defaultPort
}
