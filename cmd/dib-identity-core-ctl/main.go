package main

import (
	"fmt"
	"os"

	platformcli "github.com/GMouzourou/domain-in-a-box/internal/platform/cli"
	"github.com/GMouzourou/domain-in-a-box/internal/services/identitycore"
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

	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
