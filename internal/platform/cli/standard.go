package cli

import (
	"context"
	"fmt"

	platformcfg "github.com/GMouzourou/domain-in-a-box/internal/platform/config"
	platformlog "github.com/GMouzourou/domain-in-a-box/internal/platform/log"
	"github.com/spf13/cobra"
)

type Runner interface {
	Name() string
	Configure(context.Context) error
	Bootstrap(context.Context) error
	Validate(context.Context) error
	Run(context.Context) error
	Health(context.Context) error
}

func NewRootCommand(use string, short string, envPrefix string, runner Runner) (*cobra.Command, *platformcfg.Manager, error) {
	cfg := platformcfg.NewManager(envPrefix)
	logger := platformlog.New(runner.Name())

	cmd := &cobra.Command{
		Use:           use,
		Short:         short,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			runtimeCfg, err := cfg.Load()
			if err != nil {
				return err
			}
			if runtimeCfg.Verbose {
				logger.Infof("profile=%s verbose=true", runtimeCfg.Profile)
			}
			return nil
		},
	}

	if err := cfg.BindRootFlags(cmd); err != nil {
		return nil, nil, fmt.Errorf("bind root flags: %w", err)
	}

	cmd.AddCommand(newActionCommand("configure", "Generate and reconcile local configuration", runner.Configure))
	cmd.AddCommand(newActionCommand("bootstrap", "Perform one-time bootstrap operations", runner.Bootstrap))
	cmd.AddCommand(newActionCommand("validate", "Validate configuration and dependencies", runner.Validate))
	cmd.AddCommand(newActionCommand("run", "Run long-lived service orchestration loop", runner.Run))
	cmd.AddCommand(newActionCommand("health", "Run health checks for service dependencies", runner.Health))
	cmd.AddCommand(newVersionCommand(use))

	return cmd, cfg, nil
}

func newActionCommand(use string, short string, action func(context.Context) error) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return action(cmd.Context())
		},
	}
}

func newVersionCommand(binaryName string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print binary version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("%s version: dev\n", binaryName)
		},
	}
}
