package networkcore

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateKeaDHCPPoolPreservesOtherConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kea-dhcp4.conf")
	configuration := `{
    "Dhcp4": {
        "custom-option": "preserve-me",
        "subnet4": [
            {"id": 2, "pools": [{"pool": "10.0.0.10-10.0.0.20"}]},
            {"id": 1, "pools": [{"pool": "192.168.1.10-192.168.1.20"}]}
        ]
    }
}`
	if err := os.WriteFile(path, []byte(configuration), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := updateKeaDHCPPool(path, "192.168.1.100-192.168.1.199"); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(contents)
	if !strings.Contains(updated, `"pool": "192.168.1.100-192.168.1.199"`) {
		fatalf(t, "managed pool was not updated: %s", updated)
	}
	if !strings.Contains(updated, `"custom-option": "preserve-me"`) || !strings.Contains(updated, `"pool": "10.0.0.10-10.0.0.20"`) {
		fatalf(t, "unmanaged configuration changed: %s", updated)
	}
}

func TestUpdateBindForwardersPreservesOtherConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "named.conf.options")
	configuration := `options {
    custom-option yes;
    forwarders { 1.1.1.1; 8.8.8.8; };
    listen-on port 5353 { 192.168.1.1; };
};
`
	if err := os.WriteFile(path, []byte(configuration), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := updateBindForwarders(path, "9.9.9.9; 149.112.112.112;"); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(contents)
	if !strings.Contains(updated, "forwarders { 9.9.9.9; 149.112.112.112; };") {
		fatalf(t, "forwarders were not updated: %s", updated)
	}
	if !strings.Contains(updated, "custom-option yes;") || !strings.Contains(updated, "listen-on port 5353") {
		fatalf(t, "unmanaged configuration changed: %s", updated)
	}
}

