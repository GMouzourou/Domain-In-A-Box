package config

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type Manager struct {
	V         *viper.Viper
	envPrefix string
}

type RuntimeConfig struct {
	Profile string
	Verbose bool
}

func NewManager(envPrefix string) *Manager {
	v := viper.New()
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	return &Manager{V: v, envPrefix: envPrefix}
}

func (m *Manager) BindRootFlags(cmd *cobra.Command) error {
	cmd.PersistentFlags().String("config", "", "Path to config file")
	cmd.PersistentFlags().String("profile", "default", "Configuration profile")
	cmd.PersistentFlags().BoolP("verbose", "v", false, "Enable verbose output")

	if err := m.V.BindPFlag("runtime.config", cmd.PersistentFlags().Lookup("config")); err != nil {
		return err
	}
	if err := m.V.BindPFlag("runtime.profile", cmd.PersistentFlags().Lookup("profile")); err != nil {
		return err
	}
	if err := m.V.BindPFlag("runtime.verbose", cmd.PersistentFlags().Lookup("verbose")); err != nil {
		return err
	}

	return nil
}

func (m *Manager) Load() (RuntimeConfig, error) {
	configPath := m.V.GetString("runtime.config")
	if configPath != "" {
		m.V.SetConfigFile(configPath)
		if err := m.V.ReadInConfig(); err != nil {
			return RuntimeConfig{}, fmt.Errorf("read config: %w", err)
		}
	}

	return RuntimeConfig{
		Profile: m.V.GetString("runtime.profile"),
		Verbose: m.V.GetBool("runtime.verbose"),
	}, nil
}
