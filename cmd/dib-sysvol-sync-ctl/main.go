package main

import (
	"fmt"
	"os"

	platformcli "github.com/GMouzourou/domain-in-a-box/internal/platform/cli"
	"github.com/GMouzourou/domain-in-a-box/internal/services/sysvolsync"
	"github.com/spf13/cobra"
)

func main() {
	runner := sysvolsync.NewRunner()
	cmd, _, err := platformcli.NewRootCommand(
		"dib-sysvol-sync-ctl",
		"Domain-In-A-Box SYSVOL synchronization",
		"DIB_SYSVOL_SYNC",
		runner,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "sshd",
		Short: "Run the restricted SYSVOL export SSH server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.RunSSHServer(cmd.Context())
		},
	})
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
