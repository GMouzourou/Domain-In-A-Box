package networkcore

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	platformlog "github.com/GMouzourou/domain-in-a-box/internal/platform/log"
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
	if err := configureChrony(); err != nil {
		return err
	}
	if err := configureBind(ctx); err != nil {
		return err
	}
	if err := configureKea(ctx); err != nil {
		return err
	}
	if os.Getenv("INIT_DOMAIN") == "FALSE" {
		if err := updateBindForwarders("/etc/bind/named.conf.options", os.Getenv("DIB_DNS_FORWARDERS")); err != nil {
			return err
		}
		return updateKeaDHCPPool("/etc/kea/kea-dhcp4.conf", os.Getenv("DIB_DHCP_POOL"))
	}
	return nil
}

func (r *Runner) Bootstrap(ctx context.Context) error {
	realm, err := environment("DIB_REALM")
	if err != nil {
		return err
	}
	if os.Getenv("INIT_DOMAIN") == "TRUE" {
		for _, command := range [][]string{
			{"samba-tool", "user", "create", "keaddns", "--description=Kea DHCP DDNS GSS-TSIG service account", "--random-password"},
			{"samba-tool", "user", "setexpiry", "keaddns", "--noexpiry"},
			{"samba-tool", "group", "addmembers", "DnsAdmins", "keaddns"},
			{"samba-tool", "domain", "exportkeytab", "/var/lib/kea/keaddns.keytab", "--principal=keaddns@" + realm},
		} {
			if err := run(ctx, command[0], command[1:]...); err != nil {
				return err
			}
		}
	}

	if _, err := os.Stat("/var/lib/kea/keaddns.keytab"); err != nil {
		return fmt.Errorf("Kea DDNS keytab is missing: %w", err)
	}
	if err := run(ctx, "chown", "root:_kea", "/var/lib/kea/keaddns.keytab"); err != nil {
		return err
	}
	if err := run(ctx, "chmod", "660", "/var/lib/kea/keaddns.keytab"); err != nil {
		return err
	}
	if err := os.WriteFile("/run/kea/keaddns.ccache", nil, 0o660); err != nil {
		return fmt.Errorf("create Kea DDNS credential cache: %w", err)
	}
	if err := run(ctx, "chown", "root:_kea", "/run/kea/keaddns.ccache"); err != nil {
		return err
	}
	if err := runWithEnvironment(ctx, []string{"KRB5CCNAME=FILE:/run/kea/keaddns.ccache"}, "kinit", "-kt", "/var/lib/kea/keaddns.keytab", "keaddns@"+realm); err != nil {
		return err
	}

	cronLine := "0 */4 * * * KRB5CCNAME=FILE:/run/kea/keaddns.ccache /usr/bin/kinit -kt /var/lib/kea/keaddns.keytab keaddns@" + realm
	return run(ctx, "/bin/sh", "-c", "(crontab -u _kea -l 2>/dev/null; echo \""+cronLine+"\") | crontab -u _kea -")
}

