package dnsbridge

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

//go:embed provision.py
var provisionScript string

func (r *Runner) Provision(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("identity provisioning requires root inside a domain controller")
	}
	cfg, err := LoadConfig(r.ConfigPath)
	if err != nil {
		return err
	}
	policy, err := NewPolicy(cfg)
	if err != nil {
		return err
	}
	backend, err := NewBackend(cfg, policy)
	if err != nil {
		return err
	}
	password, err := readPassword(cfg.PasswordFile)
	if err != nil {
		return err
	}
	protected := make([]string, 0, len(policy.protected))
	for name := range policy.protected {
		protected = append(protected, name)
	}
	slices.Sort(protected)
	//nolint:gosec // G117: the password is deliberately passed to the provisioning helper over stdin.
	payload, err := json.Marshal(struct {
		Config    Config   `json:"config"`
		Password  string   `json:"password"`
		Protected []string `json:"protected"`
	}{cfg, password, protected})
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "python3", "-c", provisionScript)
	command.Stdin = bytes.NewReader(payload)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("controller-local identity provisioning failed: %w: %s", err, strings.ReplaceAll(string(output), password, "[REDACTED]"))
	}
	if _, err := backend.Records(ctx); err != nil {
		return fmt.Errorf("identity ACLs were reconciled without rotating its password, but verified DNS access failed: %w", err)
	}
	r.log.Infof("Reconciled dedicated identity and verified DNS access: %s", strings.TrimSpace(string(output)))
	return nil
}
