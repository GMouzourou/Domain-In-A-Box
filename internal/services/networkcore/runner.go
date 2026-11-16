package networkcore

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GMouzourou/domain-in-a-box/internal/platform/bootstrap"
	platformlog "github.com/GMouzourou/domain-in-a-box/internal/platform/log"
	"github.com/GMouzourou/domain-in-a-box/internal/platform/process"
	"github.com/GMouzourou/domain-in-a-box/internal/platform/wait"
)

type Runner struct {
	log *platformlog.Logger
}

func NewRunner() *Runner {
	return &Runner{log: platformlog.New("network-core")}
}

func (r *Runner) Name() string { return "network-core" }

func (r *Runner) Configure(ctx context.Context) error {
	dhcpEnabled, err := dhcpEnabled()
	if err != nil {
		return err
	}
	dhcpHAEnabled, err := dhcpHAEnabled()
	if err != nil {
		return err
	}
	if err := configureChrony(); err != nil {
		return err
	}
	if err := configureBind(ctx); err != nil {
		return err
	}
	if dhcpEnabled || dhcpHAEnabled {
		if err := configureKea(ctx); err != nil {
			return err
		}
	}
	if os.Getenv("INIT_DOMAIN") == "FALSE" {
		if err := updateBindForwarders("/etc/bind/named.conf.options", os.Getenv("DIB_DNS_FORWARDERS")); err != nil {
			return err
		}
		if dhcpEnabled {
			return updateKeaDHCPPool("/etc/kea/kea-dhcp4.conf", os.Getenv("DIB_DHCP_POOL"))
		}
	}
	return nil
}

func (r *Runner) Bootstrap(ctx context.Context) error {
	dhcpEnabled, err := dhcpEnabled()
	if err != nil {
		return err
	}
	dhcpHAEnabled, err := dhcpHAEnabled()
	if err != nil {
		return err
	}
	if !dhcpEnabled && !dhcpHAEnabled {
		r.log.Infof("DHCP and DDNS are disabled; skipping Kea bootstrap")
		return nil
	}
	realm, err := environment("DIB_REALM")
	if err != nil {
		return err
	}
	hostname, err := environment("CONTAINER_HOSTNAME")
	if err != nil {
		return err
	}
	ddnsAccount := keaDDNSAccount(hostname)
	type bootstrapStep struct {
		marker  string
		command []string
	}
	steps := []bootstrapStep{
		{"/var/lib/samba/." + ddnsAccount + "-created", []string{"samba-tool", "user", "create", ddnsAccount, "--description=DHCP DDNS GSS-TSIG service account", "--random-password"}},
		{"/var/lib/samba/." + ddnsAccount + "-expiry-set", []string{"samba-tool", "user", "setexpiry", ddnsAccount, "--noexpiry"}},
		{"/var/lib/samba/." + ddnsAccount + "-group-added", []string{"samba-tool", "group", "addmembers", "DnsAdmins", ddnsAccount}},
		{"/var/lib/kea/." + ddnsAccount + "-keytab-exported", []string{"samba-tool", "domain", "exportkeytab", "/var/lib/kea/keaddns.keytab", "--principal=" + ddnsAccount + "@" + realm}},
	}
	for _, step := range steps {
		step := step
		if err := bootstrap.RunOnce(ctx, step.marker, func(ctx context.Context) error {
			return run(ctx, step.command[0], step.command[1:]...)
		}); err != nil {
			return err
		}
	}

	if _, err := os.Stat("/var/lib/kea/keaddns.keytab"); err != nil {
		return fmt.Errorf("Kea DDNS keytab is missing: %w", err)
	}
	if err := run(ctx, "chown", "root:_kea", "/var/lib/kea/keaddns.keytab"); err != nil {
		return err
	}
	// The DDNS service obtains its own tickets as _kea. A root-owned cache
	// left by earlier releases would be unreadable, so remove it.
	if err := os.Remove("/run/kea/keaddns.ccache"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale Kea DDNS credential cache: %w", err)
	}
	return run(ctx, "chmod", "640", "/var/lib/kea/keaddns.keytab")
}