func TestDHCPEnabledDefaultsToTrueAndParsesConfiguredValue(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		configured bool
		want       bool
		wantErr    bool
	}{
		{name: "default", want: true},
		{name: "enabled", value: "true", configured: true, want: true},
		{name: "disabled", value: "false", configured: true, want: false},
		{name: "invalid", value: "sometimes", configured: true, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseDHCPEnabled(test.value, test.configured)
			if (err != nil) != test.wantErr {
				t.Fatalf("dhcpEnabled() error = %v, wantErr %v", err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("dhcpEnabled() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRunDoesNotStartKeaWhenDHCPIsDisabled(t *testing.T) {
	t.Setenv("DIB_DHCP_ENABLED", "false")
	t.Setenv("DIB_DHCP_HA_ENABLED", "false")
	runner := NewRunner()

	for _, component := range []string{"kea-dhcp4", "dhcp-ddns"} {
		t.Run(component, func(t *testing.T) {
			t.Setenv("DIB_NETWORK_COMPONENT", component)
			if err := runner.Run(context.Background()); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
		})
	}
}

func TestDHCPHASettingsRequireDifferentIPv4AddressesAndLocalRoleAddress(t *testing.T) {
	t.Setenv("DIB_DHCP_HA_ENABLED", "true")
	t.Setenv("DIB_DHCP_HA_PRIMARY_ADDRESS", "192.0.2.10")
	t.Setenv("DIB_DHCP_HA_STANDBY_ADDRESS", "192.0.2.11")
	t.Setenv("DIB_DHCP_HA_PRIMARY_NAME", "primary")
	t.Setenv("DIB_DHCP_HA_STANDBY_NAME", "standby")

	settings, err := dhcpHASettingsFromEnvironment("primary", "192.0.2.10")
	if err != nil {
		t.Fatalf("dhcpHASettingsFromEnvironment() error = %v", err)
	}
	if settings.primaryName != "primary" || settings.standbyName != "standby" {
		t.Fatalf("server names = %q/%q, want primary/standby", settings.primaryName, settings.standbyName)
	}

	if _, err := dhcpHASettingsFromEnvironment("primary", "192.0.2.11"); err == nil {
		t.Fatal("dhcpHASettingsFromEnvironment() accepted the standby address for the primary")
	}
	t.Setenv("DIB_DHCP_HA_STANDBY_ADDRESS", "192.0.2.10")
	if _, err := dhcpHASettingsFromEnvironment("primary", "192.0.2.10"); err == nil {
		t.Fatal("dhcpHASettingsFromEnvironment() accepted duplicate peer addresses")
	}
}

func TestBuildKeaDHCP4ConfigurationAddsManualTLSHotStandby(t *testing.T) {
	settings := dhcpHASettings{
		enabled:        true,
		controllerMode: "secondary",
		primaryAddress: "192.0.2.10",
		standbyAddress: "192.0.2.11",
		primaryName:    "primary",
		standbyName:    "standby",
	}
	configuration := buildKeaDHCP4Configuration("net1", "192.0.2.11", "192.0.2.254", "example.test", "192.0.2.0/24", "192.0.2.100-192.0.2.199", settings)
	dhcp4 := configuration["Dhcp4"].(map[string]any)
	haLibrary := dhcp4["hooks-libraries"].([]map[string]any)[1]
	haParameters := haLibrary["parameters"].(map[string]any)
	ha := haParameters["high-availability"].([]map[string]any)[0]
	if ha["this-server-name"] != "standby" || ha["mode"] != "hot-standby" {
		t.Fatalf("HA identity/mode = %v/%v", ha["this-server-name"], ha["mode"])
	}
	if ha["trust-anchor"] != "/run/kea-ha-tls/ca.pem" || ha["cert-file"] != "/run/kea-ha-tls/cert.pem" || ha["key-file"] != "/run/kea-ha-tls/key.pem" {
		t.Fatalf("HA TLS paths = %v/%v/%v", ha["trust-anchor"], ha["cert-file"], ha["key-file"])
	}
	if ha["max-unacked-clients"] != 0 {
		t.Fatalf("max-unacked-clients = %v, want 0", ha["max-unacked-clients"])
	}
	peers := ha["peers"].([]map[string]any)
	if len(peers) != 2 || peers[0]["role"] != "primary" || peers[1]["role"] != "standby" {
		t.Fatalf("HA peers = %#v", peers)
	}
	for _, peer := range peers {
		if peer["auto-failover"] != false || peer["require-client-certs"] != true {
			t.Fatalf("peer permits automatic failover or lacks mutual TLS: %#v", peer)
		}
	}
	subnets := dhcp4["subnet4"].([]map[string]any)
	pool := subnets[0]["pools"].([]map[string]any)[0]
	if classes := pool["client-classes"].([]string); len(classes) != 1 || classes[0] != "HA_primary" {
		t.Fatalf("DHCP pool classes = %#v", classes)
	}
}

func TestKeaDDNSAccountIsUniquePerControllerAndFitsSambaLimit(t *testing.T) {
	primary := keaDDNSAccount("dib-primary-0")
	standby := keaDDNSAccount("dib-secondary-0")
	if primary == standby {
		t.Fatal("controllers share a Kea DDNS account")
	}
	if len(standby) > 20 {
		t.Fatalf("standby Kea DDNS account has %d characters, exceeding Samba's 20-character limit", len(standby))
	}
}

func TestValidateKeaDHCPHAConfigurationRequiresManualFailoverPeers(t *testing.T) {
	settings := dhcpHASettings{
		enabled:        true,
		controllerMode: "primary",
		primaryAddress: "192.0.2.10",
		standbyAddress: "192.0.2.11",
		primaryName:    "primary",
		standbyName:    "standby",
	}
	path := filepath.Join(t.TempDir(), "kea-dhcp4.conf")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configuration := buildKeaDHCP4Configuration("net1", "192.0.2.10", "192.0.2.254", "example.test", "192.0.2.0/24", "192.0.2.100-192.0.2.199", settings)
	if err := writeJSONAtomically(path, configuration); err != nil {
		t.Fatal(err)
	}
	if err := validateKeaDHCPHAConfiguration(path, settings); err != nil {
		t.Fatalf("validateKeaDHCPHAConfiguration() error = %v", err)
	}

	settings.standbyAddress = "192.0.2.12"
	if err := validateKeaDHCPHAConfiguration(path, settings); err == nil {
		t.Fatal("validateKeaDHCPHAConfiguration() accepted a stale peer address")
	}

	dhcp4 := configuration["Dhcp4"].(map[string]any)
	hooks := dhcp4["hooks-libraries"].([]map[string]any)
	parameters := hooks[1]["parameters"].(map[string]any)
	relationships := parameters["high-availability"].([]map[string]any)
	relationships[0]["max-unacked-clients"] = 10
	if err := writeJSONAtomically(path, configuration); err != nil {
		t.Fatal(err)
	}
	settings.standbyAddress = "192.0.2.11"
	if err := validateKeaDHCPHAConfiguration(path, settings); err == nil {
		t.Fatal("validateKeaDHCPHAConfiguration() accepted client-dependent partner-loss detection")
	}

	relationships[0]["max-unacked-clients"] = 0
	subnets := dhcp4["subnet4"].([]map[string]any)
	pools := subnets[0]["pools"].([]map[string]any)
	pools[0]["client-classes"] = []string{"HA_wrong-primary"}
	if err := writeJSONAtomically(path, configuration); err != nil {
		t.Fatal(err)
	}
	if err := validateKeaDHCPHAConfiguration(path, settings); err == nil {
		t.Fatal("validateKeaDHCPHAConfiguration() accepted a pool without the configured primary scope")
	}
}

func haTestSettings(mode string) dhcpHASettings {
	return dhcpHASettings{
		enabled:        true,
		controllerMode: mode,
		primaryAddress: "192.0.2.10",
		standbyAddress: "192.0.2.11",
		primaryName:    "primary",
		standbyName:    "standby",
	}
}

func readKeaTestConfiguration(t *testing.T, path string) map[string]any {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var configuration map[string]any
	if err := json.Unmarshal(contents, &configuration); err != nil {
		t.Fatal(err)
	}
	return configuration["Dhcp4"].(map[string]any)
}

func TestReconcileKeaDHCP4ConfigurationUpgradesPersistedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kea-dhcp4.conf")
	legacy := `{"Dhcp4": {
		"custom-option": "keep",
		"dhcp-ddns": {"enable-updates": true},
		"option-data": [
			{"name": "domain-name-servers", "code": 6, "data": "192.0.2.10"},
			{"name": "ntp-servers", "code": 42, "data": "192.0.2.1"}
		],
		"subnet4": [{"id": 1, "subnet": "192.0.2.0/24", "pools": [{"pool": "192.0.2.100-192.0.2.199", "client-classes": ["HA_primary"]}]}],
		"hooks-libraries": [
			{"library": "libdhcp_lease_cmds.so"},
			{"library": "libdhcp_ha.so", "parameters": {"high-availability": [{"this-server-name": "primary", "peers": [
				{"name": "primary", "url": "https://192.0.2.10:8005/", "role": "primary"},
				{"name": "standby", "url": "https://192.0.2.99:8005/", "role": "standby"}
			]}]}}
		]
	}}`
	if err := os.WriteFile(path, []byte(legacy), 0o640); err != nil {
		t.Fatal(err)
	}
	settings := haTestSettings("primary")
	desired := buildKeaDHCP4Configuration("net1", "192.0.2.10", "192.0.2.254", "example.test", "192.0.2.0/24", "192.0.2.100-192.0.2.199", settings)
	setKeaDNSServers(desired, []string{"192.0.2.10", "192.0.2.11"})

	changed, err := reconcileKeaDHCP4Configuration(path, desired, settings)
	if err != nil || !changed {
		t.Fatalf("reconcileKeaDHCP4Configuration() = %v, %v; want changed", changed, err)
	}
	if err := validateKeaDHCPHAConfiguration(path, settings); err != nil {
		t.Fatalf("reconciled configuration failed HA validation: %v", err)
	}
	dhcp4 := readKeaTestConfiguration(t, path)
	if dhcp4["custom-option"] != "keep" || dhcp4["ddns-qualifying-suffix"] != "example.test." || dhcp4["ddns-update-on-renew"] != true {
		t.Fatalf("managed DDNS globals or operator settings wrong: %#v", dhcp4)
	}
	if ddns := dhcp4["dhcp-ddns"].(map[string]any); ddns["server-port"] != float64(53001) || ddns["ncr-protocol"] != "UDP" {
		t.Fatalf("dhcp-ddns = %#v", ddns)
	}
	options := dhcp4["option-data"].([]any)
	if len(options) != 2 || options[0].(map[string]any)["data"] != "192.0.2.10, 192.0.2.11" || options[1].(map[string]any)["name"] != "ntp-servers" {
		t.Fatalf("option-data = %#v", options)
	}
	if previous, err := os.ReadFile(path + ".dib-previous"); err != nil || string(previous) != legacy {
		t.Fatalf("previous configuration not preserved: %v", err)
	}

	if changed, err := reconcileKeaDHCP4Configuration(path, desired, settings); err != nil || changed {
		t.Fatalf("second reconciliation = %v, %v; want unchanged", changed, err)
	}
}

func TestReconcileKeaDHCP4ConfigurationRefusesRoleSwap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kea-dhcp4.conf")
	primary := haTestSettings("primary")
	configuration := buildKeaDHCP4Configuration("net1", "192.0.2.10", "192.0.2.254", "example.test", "192.0.2.0/24", "192.0.2.100-192.0.2.199", primary)
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomically(path, configuration); err != nil {
		t.Fatal(err)
	}
	secondary := haTestSettings("secondary")
	desired := buildKeaDHCP4Configuration("net1", "192.0.2.11", "192.0.2.254", "example.test", "192.0.2.0/24", "192.0.2.100-192.0.2.199", secondary)
	if _, err := reconcileKeaDHCP4Configuration(path, desired, secondary); err == nil || !strings.Contains(err.Error(), "opposite role") {
		t.Fatalf("reconcileKeaDHCP4Configuration() error = %v, want role-swap refusal", err)
	}
}

func TestDHCPDNSServersListsBothHAPeersLocalFirst(t *testing.T) {
	got, err := dhcpDNSServers("192.0.2.11", haTestSettings("secondary"))
	if err != nil || strings.Join(got, ",") != "192.0.2.11,192.0.2.10" {
		t.Fatalf("dhcpDNSServers() = %v, %v", got, err)
	}
	t.Setenv("DIB_DHCP_DNS_SERVERS", "192.0.2.53, 192.0.2.54")
	got, err = dhcpDNSServers("192.0.2.11", haTestSettings("secondary"))
	if err != nil || strings.Join(got, ",") != "192.0.2.53,192.0.2.54" {
		t.Fatalf("dhcpDNSServers() override = %v, %v", got, err)
	}
	t.Setenv("DIB_DHCP_DNS_SERVERS", "dns.example.test")
	if _, err := dhcpDNSServers("192.0.2.11", haTestSettings("secondary")); err == nil {
		t.Fatal("dhcpDNSServers() accepted a non-IPv4 server")
	}
}

func TestRepairKeaLogPermissionsRestoresTraversableDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "kea")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "kea-dhcp4.log")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "link.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o664); err != nil {
		t.Fatal(err)
	}
	if err := repairKeaLogPermissions(directory); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{directory: 0o775 | os.ModeSetgid | os.ModeDir, file: 0o664, outside: 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode() != want {
			t.Fatalf("%s mode = %v, want %v (%v)", path, info.Mode(), want, err)
		}
	}
	if err := repairKeaLogPermissions(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("missing directory: %v", err)
	}
}

