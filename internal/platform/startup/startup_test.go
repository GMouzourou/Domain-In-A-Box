package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func environment(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func requiredEnvironment() map[string]string {
	return map[string]string{
		"DIB_REALM":          "home.example.com",
		"DIB_INTERFACE":      "eth0",
		"DIB_DHCP_POOL":      "192.168.1.100 - 192.168.1.199",
		"DIB_DNS_FORWARDERS": "1.1.1.1",
	}
}

func TestLoadSettingsDefaults(t *testing.T) {
	settings, err := LoadSettings(environment(requiredEnvironment()), "DC1")
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{
		Realm: "HOME.EXAMPLE.COM", Domain: "HOME", DNSDomain: "home.example.com", Mode: "primary",
		Interface: "eth0", Hostname: "dc1", MetricsPort: "9922",
	}
	if settings != want {
		t.Fatalf("settings = %+v, want %+v", settings, want)
	}
}

func TestLoadSettingsTruncatesDefaultDomain(t *testing.T) {
	values := requiredEnvironment()
	values["DIB_REALM"] = "averyveryverylongname.example"
	settings, err := LoadSettings(environment(values), "dc1")
	if err != nil {
		t.Fatal(err)
	}
	if settings.Domain != "AVERYVERYVERYLO" {
		t.Fatalf("domain = %q", settings.Domain)
	}
}

func TestLoadSettingsRejectsInvalidInput(t *testing.T) {
	tests := map[string]struct {
		change   func(map[string]string)
		hostname string
		want     string
	}{
		"missing realm":       {func(v map[string]string) { delete(v, "DIB_REALM") }, "dc1", "DIB_REALM is not set"},
		"missing forwarders":  {func(v map[string]string) { delete(v, "DIB_DNS_FORWARDERS") }, "dc1", "DIB_DNS_FORWARDERS is not set"},
		"unknown mode":        {func(v map[string]string) { v["DIB_CONTROLLER_MODE"] = "backup" }, "dc1", "must be primary or secondary"},
		"secondary no server": {func(v map[string]string) { v["DIB_CONTROLLER_MODE"] = "secondary" }, "dc2", "DIB_JOIN_SERVER is required"},
		"secondary long name": {func(v map[string]string) {
			v["DIB_CONTROLLER_MODE"] = "secondary"
			v["DIB_JOIN_SERVER"] = "192.168.1.10"
		}, "domain-controller-2", "no more than 15 characters"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			values := requiredEnvironment()
			test.change(values)
			if _, err := LoadSettings(environment(values), test.hostname); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReverseZone(t *testing.T) {
	tests := map[string]string{
		"10.0.0.0/8":       "10.in-addr.arpa",
		"172.16.0.0/16":    "16.172.in-addr.arpa",
		"192.168.1.0/24":   "1.168.192.in-addr.arpa",
		"192.168.1.128/25": "1.168.192.in-addr.arpa",
	}
	for subnet, want := range tests {
		if got, err := ReverseZone(subnet, ""); err != nil || got != want {
			t.Errorf("ReverseZone(%s) = %q, %v; want %q", subnet, got, err, want)
		}
	}
	if got, _ := ReverseZone("192.168.0.0/22", "168.192.IN-ADDR.ARPA."); got != "168.192.in-addr.arpa" {
		t.Errorf("override = %q", got)
	}
	for _, subnet := range []string{"192.168.0.0/22", "", "fd00::/64"} {
		if _, err := ReverseZone(subnet, ""); err == nil || !strings.Contains(err.Error(), "set DIB_REVERSE_ZONE") {
			t.Errorf("ReverseZone(%q) error = %v", subnet, err)
		}
	}
}

func TestParseNetwork(t *testing.T) {
	addresses := []byte(`[{"ifname":"eth0","addr_info":[{"family":"inet6","local":"fe80::1"},{"family":"inet","local":"192.168.2.10","prefixlen":24},{"family":"inet","local":"192.168.2.11"}]}]`)
	routes := []byte(`[{"dst":"default","gateway":"192.168.2.254","protocol":"static"},{"dst":"192.168.2.0/24","protocol":"kernel","scope":"link"},{"dst":"192.168.2.8","scope":"link"}]`)
	network, err := ParseNetwork(addresses, routes)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Network{IP: "192.168.2.10", Subnet: "192.168.2.0/24", Gateway: "192.168.2.254"}); network != want {
		t.Fatalf("network = %+v, want %+v", network, want)
	}
	if _, err := ParseNetwork([]byte("not json"), routes); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestResolvConf(t *testing.T) {
	content := []byte("# generated\nnameserver 10.43.0.10\nnameserver 192.168.2.10\nsearch ns.svc.cluster.local svc.cluster.local\noptions ndots:5\nsearch ignored\n")
	nameservers, search := ParseResolvConf(content, "192.168.2.10")
	if _, search := ParseResolvConf([]byte("search .\nnameserver 127.0.0.11\n"), ""); len(search) != 0 {
		t.Fatalf("root search domain kept: %v", search)
	}
	if strings.Join(nameservers, " ") != "10.43.0.10" || strings.Join(search, " ") != "ns.svc.cluster.local svc.cluster.local" {
		t.Fatalf("nameservers = %v, search = %v", nameservers, search)
	}
	got := string(ResolvConf(append([]string{"home.example.com"}, search...), "192.168.2.10"))
	if want := "search home.example.com ns.svc.cluster.local svc.cluster.local\nnameserver 192.168.2.10\n"; got != want {
		t.Fatalf("resolv.conf = %q, want %q", got, want)
	}
}

func TestHosts(t *testing.T) {
	content := []byte("127.0.0.1\tlocalhost\n\n172.18.0.5\tdc1\n192.168.2.10 old-name\n192.168.2.100 dc10 dc1.alias\n10.0.0.1 db dc1 extra\n")
	got := string(Hosts(content, "192.168.2.10", "dc1", "home.example.com"))
	want := "127.0.0.1\tlocalhost\n\n192.168.2.100 dc10 dc1.alias\n192.168.2.10    dc1.home.example.com    dc1\n"
	if got != want {
		t.Fatalf("hosts =\n%s\nwant\n%s", got, want)
	}
	if got := string(Hosts(nil, "192.168.2.10", "dc1", "home.example.com")); got != "192.168.2.10    dc1.home.example.com    dc1\n" {
		t.Fatalf("empty hosts = %q", got)
	}
}

func newState(t *testing.T) SambaState {
	t.Helper()
	dir := t.TempDir()
	config := filepath.Join(dir, "etc-samba-image")
	if err := os.Mkdir(config, 0o755); err != nil {
		t.Fatal(err)
	}
	return SambaState{Directory: filepath.Join(dir, "samba"), ConfigDir: config}
}

func TestPrepareFirstPrimaryStart(t *testing.T) {
	state := newState(t)
	initDomain, err := state.Prepare("primary")
	if err != nil || !initDomain {
		t.Fatalf("Prepare = %v, %v; want true", initDomain, err)
	}
	if target, err := os.Readlink(state.ConfigDir); err != nil || target != filepath.Join(state.Directory, "etc-samba") {
		t.Fatalf("config link = %q, %v", target, err)
	}
	if mode, _ := os.ReadFile(filepath.Join(state.Directory, ".dib-controller-mode")); string(mode) != "primary\n" {
		t.Fatalf("mode marker = %q", mode)
	}
	if !state.BootstrapPending() {
		t.Fatal("bootstrap should be pending")
	}
}

func TestPrepareProvisionedRestart(t *testing.T) {
	state := newState(t)
	if _, err := state.Prepare("primary"); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkProvisioned(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state.ConfigDir, "smb.conf"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if initDomain, err := state.Prepare("primary"); err != nil || initDomain {
		t.Fatalf("Prepare = %v, %v; want false", initDomain, err)
	}
	if _, err := state.Prepare("secondary"); err == nil || !strings.Contains(err.Error(), "Controller mode changed from primary to secondary") {
		t.Fatalf("mode change error = %v", err)
	}
	if err := os.Remove(filepath.Join(state.ConfigDir, "smb.conf")); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Prepare("primary"); err == nil || !strings.Contains(err.Error(), "smb.conf is missing") {
		t.Fatalf("missing smb.conf error = %v", err)
	}
}

func TestPrepareRejectsUnrecordedSecondary(t *testing.T) {
	state := newState(t)
	if err := os.MkdirAll(state.Directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state.Directory, ".dib-provisioned"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Prepare("secondary"); err == nil || !strings.Contains(err.Error(), "no recorded controller mode") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrepareSecondaryJoinIsOneShot(t *testing.T) {
	state := newState(t)
	if initDomain, err := state.Prepare("secondary"); err != nil || !initDomain {
		t.Fatalf("first Prepare = %v, %v", initDomain, err)
	}
	if _, err := state.Prepare("secondary"); err == nil || !strings.Contains(err.Error(), "partial Samba state") {
		t.Fatalf("retry error = %v", err)
	}
	if err := state.MarkProvisioned(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state.Directory, ".dib-secondary-join-started")); !os.IsNotExist(err) {
		t.Fatalf("join marker remains: %v", err)
	}
}