func (r *Runner) Validate(ctx context.Context) error {
	dhcpEnabled, err := dhcpEnabled()
	if err != nil {
		return err
	}
	dhcpHAEnabled, err := dhcpHAEnabled()
	if err != nil {
		return err
	}
	commands := [][]string{
		{"chronyd", "-p", "-f", "/etc/chrony/chrony.conf"},
		{"named-checkconf", "/etc/bind/named.conf"},
	}
	if dhcpEnabled || dhcpHAEnabled {
		commands = append(commands,
			[]string{"/usr/sbin/kea-dhcp4", "-t", "/etc/kea/kea-dhcp4.conf"},
		)
	}
	for _, command := range commands {
		if err := run(ctx, command[0], command[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) Run(ctx context.Context) error {
	switch os.Getenv("DIB_NETWORK_COMPONENT") {
	case "chrony":
		return process.Replace("/usr/sbin/chronyd", "-n", "-x", "-f", "/etc/chrony/chrony.conf")
	case "bind":
		return process.Replace("/usr/sbin/named", "-f", "-c", "/etc/bind/named.conf")
	case "kea-dhcp4":
		enabled, err := dhcpEnabled()
		if err != nil {
			return err
		}
		haEnabled, err := dhcpHAEnabled()
		if err != nil {
			return err
		}
		if !enabled && !haEnabled {
			r.log.Infof("DHCPv4 is disabled; not starting Kea")
			return nil
		}
		return process.Replace("/usr/sbin/kea-dhcp4", "-c", "/etc/kea/kea-dhcp4.conf")
	case "dhcp-ddns":
		enabled, err := dhcpEnabled()
		if err != nil {
			return err
		}
		haEnabled, err := dhcpHAEnabled()
		if err != nil {
			return err
		}
		if !enabled && !haEnabled {
			r.log.Infof("DHCP DDNS is disabled; not starting the DDNS service")
			return nil
		}
		return r.runDDNS(ctx)
	default:
		return fmt.Errorf("DIB_NETWORK_COMPONENT must be one of chrony, bind, kea-dhcp4, or dhcp-ddns")
	}
}

func dhcpEnabled() (bool, error) {
	value, configured := os.LookupEnv("DIB_DHCP_ENABLED")
	return parseDHCPEnabled(value, configured)
}

func dhcpHAEnabled() (bool, error) {
	value, configured := os.LookupEnv("DIB_DHCP_HA_ENABLED")
	if !configured {
		return false, nil
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("DIB_DHCP_HA_ENABLED must be a boolean: %w", err)
	}
	return enabled, nil
}

func parseDHCPEnabled(value string, configured bool) (bool, error) {
	if !configured {
		return true, nil
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("DIB_DHCP_ENABLED must be a boolean: %w", err)
	}
	return enabled, nil
}

func (r *Runner) Health(ctx context.Context) error {
	return wait.Until(ctx, "BIND", 5, time.Second, func() error {
		connection, err := net.DialTimeout("tcp", "127.0.0.1:5353", time.Second)
		if err != nil {
			return err
		}
		return connection.Close()
	})
}

func run(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	return nil
}

func environment(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("environment variable %s is not set", key)
	}
	return value, nil
}

func updateKeaDHCPPool(path, pool string) error {
	if pool == "" {
		return fmt.Errorf("DIB_DHCP_POOL is not set")
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Kea DHCP configuration: %w", err)
	}

	var configuration map[string]any
	if err := json.Unmarshal(contents, &configuration); err != nil {
		return fmt.Errorf("parse Kea DHCP configuration: %w", err)
	}
	dhcp4, ok := configuration["Dhcp4"].(map[string]any)
	if !ok {
		return fmt.Errorf("Kea DHCP configuration does not contain a Dhcp4 object")
	}
	subnets, ok := dhcp4["subnet4"].([]any)
	if !ok {
		return fmt.Errorf("Kea DHCP configuration does not contain subnet4 entries")
	}

	for _, entry := range subnets {
		subnet, ok := entry.(map[string]any)
		if !ok || subnet["id"] != float64(1) {
			continue
		}
		pools, ok := subnet["pools"].([]any)
		if !ok || len(pools) == 0 {
			return fmt.Errorf("Kea DHCP subnet ID 1 does not contain a pool")
		}
		managedPool, ok := pools[0].(map[string]any)
		if !ok {
			return fmt.Errorf("Kea DHCP subnet ID 1 has an invalid pool")
		}
		managedPool["pool"] = pool
		return writeJSONAtomically(path, configuration)
	}

	return fmt.Errorf("Kea DHCP configuration does not contain subnet ID 1")
}

func configureChrony() error {
	const path = "/etc/chrony/chrony.conf"
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat Chrony configuration: %w", err)
	}
	subnet, err := environment("SUBNET")
	if err != nil {
		return err
	}
	ip, err := environment("IP")
	if err != nil {
		return err
	}
	configuration := strings.Join([]string{
		"pool pool.ntp.org iburst",
		"driftfile /var/lib/chrony/chrony.drift",
		"makestep 1.0 3",
		"rtcsync",
		"local stratum 10",
		"allow " + subnet,
		"bindaddress " + ip,
		"bindcmdaddress 127.0.0.1",
		"ntpsigndsocket /var/lib/samba/ntp_signd",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(configuration), 0o644); err != nil {
		return fmt.Errorf("write Chrony configuration: %w", err)
	}
	return nil
}

func configureBind(ctx context.Context) error {
	if err := run(ctx, "chown", "root:bind", "/var/cache/bind"); err != nil {
		return err
	}
	if err := run(ctx, "chmod", "775", "/var/cache/bind"); err != nil {
		return err
	}
	subnet, err := environment("SUBNET")
	if err != nil {
		return err
	}
	ip, err := environment("IP")
	if err != nil {
		return err
	}
	dnsDomain, err := environment("DNS_DOMAIN")
	if err != nil {
		return err
	}
	reverseZone, err := environment("REVERSE_ZONE")
	if err != nil {
		return err
	}
	forwarders, err := environment("DIB_DNS_FORWARDERS")
	if err != nil {
		return err
	}
	files := map[string]string{
		"/etc/bind/named.conf":            "include \"/etc/bind/named.conf.options\";\ninclude \"/etc/bind/named.conf.local\";\ninclude \"/etc/bind/named.conf.cluster\";\ninclude \"/etc/bind/named.conf.root-hints\";\ninclude \"/etc/bind/rndc.key\";\n",
		"/etc/bind/named.conf.options":    "options {\n    directory \"/var/cache/bind\";\n    pid-file \"/run/named/named.pid\";\n\n    allow-query { 127.0.0.1; " + subnet + "; };\n    allow-update { none; };\n    allow-recursion { 127.0.0.1; " + subnet + "; };\n    allow-transfer { none; };\n    forwarders { " + forwarders + " };\n    listen-on port 5353 { " + ip + "; };\n    listen-on-v6 port 5353 { none; };\n    include \"/etc/bind/named.conf.cluster-options\";\n};\n\nstatistics-channels {\n    inet 127.0.0.1 port 8053 allow { 127.0.0.1; };\n};\n",
		"/etc/bind/named.conf.local":      "zone \"" + dnsDomain + "\" {\n    type forward;\n    forward only;\n    forwarders { 127.0.0.1; };\n};\n\nzone \"" + reverseZone + "\" {\n    type forward;\n    forward only;\n    forwarders { 127.0.0.1; };\n};\n",
		"/etc/bind/named.conf.root-hints": "zone \".\" {\n    type hint;\n    file \"/usr/share/dns/root.hints\";\n};\n",
	}
	for path, contents := range files {
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			return fmt.Errorf("write BIND configuration %s: %w", path, err)
		}
	}
	if err := writeClusterForwarding(os.Getenv("DIB_CLUSTER_DOMAIN"), os.Getenv("DIB_UPSTREAM_NAMESERVERS")); err != nil {
		return err
	}
	if _, err := os.Stat("/etc/bind/rndc.key"); os.IsNotExist(err) {
		if err := run(ctx, "rndc-confgen", "-a", "-A", "hmac-sha256", "-c", "/etc/bind/rndc.key"); err != nil {
			return err
		}
		if err := run(ctx, "chown", "root:bind", "/etc/bind/rndc.key"); err != nil {
			return err
		}
		return run(ctx, "chmod", "640", "/etc/bind/rndc.key")
	}
	return nil
}

func configureKea(ctx context.Context) error {
	if err := run(ctx, "chown", "root:_kea", "/var/lib/kea"); err != nil {
		return err
	}
	if err := run(ctx, "chmod", "775", "/var/lib/kea"); err != nil {
		return err
	}
	if err := repairKeaLogPermissions("/var/log/kea"); err != nil {
		return err
	}
	values := map[string]string{}
	for _, key := range []string{"DIB_INTERFACE", "IP", "GATEWAY", "DNS_DOMAIN", "SUBNET", "DIB_DHCP_POOL", "DIB_CONTROLLER_MODE"} {
		value, err := environment(key)
		if err != nil {
			return err
		}
		values[key] = value
	}
	haSettings, err := dhcpHASettingsFromEnvironment(values["DIB_CONTROLLER_MODE"], values["IP"])
	if err != nil {
		return err
	}
	dnsServers, err := dhcpDNSServers(values["IP"], haSettings)
	if err != nil {
		return err
	}
	dhcp4 := buildKeaDHCP4Configuration(values["DIB_INTERFACE"], values["IP"], values["GATEWAY"], values["DNS_DOMAIN"], values["SUBNET"], values["DIB_DHCP_POOL"], haSettings)
	setKeaDNSServers(dhcp4, dnsServers)
	const path = "/etc/kea/kea-dhcp4.conf"
	if err := writeJSONIfMissing(path, dhcp4); err != nil {
		return err
	}
	changed, err := reconcileKeaDHCP4Configuration(path, dhcp4, haSettings)
	if err != nil {
		return err
	}
	if changed {
		platformlog.New("network-core").Infof("reconciled DIB-managed settings in %s; the previous file was saved as %s.dib-previous", path, path)
	}
	return validateKeaDHCPHAConfiguration(path, haSettings)
}

// repairKeaLogPermissions undoes the recursive chmod 664 applied by earlier
// releases, which left the log directory untraversable for _kea. _kea can
// write to the directory, so the walk is confined to it and skips symlinks.
func repairKeaLogPermissions(directory string) error {
	directoryMode := os.FileMode(0o775) | os.ModeSetgid
	// The directory itself sits in root-owned /var/log; repair it first so
	// the confined walk can traverse it.
	if err := os.Chmod(directory, directoryMode); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("set Kea log permissions on %s: %w", directory, err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("open Kea log directory: %w", err)
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		mode := os.FileMode(0o664)
		if entry.IsDir() {
			mode = directoryMode
		}
		if err := root.Chmod(path, mode); err != nil {
			return fmt.Errorf("set Kea log permissions on %s: %w", filepath.Join(directory, path), err)
		}
		return nil
	})
}

