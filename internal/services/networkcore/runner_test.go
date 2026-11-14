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

func TestEnsureCronEntryPreservesJobsAndAvoidsDuplicates(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "crontab.txt")
	command := filepath.Join(dir, "crontab")
	script := `#!/bin/sh
if [ "$3" = "-l" ]; then
    [ -f "$DIB_TEST_CRONTAB" ] || exit 1
    cat "$DIB_TEST_CRONTAB"
    exit 0
fi
if [ "$3" = "-" ]; then
    cat > "$DIB_TEST_CRONTAB"
    exit 0
fi
exit 2
`
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DIB_TEST_CRONTAB", state)
	line := "0 */4 * * * /usr/bin/kinit -kt /var/lib/kea/keaddns.keytab keaddns@DOMAIN.HOME.ARPA"
	if err := os.WriteFile(state, []byte("15 * * * * /usr/bin/other-job\n"+line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := ensureCronEntry(context.Background(), "_kea", line); err != nil {
			t.Fatalf("ensureCronEntry() error = %v", err)
		}
	}

	contents, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(contents), line); got != 1 {
		t.Fatalf("renewal job count = %d, want 1; crontab:\n%s", got, contents)
	}
	if !strings.Contains(string(contents), "/usr/bin/other-job") {
		t.Fatalf("unrelated crontab entry was removed: %s", contents)
	}
}

func TestKinitKeaDDNSUsesControllerPrincipal(t *testing.T) {
	dir := t.TempDir()
	result := filepath.Join(dir, "kinit-args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$KRB5CCNAME\" \"$@\" > \"$DIB_TEST_KINIT_ARGS\"\n"
	if err := os.WriteFile(filepath.Join(dir, "kinit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DIB_TEST_KINIT_ARGS", result)

	if err := kinitKeaDDNS(context.Background(), "dib-0", "DOMAIN.HOME.ARPA"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	want := "FILE:/run/kea/keaddns.ccache\n-kt\n/var/lib/kea/keaddns.keytab\nkea-dib-0@DOMAIN.HOME.ARPA\n"
	if string(got) != want {
		t.Fatalf("kinit invocation = %q, want %q", got, want)
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

	for _, component := range []string{"kea-dhcp4", "kea-dhcp-ddns"} {
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

func TestReconcileKeaDDNSPrincipalPreservesCustomConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kea-dhcp-ddns.conf")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configuration := map[string]any{
		"DhcpDdns": map[string]any{
			"custom-option": "preserve-me",
			"hooks-libraries": []map[string]any{{
				"library": "libddns_gss_tsig.so",
				"parameters": map[string]any{
					"client-principal": "keaddns@EXAMPLE.TEST",
					"custom-parameter": "preserve-me",
				},
			}},
		},
	}
	if err := writeJSONAtomically(path, configuration); err != nil {
		t.Fatal(err)
	}
	if err := reconcileKeaDDNSPrincipal(path, "dib-secondary-0", "example.test", "EXAMPLE.TEST"); err != nil {
		t.Fatalf("reconcileKeaDDNSPrincipal() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var updated map[string]any
	if err := json.Unmarshal(contents, &updated); err != nil {
		t.Fatal(err)
	}
	ddns := updated["DhcpDdns"].(map[string]any)
	hook := ddns["hooks-libraries"].([]any)[0].(map[string]any)
	parameters := hook["parameters"].(map[string]any)
	if parameters["client-principal"] != "kea-dib-secondary-0@EXAMPLE.TEST" || parameters["custom-parameter"] != "preserve-me" || ddns["custom-option"] != "preserve-me" {
		t.Fatalf("DDNS configuration was not reconciled while preserving custom fields: %#v", updated)
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

	err := NewRunner().DHCPHATakeover(context.Background(), true, false)
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

	err := NewRunner().DHCPHATakeover(context.Background(), true, false)
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

	if err := NewRunner().DHCPHATakeover(context.Background(), true, false); err != nil {
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

func TestDHCPHATakeoverRequiresExplicitLeaseDivergenceAcceptance(t *testing.T) {
	t.Setenv("DIB_DHCP_HA_ENABLED", "true")
	t.Setenv("DIB_CONTROLLER_MODE", "secondary")
	socketPath := filepath.Join(t.TempDir(), "kea.sock")
	t.Setenv("DIB_KEA_CONTROL_SOCKET", socketPath)
	startControlSocket(t, socketPath, func(request keaControlRequest) keaControlResponse {
		return keaControlResponse{Result: 0, Arguments: DHCPHAStatus{State: "partner-down", UnsentUpdateCount: 3}}
	})

	err := NewRunner().DHCPHATakeover(context.Background(), true, false)
	if err == nil || !strings.Contains(err.Error(), "--accept-unsent-updates") {
		t.Fatalf("DHCPHATakeover() error = %v, want unsent-update warning", err)
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
