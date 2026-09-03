package identitycore

import (
	"context"
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
	return &Runner{log: platformlog.New("identity-core")}
}

func (r *Runner) Name() string { return "identity-core" }

func (r *Runner) Configure(ctx context.Context) error {
	if os.Getenv("INIT_DOMAIN") == "TRUE" {
		if err := provision(ctx); err != nil {
			return err
		}
	}
	if err := configureKerberos(ctx); err != nil {
		return err
	}
	if err := installTLSAssets(ctx); err != nil {
		return err
	}
	if os.Getenv("INIT_DOMAIN") == "FALSE" {
		if os.Getenv("DIB_SYNC_DOMAIN_ADMIN_PASSWORD_ON_RESTART") == "true" {
			if password := os.Getenv("DIB_DOMAIN_ADMIN_PASSWORD"); password != "" {
				if err := run(ctx, "samba-tool", "user", "setpassword", "Administrator", "--newpassword="+password); err != nil {
					return err
				}
			}
		}
		return updateSambaMetrics("/etc/samba/smb.conf", os.Getenv("DIB_SAMBA_METRICS_ENABLED"))
	}
	return nil
}

func (r *Runner) Bootstrap(ctx context.Context) error {
	if os.Getenv("INIT_DOMAIN") != "TRUE" {
		return nil
	}
	reverseZone, err := environment("REVERSE_ZONE")
	if err != nil {
		return err
	}
	dnsDomain, err := environment("DNS_DOMAIN")
	if err != nil {
		return err
	}
	hostname, err := environment("CONTAINER_HOSTNAME")
	if err != nil {
		return err
	}
	address, err := environment("IP")
	if err != nil {
		return err
	}
	password, err := environment("DIB_DOMAIN_ADMIN_PASSWORD")
	if err != nil {
		return err
	}
	parts := strings.Split(address, ".")
	if len(parts) != 4 {
		return fmt.Errorf("IP must be an IPv4 address, got %q", address)
	}
	credentials := "Administrator%" + password
	if err := run(ctx, "samba-tool", "domain", "level", "raise", "--domain-level=2016", "--forest-level=2016"); err != nil {
		return err
	}
	if err := run(ctx, "samba-tool", "dns", "zonecreate", "127.0.0.1", reverseZone, "-U", credentials); err != nil {
		return err
	}
	if err := run(ctx, "samba-tool", "dns", "add", "127.0.0.1", reverseZone, parts[3], "PTR", hostname+"."+dnsDomain, "-U", credentials); err != nil {
		return err
	}
	if len(hostname) <= 15 {
		return nil
	}
	netbiosName, err := exec.CommandContext(ctx, "testparm", "-s", "--parameter-name=netbios name").Output()
	if err != nil {
		return fmt.Errorf("read Samba NetBIOS name: %w", err)
	}
	return run(ctx, "samba-tool", "dns", "add", "127.0.0.1", dnsDomain, hostname, "CNAME", strings.TrimSpace(string(netbiosName))+"."+dnsDomain, "-U", credentials)
}

func (r *Runner) Validate(ctx context.Context) error {
	return run(ctx, "testparm", "-s")
}

func (r *Runner) Run(ctx context.Context) error {
	return run(ctx, "/usr/sbin/samba", "-i")
}

