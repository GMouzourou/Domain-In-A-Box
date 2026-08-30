package main

import (
	"fmt"
	"os"

	platformcli "github.com/GMouzourou/domain-in-a-box/internal/platform/cli"
	"github.com/GMouzourou/domain-in-a-box/internal/services/db"
)

func main() {
	runner := db.NewRunner()
	cmd, _, err := platformcli.NewRootCommand(
		"dib-db-ctl",
		"Domain-In-A-Box database orchestrator",
		"DIB_DB",
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