// dhcpDNSServers returns the resolvers advertised to DHCP clients. Without an
// explicit DIB_DHCP_DNS_SERVERS list, an HA pair advertises the local controller
// first and its partner second so clients survive the loss of either one.
func dhcpDNSServers(localAddress string, ha dhcpHASettings) ([]string, error) {
	configured := strings.FieldsFunc(os.Getenv("DIB_DHCP_DNS_SERVERS"), func(r rune) bool {
		return r == ',' || r == ' '
	})
	if len(configured) > 0 {
		for _, server := range configured {
			if net.ParseIP(server).To4() == nil {
				return nil, fmt.Errorf("DIB_DHCP_DNS_SERVERS must contain IPv4 addresses, got %q", server)
			}
		}
		return configured, nil
	}
	servers := []string{localAddress}
	if ha.enabled {
		for _, peer := range []string{ha.primaryAddress, ha.standbyAddress} {
			if peer != localAddress {
				servers = append(servers, peer)
			}
		}
	}
	return servers, nil
}

func setKeaDNSServers(configuration map[string]any, servers []string) {
	dhcp4 := configuration["Dhcp4"].(map[string]any)
	for _, option := range dhcp4["option-data"].([]map[string]any) {
		if option["name"] == "domain-name-servers" {
			option["data"] = strings.Join(servers, ", ")
		}
	}
}