func TestDHCPHATakeoverRequiresPartnerDownAndAllowsOnlyStandby(t *testing.T) {
	t.Setenv("DIB_DHCP_HA_ENABLED", "true")
	t.Setenv("DIB_CONTROLLER_MODE", "secondary")
	socketPath := filepath.Join(t.TempDir(), "kea.sock")
	t.Setenv("DIB_KEA_CONTROL_SOCKET", socketPath)
	startControlSocket(t, socketPath, func(request keaControlRequest) keaControlResponse {
		if request.Command != "ha-heartbeat" {
			t.Fatalf("unexpected command after non-partner-down state: %s", request.Command)
		}
		return keaControlResponse{Result: 0, Arguments: DHCPHAStatus{State: "hot-standby"}}
	})

	err := NewRunner().DHCPHATakeover(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "not partner-down") {
		t.Fatalf("DHCPHATakeover() error = %v, want partner-down refusal", err)
	}
}

func TestDHCPHATakeoverRefusesWhenPrimaryPeerIsAlreadyEnabled(t *testing.T) {
	t.Setenv("DIB_DHCP_HA_ENABLED", "true")
	t.Setenv("DIB_CONTROLLER_MODE", "secondary")
	socketPath := filepath.Join(t.TempDir(), "kea.sock")
	t.Setenv("DIB_KEA_CONTROL_SOCKET", socketPath)
	startControlSocket(t, socketPath, func(request keaControlRequest) keaControlResponse {
		if request.Command != "ha-heartbeat" {
			t.Fatalf("unexpected command after primary scope was reported enabled: %s", request.Command)
		}
		return keaControlResponse{Result: 0, Arguments: DHCPHAStatus{State: "partner-down", Scopes: []string{"dib-primary"}}}
	})

	err := NewRunner().DHCPHATakeover(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "already reports") {
		t.Fatalf("DHCPHATakeover() error = %v, want refusal for already-enabled primary peer", err)
	}
}

