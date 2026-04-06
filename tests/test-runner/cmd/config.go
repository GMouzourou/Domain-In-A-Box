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
	verbose, _ := cmd.Flags().GetBool("verbose")
	return testConfig{
		DomainControllerIP: cmd.Flag("domain-controller").Value.String(),
		Realm:              cmd.Flag("realm").Value.String(),
		DNSDomain:          cmd.Flag("dns-domain").Value.String(),
		AdminUser:          cmd.Flag("admin-user").Value.String(),
		AdminPassword:      cmd.Flag("admin-password").Value.String(),
		ServerHostname:     cmd.Flag("server-hostname").Value.String(),
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