// keaManagedGlobals are the DHCPv4 globals DIB owns in persisted configurations.
var keaManagedGlobals = []string{
	"dhcp-ddns", "ddns-send-updates", "ddns-qualifying-suffix", "ddns-override-client-update",
	"ddns-override-no-update", "ddns-replace-client-name", "ddns-update-on-renew",
	"hostname-char-set", "hostname-char-replacement",
}

// reconcileKeaDHCP4Configuration brings DIB-managed settings in a persisted
// configuration up to date while preserving operator additions. Earlier
// releases only wrote them at first provisioning, so upgrades and peer address
// changes never reached existing volumes.
func reconcileKeaDHCP4Configuration(path string, desired map[string]any, ha dhcpHASettings) (bool, error) {
	original, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read Kea DHCPv4 configuration: %w", err)
	}
	var configuration map[string]any
	if err := json.Unmarshal(original, &configuration); err != nil {
		return false, fmt.Errorf("parse Kea DHCPv4 configuration: %w", err)
	}
	dhcp4, ok := configuration["Dhcp4"].(map[string]any)
	if !ok {
		return false, fmt.Errorf("Kea DHCPv4 configuration has no Dhcp4 object")
	}
	var generic map[string]any
	encoded, err := json.Marshal(desired)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(encoded, &generic); err != nil {
		return false, err
	}
	want := generic["Dhcp4"].(map[string]any)
	for _, key := range keaManagedGlobals {
		dhcp4[key] = want[key]
	}
	reconcileKeaOption(dhcp4, want, "domain-name-servers")
	if err := reconcileKeaHA(dhcp4, want, ha); err != nil {
		return false, err
	}

	before, err := json.Marshal(mustDecode(original))
	if err != nil {
		return false, err
	}
	after, err := json.Marshal(configuration)
	if err != nil {
		return false, err
	}
	if string(before) == string(after) {
		return false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path+".dib-previous", original, info.Mode().Perm()); err != nil {
		return false, fmt.Errorf("save previous Kea DHCPv4 configuration: %w", err)
	}
	return true, writeJSONAtomically(path, configuration)
}