func (r *Runner) Validate(ctx context.Context) error {
	for _, command := range [][]string{
		{"chronyd", "-p", "-f", "/etc/chrony/chrony.conf"},
		{"named-checkconf", "/etc/bind/named.conf"},
		{"/usr/sbin/kea-dhcp4", "-t", "/etc/kea/kea-dhcp4.conf"},
		{"/usr/sbin/kea-dhcp-ddns", "-t", "/etc/kea/kea-dhcp-ddns.conf"},
	} {
		if err := run(ctx, command[0], command[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) Run(ctx context.Context) error {
	switch os.Getenv("DIB_NETWORK_COMPONENT") {
	case "chrony":
		return run(ctx, "/usr/sbin/chronyd", "-n", "-x", "-f", "/etc/chrony/chrony.conf")
	case "bind":
		return run(ctx, "/usr/sbin/named", "-f", "-c", "/etc/bind/named.conf")
	case "kea-dhcp4":
		return run(ctx, "/usr/sbin/kea-dhcp4", "-c", "/etc/kea/kea-dhcp4.conf")
	case "kea-dhcp-ddns":
		return run(ctx, "/usr/sbin/kea-dhcp-ddns", "-c", "/etc/kea/kea-dhcp-ddns.conf")
	default:
		return fmt.Errorf("DIB_NETWORK_COMPONENT must be one of chrony, bind, kea-dhcp4, or kea-dhcp-ddns")
	}
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

func runWithEnvironment(ctx context.Context, environment []string, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), environment...)
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
		"/etc/bind/named.conf":            "include \"/etc/bind/named.conf.options\";\ninclude \"/etc/bind/named.conf.local\";\ninclude \"/etc/bind/named.conf.root-hints\";\ninclude \"/etc/bind/rndc.key\";\n",
		"/etc/bind/named.conf.options":    "options {\n    directory \"/var/cache/bind\";\n    pid-file \"/run/named/named.pid\";\n\n    allow-query { 127.0.0.1; " + subnet + "; };\n    allow-update { none; };\n    allow-recursion { 127.0.0.1; " + subnet + "; };\n    allow-transfer { none; };\n    forwarders { " + forwarders + " };\n    listen-on port 5353 { " + ip + "; };\n    listen-on-v6 port 5353 { none; };\n};\n\nstatistics-channels {\n    inet 127.0.0.1 port 8053 allow { 127.0.0.1; };\n};\n",
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
	interfaceName, err := environment("DIB_INTERFACE")
	if err != nil {
		return err
	}
	ip, err := environment("IP")
	if err != nil {
		return err
	}
	gateway, err := environment("GATEWAY")
	if err != nil {
		return err
	}
	dnsDomain, err := environment("DNS_DOMAIN")
	if err != nil {
		return err
	}
	subnet, err := environment("SUBNET")
	if err != nil {
		return err
	}
	pool, err := environment("DIB_DHCP_POOL")
	if err != nil {
		return err
	}
	realm, err := environment("DIB_REALM")
	if err != nil {
		return err
	}
	hostname, err := environment("CONTAINER_HOSTNAME")
	if err != nil {
		return err
	}
	reverseZone, err := environment("REVERSE_ZONE")
	if err != nil {
		return err
	}

	dhcp4 := map[string]any{"Dhcp4": map[string]any{
		"control-socket":    map[string]any{"socket-type": "unix", "socket-name": "/run/kea/kea4-ctrl-socket"},
		"interfaces-config": map[string]any{"interfaces": []string{interfaceName}},
		"lease-database":    map[string]any{"type": "memfile"},
		"option-data": []map[string]any{
			{"name": "domain-name-servers", "code": 6, "data": ip},
			{"name": "routers", "code": 3, "data": gateway},
			{"name": "domain-name", "code": 15, "data": dnsDomain},
			{"name": "domain-search", "code": 119, "data": dnsDomain},
		},
		"subnet4":   []map[string]any{{"id": 1, "subnet": subnet, "pools": []map[string]any{{"pool": pool}}}},
		"dhcp-ddns": map[string]any{"enable-updates": true},
		"loggers":   []map[string]any{{"name": "kea-dhcp4", "output_options": []map[string]string{{"output": "/var/log/kea/kea-dhcp4.log"}}, "severity": "INFO"}},
	}}
	ddns := map[string]any{"DhcpDdns": map[string]any{
		"control-socket":  map[string]any{"socket-type": "unix", "socket-name": "/run/kea/kea-ddns-ctrl-socket"},
		"forward-ddns":    map[string]any{"ddns-domains": []map[string]any{{"name": dnsDomain + ".", "dns-servers": []map[string]string{{"ip-address": "127.0.0.1"}}}}},
		"reverse-ddns":    map[string]any{"ddns-domains": []map[string]any{{"name": reverseZone + ".", "dns-servers": []map[string]string{{"ip-address": "127.0.0.1"}}}}},
		"hooks-libraries": []map[string]any{{"library": "libddns_gss_tsig.so", "parameters": map[string]any{"server-principal": "DNS/" + hostname + "." + dnsDomain + "@" + realm, "client-principal": "keaddns@" + realm, "credentials-cache": "FILE:/run/kea/keaddns.ccache", "servers": []map[string]string{{"id": "samba-dc", "ip-address": "127.0.0.1"}}}}},
		"loggers":         []map[string]any{{"name": "kea-dhcp-ddns", "output_options": []map[string]string{{"output": "/var/log/kea/kea-dhcp-ddns.log"}}, "severity": "INFO"}},
	}}
	if err := writeJSONIfMissing("/etc/kea/kea-dhcp4.conf", dhcp4); err != nil {
		return err
	}
	return writeJSONIfMissing("/etc/kea/kea-dhcp-ddns.conf", ddns)
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
