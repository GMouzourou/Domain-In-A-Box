package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var rootCmd = &cobra.Command{
	Use:           "test-runner",
	Short:         "Test runner for Domain-In-A-Box",
	Long:          "A CLI to run integration tests inside the Domain-In-A-Box test container.",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runAll,
}

// RegisterCommands registers all test commands
func RegisterCommands() {
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(healthCmd)
	rootCmd.AddCommand(dnsCmd)
	rootCmd.AddCommand(dhcpCmd)
	rootCmd.AddCommand(adCmd)
	rootCmd.AddCommand(adLinuxCmd)
}

// Execute runs the root command
func Execute() error {
	return rootCmd.Execute()
}

// InitFlags initializes global flags
func InitFlags() {
	rootCmd.PersistentFlags().StringP("domain-controller", "d", envOrDefault(defaultDomainControllerIP, "DOMAIN_CONTROLLER_IP", "TESTRUNNER_DOMAIN_CONTROLLER"), "Domain controller IP address")
	rootCmd.PersistentFlags().StringP("realm", "r", envOrDefault(defaultRealm, "DIB_REALM", "TESTRUNNER_REALM"), "Kerberos realm")
	rootCmd.PersistentFlags().String("dns-domain", envOrDefault(defaultDNSDomain, "DNS_DOMAIN", "TESTRUNNER_DNS_DOMAIN"), "DNS domain name")
	rootCmd.PersistentFlags().String("admin-user", envOrDefault(defaultAdminUser, "ADMIN_USER", "TESTRUNNER_ADMIN_USER"), "AD admin username")
	rootCmd.PersistentFlags().String("admin-password", envOrDefault(defaultAdminPassword, "ADMIN_PASSWORD", "TESTRUNNER_ADMIN_PASSWORD"), "AD admin password")
	rootCmd.PersistentFlags().String("server-hostname", envOrDefault(defaultHostname, "SERVER_HOSTNAME", "TESTRUNNER_SERVER_HOSTNAME"), "Server hostname")
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "Enable verbose output")

	viper.BindPFlag("domain-controller", rootCmd.PersistentFlags().Lookup("domain-controller"))
	viper.BindPFlag("realm", rootCmd.PersistentFlags().Lookup("realm"))
	viper.BindPFlag("dns-domain", rootCmd.PersistentFlags().Lookup("dns-domain"))
	viper.BindPFlag("admin-user", rootCmd.PersistentFlags().Lookup("admin-user"))
	viper.BindPFlag("admin-password", rootCmd.PersistentFlags().Lookup("admin-password"))
	viper.BindPFlag("server-hostname", rootCmd.PersistentFlags().Lookup("server-hostname"))
	viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose"))

	viper.SetEnvPrefix("TESTRUNNER")
	viper.AutomaticEnv()
}

// TestFailureError represents a test suite failure (not a command execution error)
type TestFailureError struct {
	Suite   string
	Message string
}

func (e TestFailureError) Error() string {
	return fmt.Sprintf("%s: %s", e.Suite, e.Message)
}

// IsTestFailure checks if an error is a test failure (not a real execution error)
func IsTestFailure(err error) bool {
	var testErr TestFailureError
	return errors.As(err, &testErr)
}

// runAll runs all test suites by default
func runAll(cmd *cobra.Command, args []string) error {
	return runAllTests()
}

// runAllTests orchestrates all test suites
func runAllTests() error {
	suites := []*cobra.Command{healthCmd, dhcpCmd, adCmd, adLinuxCmd, dnsCmd}
	fmt.Printf("\n%s=== Running All Tests ===%s\n", "\033[1;36m", "\033[0m")

	var failed []string

	for _, suite := range suites {
		fmt.Printf("\n%s=== Running %s ===%s\n", "\033[1;33m", suite.Use, "\033[0m")
		if err := suite.RunE(suite, nil); err != nil {
			if IsTestFailure(err) {
				// This is a test failure, not a command execution error
				failed = append(failed, suite.Use)
			} else {
				// This is a real execution error
				failed = append(failed, suite.Use)
				fmt.Printf("%s[ERROR]%s %s failed: %v\n", "\033[0;31m", "\033[0m", suite.Use, err)
			}
		}
	}

	fmt.Printf("\n%s=== Test Summary ===%s\n", "\033[1;36m", "\033[0m")
	passed := len(suites) - len(failed)
	fmt.Printf("Passed: %d/%d\n", passed, len(suites))

	if len(failed) > 0 {
		fmt.Printf("Failed: %v\n", failed)
		return TestFailureError{Suite: "all", Message: fmt.Sprintf("%d/%d suites passed", passed, len(suites))}
	}

	return nil
}

// runCmd is the "run" command that allows running specific or all tests
var runCmd = &cobra.Command{
	Use:   "run [suite]",
	Short: "Run one test suite or all suites",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return runAllTests()
		}

		suite := args[0]
		switch suite {
		case "all":
			return runAllTests()
		case "health":
			return healthCmd.RunE(healthCmd, nil)
		case "dns":
			return dnsCmd.RunE(dnsCmd, nil)
		case "dhcp":
			return dhcpCmd.RunE(dhcpCmd, nil)
		case "ad":
			return adCmd.RunE(adCmd, nil)
		case "ad-linux":
			return adLinuxCmd.RunE(adLinuxCmd, nil)
		default:
			return fmt.Errorf("unknown suite: %s (valid: all, health, dns, dhcp, ad, ad-linux)", suite)
		}
	},
}

func init() {
	cobra.OnInitialize(func() {
		viper.ReadInConfig()
	})
}