func mustDecode(contents []byte) any {
	var value any
	_ = json.Unmarshal(contents, &value)
	return value
}

func reconcileKeaOption(dhcp4, want map[string]any, name string) {
	var desired any
	for _, option := range want["option-data"].([]any) {
		if option.(map[string]any)["name"] == name {
			desired = option
		}
	}
	options, _ := dhcp4["option-data"].([]any)
	for index, option := range options {
		if entry, _ := option.(map[string]any); entry["name"] == name {
			options[index] = desired
			return
		}
	}
	dhcp4["option-data"] = append(options, desired)
}

// reconcileKeaHA rewrites the HA relationship and the primary scope pool class
// from the current settings. Enabling or disabling HA, or reusing a volume for
// the other role, changes lease authority and is left to validation to refuse.
func reconcileKeaHA(dhcp4, want map[string]any, ha dhcpHASettings) error {
	if !ha.enabled {
		return nil
	}
	var desiredParameters any
	for _, rawHook := range want["hooks-libraries"].([]any) {
		if hook := rawHook.(map[string]any); hook["library"] == "libdhcp_ha.so" {
			desiredParameters = hook["parameters"]
		}
	}
	hooks, _ := dhcp4["hooks-libraries"].([]any)
	var haHook map[string]any
	for _, rawHook := range hooks {
		if hook, _ := rawHook.(map[string]any); hook["library"] == "libdhcp_ha.so" {
			haHook = hook
		}
	}
	if haHook == nil {
		return nil
	}
	localName, partnerName := ha.primaryName, ha.standbyName
	if ha.controllerMode == "secondary" {
		localName, partnerName = partnerName, localName
	}
	parameters, _ := haHook["parameters"].(map[string]any)
	relationships, _ := parameters["high-availability"].([]any)
	if len(relationships) == 1 {
		current, _ := relationships[0].(map[string]any)
		if current["this-server-name"] == partnerName {
			return fmt.Errorf("persisted Kea DHCPv4 HA identity %q belongs to the %s's partner; refusing to reuse this volume with the opposite role", partnerName, localName)
		}
	}
	haHook["parameters"] = desiredParameters

	class := keaDHCPHAPrimaryClass(ha.primaryName)
	subnets, _ := dhcp4["subnet4"].([]any)
	for _, rawSubnet := range subnets {
		subnet, _ := rawSubnet.(map[string]any)
		if subnet["id"] != float64(1) {
			continue
		}
		pools, _ := subnet["pools"].([]any)
		for _, rawPool := range pools {
			pool, _ := rawPool.(map[string]any)
			classes, _ := pool["client-classes"].([]any)
			for index, existing := range classes {
				if name, _ := existing.(string); strings.HasPrefix(name, "HA_") {
					classes[index] = class
				}
			}
		}
	}
	return nil
}