func TestDHCPHATakeoverEnablesPrimaryScopeOnlyAfterExplicitConfirmation(t *testing.T) {
	t.Setenv("DIB_DHCP_HA_ENABLED", "true")
	t.Setenv("DIB_CONTROLLER_MODE", "secondary")
	socketPath := filepath.Join(t.TempDir(), "kea.sock")
	t.Setenv("DIB_KEA_CONTROL_SOCKET", socketPath)
	requests := make(chan keaControlRequest, 2)
	startControlSocket(t, socketPath, func(request keaControlRequest) keaControlResponse {
		requests <- request
		if request.Command == "ha-heartbeat" {
			return keaControlResponse{Result: 0, Arguments: DHCPHAStatus{State: "partner-down"}}
		}
		return keaControlResponse{Result: 0, Text: "HA scopes set"}
	})

	if err := NewRunner().DHCPHATakeover(context.Background(), true); err != nil {
		t.Fatalf("DHCPHATakeover() error = %v", err)
	}
	heartbeat := <-requests
	scopes := <-requests
	if heartbeat.Command != "ha-heartbeat" || scopes.Command != "ha-scopes" {
		t.Fatalf("commands = %q, %q", heartbeat.Command, scopes.Command)
	}
	if got := scopes.Arguments["scopes"].([]any); len(got) != 1 || got[0] != "dib-primary" {
		t.Fatalf("takeover scopes = %#v", scopes.Arguments["scopes"])
	}
}

