package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	platformcli "github.com/GMouzourou/domain-in-a-box/internal/platform/cli"
	"github.com/GMouzourou/domain-in-a-box/internal/services/identitycore"
	"github.com/spf13/cobra"
)

func main() {
	runner := identitycore.NewRunner()
	cmd, _, err := platformcli.NewRootCommand(
		"dib-identity-core-ctl",
		"Domain-In-A-Box identity core orchestrator",
		"DIB_IDENTITY_CORE",
		runner,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "ldaps-relay",
		Short: "Relay internal LDAPS to Samba without terminating TLS",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
			defer stop()
			return runner.RunLDAPSRelay(ctx)
		},
	})
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
