package main

import (
	"fmt"
	"os"

	platformcli "github.com/GMouzourou/domain-in-a-box/internal/platform/cli"
	"github.com/GMouzourou/domain-in-a-box/internal/services/networkcore"
)

func main() {
	runner := networkcore.NewRunner()
	cmd, _, err := platformcli.NewRootCommand(
		"dib-network-core-ctl",
		"Domain-In-A-Box network core orchestrator",
		"DIB_NETWORK_CORE",
		runner,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