type dhcpHASettings struct {
	enabled        bool
	controllerMode string
	primaryAddress string
	standbyAddress string
	primaryName    string
	standbyName    string
}

func dhcpHASettingsFromEnvironment(controllerMode, localAddress string) (dhcpHASettings, error) {
	enabled, err := dhcpHAEnabled()
	if err != nil {
		return dhcpHASettings{}, err
	}
	if !enabled {
		return dhcpHASettings{}, nil
	}
	if controllerMode != "primary" && controllerMode != "secondary" {
		return dhcpHASettings{}, fmt.Errorf("DIB_CONTROLLER_MODE must be primary or secondary when DHCP HA is enabled")
	}
	primaryAddress, err := requiredIPv4Environment("DIB_DHCP_HA_PRIMARY_ADDRESS")
	if err != nil {
		return dhcpHASettings{}, err
	}
	standbyAddress, err := requiredIPv4Environment("DIB_DHCP_HA_STANDBY_ADDRESS")
	if err != nil {
		return dhcpHASettings{}, err
	}
	if primaryAddress == standbyAddress {
		return dhcpHASettings{}, fmt.Errorf("DIB_DHCP_HA_PRIMARY_ADDRESS and DIB_DHCP_HA_STANDBY_ADDRESS must differ")
	}
	wantLocalAddress := primaryAddress
	if controllerMode == "secondary" {
		wantLocalAddress = standbyAddress
	}
	if localAddress != wantLocalAddress {
		return dhcpHASettings{}, fmt.Errorf("local controller address %q does not match the DHCP HA %s address %q", localAddress, controllerMode, wantLocalAddress)
	}
	primaryName := environmentOrDefault("DIB_DHCP_HA_PRIMARY_NAME", "dib-primary")
	standbyName := environmentOrDefault("DIB_DHCP_HA_STANDBY_NAME", "dib-secondary")
	if primaryName == standbyName {
		return dhcpHASettings{}, fmt.Errorf("DIB_DHCP_HA_PRIMARY_NAME and DIB_DHCP_HA_STANDBY_NAME must differ")
	}
	return dhcpHASettings{
		enabled:        true,
		controllerMode: controllerMode,
		primaryAddress: primaryAddress,
		standbyAddress: standbyAddress,
		primaryName:    primaryName,
		standbyName:    standbyName,
	}, nil
}

func requiredIPv4Environment(name string) (string, error) {
	value, err := environment(name)
	if err != nil {
		return "", err
	}
	if net.ParseIP(value).To4() == nil {
		return "", fmt.Errorf("%s must be an IPv4 address, got %q", name, value)
	}
	return value, nil
}

func environmentOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func keaDDNSAccount(hostname string) string {
	return "kea-" + hostname
}

func configuredDHCPHAPrimaryName() string {
	return environmentOrDefault("DIB_DHCP_HA_PRIMARY_NAME", "dib-primary")
}

func keaDHCPHAPrimaryClass(primaryName string) string {
	return "HA_" + primaryName
}

