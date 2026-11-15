package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	platformcli "github.com/GMouzourou/domain-in-a-box/internal/platform/cli"
	"github.com/GMouzourou/domain-in-a-box/internal/services/dnsbridge"
	"github.com/spf13/cobra"
)

func main() {
	runner := dnsbridge.NewRunner()
	command, _, err := platformcli.NewRootCommand(
		"dib-external-dns-bridge", "Domain-In-A-Box pod-local ExternalDNS webhook",
		"DIB_DNS_BRIDGE", runner,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	command.PersistentFlags().StringVar(&runner.ConfigPath, "bridge-config", runner.ConfigPath, "Path to bridge JSON configuration")
	command.PersistentFlags().BoolVar(&runner.Liveness, "liveness", false, "Check only the local listener, not backend readiness")
	command.AddCommand(&cobra.Command{
		Use: "provision", Short: "Reconcile a dedicated DNS identity inside a domain controller",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runner.Provision(cmd.Context()) },
	})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := command.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
