package main

import (
	"fmt"
	"os"

	platformcli "github.com/GMouzourou/domain-in-a-box/internal/platform/cli"
	"github.com/GMouzourou/domain-in-a-box/internal/services/observability"
)

func main() {
	runner := observability.NewRunner()
	cmd, _, err := platformcli.NewRootCommand(
		"dib-observability-ctl",
		"Domain-In-A-Box observability orchestrator",
		"DIB_OBSERVABILITY",
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