func TestDHCPHATakeoverIgnoresCumulativeUnsentUpdateCount(t *testing.T) {
	t.Setenv("DIB_DHCP_HA_ENABLED", "true")
	t.Setenv("DIB_CONTROLLER_MODE", "secondary")
	socketPath := filepath.Join(t.TempDir(), "kea.sock")
	t.Setenv("DIB_KEA_CONTROL_SOCKET", socketPath)
	startControlSocket(t, socketPath, func(request keaControlRequest) keaControlResponse {
		if request.Command == "ha-heartbeat" {
			return keaControlResponse{Result: 0, Arguments: DHCPHAStatus{State: "partner-down", UnsentUpdateCount: 1}}
		}
		return keaControlResponse{Result: 0, Text: "HA scopes set"}
	})

	if err := NewRunner().DHCPHATakeover(context.Background(), true); err != nil {
		t.Fatalf("DHCPHATakeover() error = %v; a counter left by an earlier takeover must not block a later one", err)
	}
}

func startControlSocket(t *testing.T, path string, respond func(keaControlRequest) keaControlResponse) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on test Kea socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				var request keaControlRequest
				if err := json.NewDecoder(connection).Decode(&request); err != nil {
					t.Errorf("decode test Kea command: %v", err)
					return
				}
				if err := json.NewEncoder(connection).Encode(respond(request)); err != nil {
					t.Errorf("encode test Kea response: %v", err)
				}
			}()
		}
	}()
}

func fatalf(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf(format, args...)
}