func validateKeaDHCPHAConfiguration(path string, expected dhcpHASettings) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Kea DHCPv4 configuration: %w", err)
	}
	var configuration map[string]any
	if err := json.Unmarshal(contents, &configuration); err != nil {
		return fmt.Errorf("parse Kea DHCPv4 configuration: %w", err)
	}
	dhcp4, ok := configuration["Dhcp4"].(map[string]any)
	if !ok {
		return fmt.Errorf("Kea DHCPv4 configuration has no Dhcp4 object")
	}
	hooks, _ := dhcp4["hooks-libraries"].([]any)
	var ha map[string]any
	for _, rawHook := range hooks {
		hook, _ := rawHook.(map[string]any)
		if hook["library"] != "libdhcp_ha.so" {
			continue
		}
		parameters, _ := hook["parameters"].(map[string]any)
		relationships, _ := parameters["high-availability"].([]any)
		if len(relationships) == 1 {
			ha, _ = relationships[0].(map[string]any)
		}
	}
	if !expected.enabled {
		if ha != nil {
			return fmt.Errorf("persisted Kea DHCPv4 configuration still enables HA while DIB_DHCP_HA_ENABLED is false")
		}
		return nil
	}
	if ha == nil {
		return fmt.Errorf("persisted Kea DHCPv4 configuration does not contain the configured DHCP HA relationship; back up and reconcile /etc/kea/kea-dhcp4.conf before enabling DHCP HA")
	}
	expectedName := expected.primaryName
	if expected.controllerMode == "secondary" {
		expectedName = expected.standbyName
	}
	if ha["this-server-name"] != expectedName || ha["mode"] != "hot-standby" {
		return fmt.Errorf("persisted Kea DHCPv4 HA identity/mode does not match the configured %s controller", expected.controllerMode)
	}
	if ha["max-unacked-clients"] != float64(0) {
		return fmt.Errorf("persisted Kea DHCPv4 HA configuration must detect partner loss without waiting for DHCP clients")
	}
	expectedClass := keaDHCPHAPrimaryClass(expected.primaryName)
	subnets, _ := dhcp4["subnet4"].([]any)
	hasPrimaryPool := false
	for _, rawSubnet := range subnets {
		subnet, _ := rawSubnet.(map[string]any)
		if subnet["id"] != float64(1) {
			continue
		}
		pools, _ := subnet["pools"].([]any)
		for _, rawPool := range pools {
			pool, _ := rawPool.(map[string]any)
			classes, _ := pool["client-classes"].([]any)
			for _, class := range classes {
				if class == expectedClass {
					hasPrimaryPool = true
				}
			}
		}
	}
	if !hasPrimaryPool {
		return fmt.Errorf("persisted Kea DHCPv4 HA configuration does not assign the primary scope class %q to a pool", expectedClass)
	}
	peers, _ := ha["peers"].([]any)
	if len(peers) != 2 {
		return fmt.Errorf("persisted Kea DHCPv4 HA configuration must contain exactly two peers")
	}
	expectedPeers := []struct {
		name, address, role string
	}{
		{expected.primaryName, expected.primaryAddress, "primary"},
		{expected.standbyName, expected.standbyAddress, "standby"},
	}
	for index, want := range expectedPeers {
		peer, _ := peers[index].(map[string]any)
		if peer["name"] != want.name ||
			peer["url"] != fmt.Sprintf("https://%s:8005/", want.address) ||
			peer["role"] != want.role ||
			peer["auto-failover"] != false ||
			peer["require-client-certs"] != true {
			return fmt.Errorf("persisted Kea DHCPv4 HA peer %d does not match the required pinned mutual-TLS manual-failover configuration", index+1)
		}
	}
	return nil
}

