package main

import (
	"fmt"
	"os"

	"github.com/GMouzourou/domain-in-a-box/tests/test-runner/cmd"
)

func main() {
	cmd.InitFlags()
	cmd.RegisterCommands()

	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
