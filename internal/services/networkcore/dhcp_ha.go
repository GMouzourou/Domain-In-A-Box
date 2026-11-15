package networkcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const defaultKeaControlSocket = "/run/kea/kea4-ctrl-socket"

type DHCPHAStatus struct {
	State             string   `json:"state"`
	Scopes            []string `json:"scopes"`
	UnsentUpdateCount uint64   `json:"unsent-update-count"`
}

type keaControlRequest struct {
	Command   string         `json:"command"`
	Service   []string       `json:"service"`
	Arguments map[string]any `json:"arguments"`
}

type keaControlResponse struct {
	Result    int          `json:"result"`
	Text      string       `json:"text"`
	Arguments DHCPHAStatus `json:"arguments"`
}

func NewDHCPHACommand(runner *Runner) *cobra.Command {
	command := &cobra.Command{
		Use:   "dhcp-ha",
		Short: "Inspect and manually manage DHCP HA authority",
	}
	command.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show the local Kea HA state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			status, err := runner.DHCPHAStatus(cmd.Context())
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
		},
	})

	var confirmPrimaryOff, acceptUnsentUpdates bool
	takeover := &cobra.Command{
		Use:   "takeover",
		Short: "Enable DHCP on the standby after confirming the primary is off",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.DHCPHATakeover(cmd.Context(), confirmPrimaryOff)
		},
	}
	takeover.Flags().BoolVar(&confirmPrimaryOff, "confirm-primary-off", false, "confirm the primary host is powered off or permanently lost")
	// Kea's unsent-update-count is cumulative and never reset, so it cannot
	// gate a takeover. The flag remains accepted for existing runbooks.
	takeover.Flags().BoolVar(&acceptUnsentUpdates, "accept-unsent-updates", false, "deprecated; has no effect")
	_ = takeover.Flags().MarkDeprecated("accept-unsent-updates", "the unsent-update gate was removed; the flag has no effect")
	command.AddCommand(takeover)

	var confirmPrimaryDHCPStopped bool
	resume := &cobra.Command{
		Use:   "resume",
		Short: "Withdraw standby DHCP authority before restoring the primary",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.DHCPHAResume(cmd.Context(), confirmPrimaryDHCPStopped)
		},
	}
	resume.Flags().BoolVar(&confirmPrimaryDHCPStopped, "confirm-primary-dhcp-stopped", false, "confirm the primary Kea DHCP service is stopped")
	command.AddCommand(resume)
	return command
}

func (r *Runner) DHCPHAStatus(ctx context.Context) (DHCPHAStatus, error) {
	if err := requireDHCPHAEnabled(); err != nil {
		return DHCPHAStatus{}, err
	}
	response, err := sendKeaControlCommand(ctx, keaControlSocketPath(), "ha-heartbeat", nil)
	if err != nil {
		return DHCPHAStatus{}, err
	}
	if response.Result != 0 {
		return DHCPHAStatus{}, fmt.Errorf("Kea ha-heartbeat failed (%d): %s", response.Result, response.Text)
	}
	if response.Arguments.State == "" {
		return DHCPHAStatus{}, errors.New("Kea ha-heartbeat returned no HA state")
	}
	return response.Arguments, nil
}

func (r *Runner) DHCPHATakeover(ctx context.Context, confirmPrimaryOff bool) error {
	if !confirmPrimaryOff {
		return errors.New("refusing DHCP takeover without --confirm-primary-off")
	}
	if err := requireStandbyDHCPHA(); err != nil {
		return err
	}
	status, err := r.DHCPHAStatus(ctx)
	if err != nil {
		return err
	}
	if status.State != "partner-down" {
		return fmt.Errorf("refusing DHCP takeover: local Kea state is %q, not partner-down", status.State)
	}
	primaryName := configuredDHCPHAPrimaryName()
	if containsString(status.Scopes, primaryName) {
		return errors.New("refusing DHCP takeover: standby already reports the primary DHCP scope")
	}
	response, err := sendKeaControlCommand(ctx, keaControlSocketPath(), "ha-scopes", map[string]any{
		"scopes": []string{primaryName},
	})
	if err != nil {
		return err
	}
	if response.Result != 0 {
		return fmt.Errorf("Kea ha-scopes takeover failed (%d): %s", response.Result, response.Text)
	}
	r.log.Infof("standby DHCP scope enabled; primary host must remain off until authority is withdrawn")
	return nil
}

func (r *Runner) DHCPHAResume(ctx context.Context, confirmPrimaryDHCPStopped bool) error {
	if !confirmPrimaryDHCPStopped {
		return errors.New("refusing to withdraw standby DHCP authority without --confirm-primary-dhcp-stopped")
	}
	if err := requireStandbyDHCPHA(); err != nil {
		return err
	}
	status, err := r.DHCPHAStatus(ctx)
	if err != nil {
		return err
	}
	if status.State != "partner-down" {
		return fmt.Errorf("refusing to withdraw standby authority: local Kea state is %q, not partner-down", status.State)
	}
	response, err := sendKeaControlCommand(ctx, keaControlSocketPath(), "ha-scopes", map[string]any{
		"scopes": []string{},
	})
	if err != nil {
		return err
	}
	if response.Result != 0 {
		return fmt.Errorf("Kea ha-scopes resume failed (%d): %s", response.Result, response.Text)
	}
	r.log.Infof("standby DHCP scope withdrawn; start the primary DHCP service only after this command succeeds")
	return nil
}

func requireStandbyDHCPHA() error {
	if err := requireDHCPHAEnabled(); err != nil {
		return err
	}
	if os.Getenv("DIB_CONTROLLER_MODE") != "secondary" {
		return errors.New("manual DHCP authority changes are allowed only on the standby controller")
	}
	return nil
}

func requireDHCPHAEnabled() error {
	enabled, err := dhcpHAEnabled()
	if err != nil {
		return err
	}
	if !enabled {
		return errors.New("DHCP HA is not enabled on this controller")
	}
	return nil
}

func keaControlSocketPath() string {
	if path := os.Getenv("DIB_KEA_CONTROL_SOCKET"); path != "" {
		return path
	}
	return defaultKeaControlSocket
}

func sendKeaControlCommand(ctx context.Context, socketPath, command string, arguments map[string]any) (keaControlResponse, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return keaControlResponse{}, fmt.Errorf("connect to Kea control socket %s: %w", socketPath, err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return keaControlResponse{}, fmt.Errorf("set Kea control socket deadline: %w", err)
	}
	request := keaControlRequest{
		Command:   command,
		Service:   []string{"dhcp4"},
		Arguments: arguments,
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return keaControlResponse{}, fmt.Errorf("send Kea %s command: %w", command, err)
	}
	var response keaControlResponse
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		return keaControlResponse{}, fmt.Errorf("read Kea %s response: %w", command, err)
	}
	return response, nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}