func (r *Runner) Health(ctx context.Context) error {
	for _, address := range []string{"127.0.0.1:389", "127.0.0.1:88"} {
		if err := wait.Until(ctx, address, 5, time.Second, func() error {
			connection, err := net.DialTimeout("tcp", address, time.Second)
			if err != nil {
				return err
			}
			return connection.Close()
		}); err != nil {
			return err
		}
	}
	return nil
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

func provision(ctx context.Context) error {
	realm, err := environment("DIB_REALM")
	if err != nil {
		return err
	}
	domain, err := environment("DIB_DOMAIN")
	if err != nil {
		return err
	}
	password, err := environment("DIB_DOMAIN_ADMIN_PASSWORD")
	if err != nil {
		return err
	}
	hostname, err := environment("CONTAINER_HOSTNAME")
	if err != nil {
		return err
	}
	ip, err := environment("IP")
	if err != nil {
		return err
	}
	interfaceName, err := environment("DIB_INTERFACE")
	if err != nil {
		return err
	}
	metrics := os.Getenv("DIB_SAMBA_METRICS_ENABLED")
	if metrics == "" {
		metrics = "off"
	}
	return run(ctx, "samba-tool", "domain", "provision", "--realm="+realm, "--domain="+domain, "--server-role=dc", "--use-rfc2307", "--dns-backend=SAMBA_INTERNAL", "--adminpass="+password, "--host-name="+hostname, "--host-ip="+ip, "--option=bind interfaces only = yes", "--option=interfaces = lo "+interfaceName, "--option=dns forwarder = "+ip+":5353", "--option=rpc server dynamic port range = 49152-49252", "--option=ntp signd socket directory = /var/lib/samba/ntp_signd", "--option=ad dc functional level = 2016", "--option=log file = /var/log/samba/%m.log", "--option=max log size = 10000", "--option=smbd profiling level = "+metrics)
}

func configureKerberos(ctx context.Context) error {
	contents, err := os.ReadFile("/var/lib/samba/private/krb5.conf")
	if err != nil {
		return fmt.Errorf("read Samba Kerberos configuration: %w", err)
	}
	realm, err := environment("DIB_REALM")
	if err != nil {
		return err
	}
	ip, err := environment("IP")
	if err != nil {
		return err
	}
	configuration := regexp.MustCompile(`(?m)^\s*dns_lookup_kdc\s*=\s*true\s*$`).ReplaceAll(contents, []byte("\tdns_lookup_kdc = false"))
	realmLine := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(realm) + `\s*=.*$`)
	if !realmLine.Match(configuration) {
		return fmt.Errorf("Kerberos configuration does not contain realm %s", realm)
	}
	configuration = realmLine.ReplaceAll(configuration, []byte("${0}\n\tkdc = "+ip+"\n\tadmin_server = "+ip))
	if err := os.WriteFile("/etc/krb5.conf", configuration, 0o644); err != nil {
		return err
	}
	return run(ctx, "chown", "root:bind", "/etc/krb5.conf")
}

func installTLSAssets(ctx context.Context) error {
	paths := []string{"/certs/cert.pem", "/certs/key.pem", "/certs/ca.pem"}
	present := 0
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			present++
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if present == 0 {
		return nil
	}
	if present != len(paths) {
		return fmt.Errorf("custom Samba TLS requires non-empty cert.pem, key.pem, and ca.pem")
	}
	if err := os.MkdirAll("/var/lib/samba/private/tls", 0o755); err != nil {
		return err
	}
	for _, asset := range []struct {
		source, target string
		mode           os.FileMode
	}{{paths[0], "cert.pem", 0o644}, {paths[1], "key.pem", 0o600}, {paths[2], "ca.pem", 0o644}} {
		contents, err := os.ReadFile(asset.source)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join("/var/lib/samba/private/tls", asset.target), contents, asset.mode); err != nil {
			return err
		}
	}
	contents, err := os.ReadFile(paths[2])
	if err != nil {
		return err
	}
	if err := os.WriteFile("/usr/local/share/ca-certificates/domain-in-a-box-ldap-ca.crt", contents, 0o644); err != nil {
		return err
	}
	return run(ctx, "update-ca-certificates")
}

func updateSambaMetrics(path, value string) error {
	if value == "" {
		return fmt.Errorf("DIB_SAMBA_METRICS_ENABLED is not set")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Samba configuration: %w", err)
	}
	directive := regexp.MustCompile(`(?m)^\s*smbd profiling level\s*=.*$`)
	if len(directive.FindAllIndex(contents, -1)) != 1 {
		return fmt.Errorf("Samba configuration must contain exactly one DIB-managed smbd profiling level directive")
	}
	updated := directive.ReplaceAll(contents, []byte("smbd profiling level = "+value))
	return writeAtomically(path, updated)
}

func writeAtomically(path string, contents []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".dib-samba-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(info.Mode()); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