func buildKeaDHCP4Configuration(interfaceName, ip, gateway, dnsDomain, subnet, pool string, ha dhcpHASettings) map[string]any {
	poolConfig := map[string]any{"pool": pool}
	if ha.enabled {
		poolConfig["client-classes"] = []string{keaDHCPHAPrimaryClass(ha.primaryName)}
	}
	dhcp4 := map[string]any{
		"control-socket":    map[string]any{"socket-type": "unix", "socket-name": "kea4-ctrl-socket"},
		"interfaces-config": map[string]any{"interfaces": []string{interfaceName}},
		"lease-database":    map[string]any{"type": "memfile"},
		"option-data": []map[string]any{
			{"name": "domain-name-servers", "code": 6, "data": ip},
			{"name": "routers", "code": 3, "data": gateway},
			{"name": "domain-name", "code": 15, "data": dnsDomain},
			{"name": "domain-search", "code": 119, "data": dnsDomain},
		},
		"subnet4": []map[string]any{{"id": 1, "subnet": subnet, "interface": interfaceName, "pools": []map[string]any{poolConfig}}},
		"dhcp-ddns": map[string]any{
			"enable-updates": true,
			"server-ip":      "127.0.0.1",
			"server-port":    ddnsListenPort,
			"ncr-protocol":   "UDP",
			"ncr-format":     "JSON",
		},
		// Clients that update their own A record (Windows domain members) keep
		// doing so; DIB registers the rest and refreshes on renewal.
		"ddns-send-updates":           true,
		"ddns-qualifying-suffix":      dnsDomain + ".",
		"ddns-override-client-update": false,
		"ddns-override-no-update":     false,
		"ddns-replace-client-name":    "never",
		"ddns-update-on-renew":        true,
		"hostname-char-set":           "[^A-Za-z0-9.-]",
		"hostname-char-replacement":   "",
		"loggers":                     []map[string]any{{"name": "kea-dhcp4", "output_options": []map[string]string{{"output": "/var/log/kea/kea-dhcp4.log"}}, "severity": "INFO"}},
	}
	if ha.enabled {
		localName := ha.primaryName
		if ha.controllerMode == "secondary" {
			localName = ha.standbyName
		}
		peerConfig := func(name, address, role string) map[string]any {
			return map[string]any{
				"name":                 name,
				"url":                  fmt.Sprintf("https://%s:8005/", address),
				"role":                 role,
				"auto-failover":        false,
				"require-client-certs": true,
			}
		}
		haConfig := map[string]any{
			"this-server-name":    localName,
			"mode":                "hot-standby",
			"max-unacked-clients": 0,
			"trust-anchor":        "/run/kea-ha-tls/ca.pem",
			"cert-file":           "/run/kea-ha-tls/cert.pem",
			"key-file":            "/run/kea-ha-tls/key.pem",
			"multi-threading": map[string]any{
				"enable-multi-threading":  true,
				"http-dedicated-listener": true,
			},
			"peers": []map[string]any{
				peerConfig(ha.primaryName, ha.primaryAddress, "primary"),
				peerConfig(ha.standbyName, ha.standbyAddress, "standby"),
			},
		}
		dhcp4["hooks-libraries"] = []map[string]any{
			{"library": "libdhcp_lease_cmds.so"},
			{"library": "libdhcp_ha.so", "parameters": map[string]any{"high-availability": []map[string]any{haConfig}}},
		}
	}
	return map[string]any{"Dhcp4": dhcp4}
}

func writeJSONIfMissing(path string, value any) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	encoded, err := json.MarshalIndent(value, "", "    ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(path, encoded, 0o644)
}

func writeJSONAtomically(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "    ")
	if err != nil {
		return fmt.Errorf("encode Kea DHCP configuration: %w", err)
	}
	encoded = append(encoded, '\n')

	return writeAtomically(path, encoded)
}

func updateBindForwarders(path, forwarders string) error {
	if forwarders == "" {
		return fmt.Errorf("DIB_DNS_FORWARDERS is not set")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read BIND configuration: %w", err)
	}

	directive := regexp.MustCompile(`(?s)(forwarders\s*\{)[^}]*?(\}\s*;)`)
	matches := directive.FindAllIndex(contents, -1)
	if len(matches) != 1 {
		return fmt.Errorf("BIND configuration must contain exactly one DIB-managed forwarders directive")
	}
	updated := directive.ReplaceAll(contents, []byte("${1} "+forwarders+" ${2}"))
	return writeAtomically(path, updated)
}

// writeClusterForwarding points a platform domain such as cluster.local at the
// resolver the container was started with, which /etc/resolv.conf no longer names.
// Those zones are unsigned, so DNSSEC validation has to be skipped for them.
func writeClusterForwarding(domain, nameservers string) error {
	zone, options := "", ""
	if servers := strings.Fields(nameservers); domain != "" && len(servers) > 0 {
		var forwarders strings.Builder
		for _, server := range servers {
			forwarders.WriteString(" " + server + ";")
		}
		zone = fmt.Sprintf("zone %q {\n    type forward;\n    forward only;\n    forwarders {%s };\n};\n", domain, forwarders.String())
		options = fmt.Sprintf("validate-except { %q; };\n", domain)
	}
	for path, contents := range map[string]string{
		"/etc/bind/named.conf.cluster":         zone,
		"/etc/bind/named.conf.cluster-options": options,
	} {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			return fmt.Errorf("write BIND cluster configuration %s: %w", path, err)
		}
	}
	return nil
}

func writeAtomically(path string, contents []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat configuration file: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".kea-dhcp4-*.json")
	if err != nil {
		return fmt.Errorf("create temporary configuration file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary configuration file: %w", err)
	}
	if err := temporary.Chmod(info.Mode()); err != nil {
		temporary.Close()
		return fmt.Errorf("set temporary configuration file mode: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary configuration file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace configuration file: %w", err)
	}
	return nil
}
