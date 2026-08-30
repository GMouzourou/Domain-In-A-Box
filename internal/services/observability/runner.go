package observability

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	platformlog "github.com/GMouzourou/domain-in-a-box/internal/platform/log"
	"github.com/GMouzourou/domain-in-a-box/internal/platform/wait"
)

type Runner struct {
	log *platformlog.Logger
}

func NewRunner() *Runner {
	return &Runner{log: platformlog.New("observability")}
}

func (r *Runner) Name() string { return "observability" }

func (r *Runner) Configure(ctx context.Context) error {
	for _, path := range []string{"/usr/lib/stork-agent/hooks", "/usr/lib/stork-server/hooks"} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	if err := os.MkdirAll("/usr/share/stork/www/assets/authentication-methods", 0o775); err != nil {
		return err
	}
	if err := run(ctx, "chown", "root:stork-server", "/usr/share/stork/www/assets/authentication-methods"); err != nil {
		return err
	}
	if err := writeStorkServerEnvironment(); err != nil {
		return err
	}
	if err := writeStorkAgentEnvironment(); err != nil {
		return err
	}
	for _, group := range []string{"bind", "_kea"} {
		if err := run(ctx, "getent", "group", group); err == nil {
			if err := run(ctx, "usermod", "-aG", group, "stork-agent"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runner) Bootstrap(ctx context.Context) error {
	password, err := storkDatabasePassword()
	if err != nil {
		return err
	}
	if err := postgres(ctx, "SELECT 1 FROM pg_roles WHERE rolname='stork'", "CREATE ROLE \"stork\" LOGIN PASSWORD '"+strings.ReplaceAll(password, "'", "''")+"'"); err != nil {
		return err
	}
	if err := postgres(ctx, "SELECT 1 FROM pg_database WHERE datname='stork'", "CREATE DATABASE \"stork\" OWNER \"stork\""); err != nil {
		return err
	}
	if err := run(ctx, "su", "-s", "/bin/sh", "-c", "psql -v ON_ERROR_STOP=1 -d stork -c 'CREATE EXTENSION IF NOT EXISTS pgcrypto'", "postgres"); err != nil {
		return err
	}

	if os.Getenv("INIT_DOMAIN") == "TRUE" {
		ldapPassword, err := storkEnvironment("STORK_SERVER_HOOK_LDAP_BIND_PASSWORD")
		if err != nil {
			return err
		}
		if err := run(ctx, "samba-tool", "user", "create", "ldap-search-user", "--description=Read-only LDAP Bind User", ldapPassword); err != nil {
			return err
		}
		if err := run(ctx, "samba-tool", "user", "setexpiry", "ldap-search-user", "--noexpiry"); err != nil {
			return err
		}
	}

	if _, err := os.Stat("/var/lib/stork-agent/.stork-agent-registered"); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check Stork agent registration: %w", err)
	}

	address, err := storkEnvironment("STORK_REST_HOST")
	if err != nil {
		return err
	}
	port, err := storkEnvironment("STORK_REST_PORT")
	if err != nil {
		return err
	}
	if err := waitFor(ctx, "Stork server", net.JoinHostPort(address, port)); err != nil {
		return err
	}
	token, err := postgresValue(ctx, "SELECT * FROM secret", "stork")
	if err != nil {
		return err
	}
	parts := strings.Split(token, "|")
	serverToken := parts[len(parts)-1]
	if serverToken == "" {
		return fmt.Errorf("Stork server token is empty")
	}
	scheme := "http"
	if port == "443" {
		scheme = "https"
	}
	hostname, err := requiredEnvironment("CONTAINER_HOSTNAME")
	if err != nil {
		return err
	}
	dnsDomain, err := requiredEnvironment("DNS_DOMAIN")
	if err != nil {
		return err
	}
	if err := run(ctx, "stork-agent", "register", "--server-url="+scheme+"://"+hostname+"."+dnsDomain+":"+port, "--server-token="+serverToken, "--agent-host="+address, "--agent-port=8081", "--non-interactive"); err != nil {
		return err
	}
	if err := run(ctx, "chown", "-R", "stork-agent:root", "/var/lib/stork-agent/certs/", "/var/lib/stork-agent/tokens/"); err != nil {
		return err
	}
	return os.WriteFile("/var/lib/stork-agent/.stork-agent-registered", nil, 0o644)
}

func (r *Runner) Validate(context.Context) error {
	for _, path := range []string{"/etc/stork/server.env", "/etc/stork/agent.env"} {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if info.Size() == 0 {
			return fmt.Errorf("%s is empty", path)
		}
	}
	return nil
}

func (r *Runner) Run(ctx context.Context) error {
	switch os.Getenv("DIB_OBSERVABILITY_COMPONENT") {
	case "samba-metrics":
		if os.Getenv("DIB_SAMBA_METRICS_ENABLED") != "on" {
			r.log.Infof("Samba metrics are disabled")
			return nil
		}
		return run(ctx, "smb_prometheus_endpoint", "-a", "0.0.0.0", "-p", sambaMetricsPort(), "/var/cache/samba/smbprofile.tdb")
	case "stork-server":
		if err := waitFor(ctx, "PostgreSQL", "127.0.0.1:5432"); err != nil {
			return err
		}
		return run(ctx, "/usr/bin/stork-server", "--use-env-file")
	case "stork-agent":
		if err := waitForFile(ctx, "/var/lib/stork-agent/.stork-agent-registered"); err != nil {
			return err
		}
		return run(ctx, "/usr/bin/stork-agent", "--use-env-file")
	default:
		return fmt.Errorf("DIB_OBSERVABILITY_COMPONENT must be samba-metrics, stork-server, or stork-agent")
	}
}

func (r *Runner) Health(ctx context.Context) error {
	for _, address := range []string{"127.0.0.1:9119", "127.0.0.1:9547"} {
		if err := waitFor(ctx, "observability service", address); err != nil {
			return err
		}
	}
	return nil
}

func sambaMetricsPort() string {
	if port := os.Getenv("DIB_SAMBA_METRICS_PORT"); port != "" {
		return port
	}
	return "9922"
}

func waitFor(ctx context.Context, name, address string) error {
	return wait.Until(ctx, name, bootstrapAttempts(), time.Second, func() error {
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			return err
		}
		return connection.Close()
	})
}

func waitForFile(ctx context.Context, path string) error {
	return wait.Until(ctx, path, bootstrapAttempts(), time.Second, func() error {
		_, err := os.Stat(path)
		return err
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

func postgres(ctx context.Context, query, create string) error {
	value, err := postgresValue(ctx, query, "postgres")
	if err != nil {
		return err
	}
	if value == "1" {
		return nil
	}
	return run(ctx, "su", "-s", "/bin/sh", "-c", "psql -v ON_ERROR_STOP=1 -c \""+create+"\"", "postgres")
}

func postgresValue(ctx context.Context, query, database string) (string, error) {
	command := exec.CommandContext(ctx, "su", "-s", "/bin/sh", "-c", "psql -tAc \""+query+"\" -d \""+database+"\"", "postgres")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("query PostgreSQL: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func storkDatabasePassword() (string, error) {
	return storkEnvironment("STORK_DATABASE_PASSWORD")
}

func storkEnvironment(key string) (string, error) {
	contents, err := os.ReadFile("/etc/stork/server.env")
	if err != nil {
		return "", fmt.Errorf("read Stork environment: %w", err)
	}
	prefix := key + "="
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix), nil
		}
	}
	return "", fmt.Errorf("%s is missing from /etc/stork/server.env", key)
}

func requiredEnvironment(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("environment variable %s is not set", key)
	}
	return value, nil
}

func bootstrapAttempts() int {
	if seconds, err := strconv.Atoi(os.Getenv("DIB_BOOTSTRAP_TIMEOUT_SECONDS")); err == nil && seconds > 0 {
		return seconds
	}
	return 120
}

func writeStorkServerEnvironment() error {
	const path = "/etc/stork/server.env"
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	dnsDomain, err := requiredEnvironment("DNS_DOMAIN")
	if err != nil {
		return err
	}
	ip, err := requiredEnvironment("IP")
	if err != nil {
		return err
	}
	hostname, err := requiredEnvironment("CONTAINER_HOSTNAME")
	if err != nil {
		return err
	}
	ldapPassword := os.Getenv("DIB_LDAP_PASSWORD")
	if ldapPassword == "" {
		ldapPassword, err = randomBase64(18)
		if err != nil {
			return err
		}
	}
	databasePassword, err := randomAlphaNumeric(32)
	if err != nil {
		return err
	}
	tls, err := storkTLSSettings()
	if err != nil {
		return err
	}
	ldapRoot := domainComponents(dnsDomain)
	lines := []string{
		"STORK_DATABASE_HOST=127.0.0.1",
		"STORK_DATABASE_PORT=5432",
		"STORK_DATABASE_NAME=stork",
		"STORK_DATABASE_USER_NAME=stork",
		"STORK_DATABASE_PASSWORD=" + databasePassword,
		"",
		"STORK_REST_HOST=" + ip,
		"STORK_REST_PORT=" + tls.port,
	}
	if tls.enabled {
		lines = append(lines, "STORK_REST_TLS_CERTIFICATE=/certs/cert.pem", "STORK_REST_TLS_PRIVATE_KEY=/certs/key.pem")
	}
	lines = append(lines,
		"",
		"STORK_SERVER_HOOK_LDAP_URL=ldaps://"+hostname+"."+dnsDomain+":636",
		"STORK_SERVER_HOOK_LDAP_SKIP_SERVER_TLS_VERIFICATION="+strconv.FormatBool(!tls.enabled),
		"STORK_SERVER_HOOK_LDAP_BIND_USERDN=CN=ldap-search-user,CN=Users,"+ldapRoot,
		"STORK_SERVER_HOOK_LDAP_BIND_PASSWORD="+ldapPassword,
		"STORK_SERVER_HOOK_LDAP_ROOT="+ldapRoot,
		"STORK_SERVER_HOOK_LDAP_MAP_GROUPS=true",
		"STORK_SERVER_HOOK_LDAP_GROUP_ADMIN=Domain Admins",
		"STORK_SERVER_HOOK_LDAP_GROUP_SUPER_ADMIN=Domain Admins",
		"STORK_SERVER_HOOK_LDAP_GROUP_READ_ONLY=Domain Users",
		"STORK_SERVER_HOOK_LDAP_OBJECT_CLASS_USER_ID=sAMAccountName",
		"STORK_SERVER_HOOK_LDAP_OBJECT_CLASS_USER_UNIQUE_IDENTIFIER=objectGUID",
		"STORK_SERVER_HOOK_LDAP_OBJECT_CLASS_GROUP=group",
		"",
	)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func writeStorkAgentEnvironment() error {
	const path = "/etc/stork/agent.env"
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	ip, err := requiredEnvironment("IP")
	if err != nil {
		return err
	}
	contents := "STORK_AGENT_HOST=" + ip + "\nSTORK_AGENT_PORT=8081\nSTORK_AGENT_PROMETHEUS_BIND9_EXPORTER_ADDRESS=0.0.0.0\nSTORK_AGENT_PROMETHEUS_BIND9_EXPORTER_PORT=9119\nSTORK_AGENT_PROMETHEUS_KEA_EXPORTER_ADDRESS=0.0.0.0\nSTORK_AGENT_PROMETHEUS_KEA_EXPORTER_PORT=9547\n"
	return os.WriteFile(path, []byte(contents), 0o644)
}

type tlsSettings struct {
	enabled bool
	port    string
}

func storkTLSSettings() (tlsSettings, error) {
	paths := []string{"/certs/cert.pem", "/certs/key.pem", "/certs/ca.pem"}
	present := 0
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			present++
		} else if err != nil && !os.IsNotExist(err) {
			return tlsSettings{}, err
		}
	}
	if present == 0 {
		return tlsSettings{port: "80"}, nil
	}
	if present != len(paths) {
		return tlsSettings{}, fmt.Errorf("custom Stork TLS requires non-empty cert.pem, key.pem, and ca.pem")
	}
	return tlsSettings{enabled: true, port: "443"}, nil
}

func domainComponents(domain string) string {
	components := strings.Split(domain, ".")
	for index, component := range components {
		components[index] = "DC=" + component
	}
	return strings.Join(components, ",")
}

func randomBase64(byteCount int) (string, error) {
	bytes := make([]byte, byteCount)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(bytes), nil
}

func randomAlphaNumeric(length int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	bytes := make([]byte, length)
	for index := range bytes {
		value, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		bytes[index] = alphabet[value.Int64()]
	}
	return string(bytes), nil
}
