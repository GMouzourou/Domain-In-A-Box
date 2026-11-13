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
	"path/filepath"
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
	return &Runner{log: platformlog.New("observability")}
}

func (r *Runner) Name() string { return "observability" }

func (r *Runner) Configure(ctx context.Context) error {
	if observabilityRole() == "server" {
		return configureStorkServer(ctx)
	}
	return configureStorkAgent(ctx)
}

func configureStorkAgent(ctx context.Context) error {
	if err := os.MkdirAll("/usr/lib/stork-agent/hooks", 0o755); err != nil {
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

func configureStorkServer(ctx context.Context) error {
	for _, path := range []string{"/usr/lib/stork-server/hooks", "/usr/share/stork/www/assets/authentication-methods"} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	if err := os.Chmod("/usr/share/stork/www/assets/authentication-methods", 0o775); err != nil {
		return err
	}
	if err := run(ctx, "chown", "root:stork-server", "/usr/share/stork/www/assets/authentication-methods"); err != nil {
		return err
	}
	if path := os.Getenv("DIB_LDAP_CA_CERTIFICATE"); path != "" {
		if err := installTrustedCA(ctx, path, "domain-in-a-box-ldap-ca.crt"); err != nil {
			return err
		}
	}
	return writeStorkServerEnvironment()
}

func (r *Runner) Bootstrap(ctx context.Context) error {
	if observabilityRole() == "server" {
		return nil
	}

	if os.Getenv("STORK_SERVER_HOST") == "" {
		r.log.Infof("Stork is not configured; skipping agent bootstrap")
		return nil
	}

	if os.Getenv("DIB_STORK_BOOTSTRAP_PENDING") == "TRUE" {
		ldapPassword, err := requiredEnvironment("DIB_LDAP_PASSWORD")
		if err != nil {
			return err
		}
		if err := bootstrap.RunOnce(ctx, "/var/lib/samba/.dib-ldap-search-user-created", func(ctx context.Context) error {
			return run(ctx, "samba-tool", "user", "create", "ldap-search-user", "--description=Read-only LDAP Bind User", ldapPassword)
		}); err != nil {
			return err
		}
		if err := bootstrap.RunOnce(ctx, "/var/lib/samba/.dib-ldap-search-user-expiry-set", func(ctx context.Context) error {
			return run(ctx, "samba-tool", "user", "setexpiry", "ldap-search-user", "--noexpiry")
		}); err != nil {
			return err
		}
	}

	const registrationMarker = "/var/lib/stork-agent/.stork-agent-registered"
	if _, err := os.Stat(registrationMarker); err == nil {
		if advertisedHost := os.Getenv("DIB_STORK_AGENT_ADVERTISE_HOST"); advertisedHost != "" {
			if err := validateRegisteredAgentEndpoint(registrationMarker, advertisedHost); err != nil {
				return err
			}
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check Stork agent registration: %w", err)
	}

	address, err := requiredEnvironment("STORK_SERVER_HOST")
	if err != nil {
		return err
	}
	port := os.Getenv("STORK_SERVER_PORT")
	if port == "" {
		port = "443"
	}
	if err := waitFor(ctx, "Stork server", net.JoinHostPort(address, port)); err != nil {
		return err
	}
	scheme := os.Getenv("STORK_SERVER_SCHEME")
	if scheme == "" {
		scheme = "https"
	}
	if scheme != "https" && scheme != "http" {
		return fmt.Errorf("unsupported Stork server scheme %q", scheme)
	}
	if scheme == "http" && os.Getenv("DIB_ALLOW_INSECURE_STORK_HTTP") != "true" {
		return fmt.Errorf("Stork agent registration over HTTP requires DIB_ALLOW_INSECURE_STORK_HTTP=true")
	}
	serverURL := scheme + "://" + net.JoinHostPort(address, port)
	token, err := externalPostgresValue(ctx, "SELECT * FROM secret")
	if err != nil {
		return err
	}
	parts := strings.Split(token, "|")
	serverToken := parts[len(parts)-1]
	if serverToken == "" {
		return fmt.Errorf("Stork server token is empty")
	}
	agentHost := os.Getenv("DIB_STORK_AGENT_ADVERTISE_HOST")
	if agentHost == "" {
		agentHost, err = requiredEnvironment("IP")
		if err != nil {
			return err
		}
	}
	return registerStorkAgent(ctx, "/var/lib/stork-agent", agentHost, "--server-url="+serverURL, "--server-token="+serverToken, "--agent-host="+agentHost, "--agent-port=8081", "--non-interactive")
}

func registerStorkAgent(ctx context.Context, stateDir, advertisedHost string, args ...string) error {
	if err := run(ctx, "stork-agent", append([]string{"register"}, args...)...); err != nil {
		return err
	}
	// Registration runs as root, but the agent reads its credentials as stork-agent.
	if err := run(ctx, "chown", "-R", "stork-agent:root", stateDir); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, ".stork-agent-registered"), []byte(advertisedHost), 0o644)
}

func validateRegisteredAgentEndpoint(markerPath, advertisedHost string) error {
	registeredHost, err := os.ReadFile(markerPath)
	if err != nil {
		return fmt.Errorf("read Stork agent registration marker: %w", err)
	}
	if len(registeredHost) == 0 {
		return fmt.Errorf("Stork agent has a legacy registration with no recorded callback address; remove its old entry from Stork and clear its registration state before enabling the internal Service endpoint")
	}
	if string(registeredHost) != advertisedHost {
		return fmt.Errorf("Stork agent callback address changed from %q to %q; update the existing Stork machine entry before restarting to avoid a stale or duplicate registration", string(registeredHost), advertisedHost)
	}
	return nil
}

func (r *Runner) Validate(context.Context) error {
	path := "/etc/stork/agent.env"
	if observabilityRole() == "server" {
		path = "/etc/stork/server.env"
	}
	for _, path := range []string{path} {
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
		return process.Replace("smb_prometheus_endpoint", "-a", "0.0.0.0", "-p", sambaMetricsPort(), "/var/cache/samba/smbprofile.tdb")
	case "stork-server":
		if err := configureStorkServer(ctx); err != nil {
			return err
		}
		dbHost, _ := storkEnvironment("STORK_DATABASE_HOST")
		if dbHost == "" {
			dbHost = "127.0.0.1"
		}
		dbPort, _ := storkEnvironment("STORK_DATABASE_PORT")
		if dbPort == "" {
			dbPort = "5432"
		}
		if err := waitFor(ctx, "PostgreSQL", net.JoinHostPort(dbHost, dbPort)); err != nil {
			return err
		}
		return process.Replace("/usr/bin/stork-server", "--use-env-file")
	case "stork-agent":
		if os.Getenv("STORK_SERVER_HOST") == "" {
			r.log.Infof("Stork is not configured; agent is disabled")
			return nil
		}
		if err := waitForFile(ctx, "/var/lib/stork-agent/.stork-agent-registered"); err != nil {
			return err
		}
		return process.Replace("/usr/bin/stork-agent", "--use-env-file")
	default:
		return fmt.Errorf("DIB_OBSERVABILITY_COMPONENT must be samba-metrics, stork-server, or stork-agent")
	}
}

func observabilityRole() string {
	if os.Getenv("DIB_OBSERVABILITY_COMPONENT") == "stork-server" {
		return "server"
	}
	return "agent"
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

func externalPostgresValue(ctx context.Context, query string) (string, error) {
	host, err := requiredEnvironment("STORK_DATABASE_HOST")
	if err != nil {
		return "", err
	}
	port := os.Getenv("STORK_DATABASE_PORT")
	if port == "" {
		port = "5432"
	}
	database := os.Getenv("STORK_DATABASE_NAME")
	if database == "" {
		database = "stork"
	}
	user := os.Getenv("STORK_DATABASE_USER_NAME")
	if user == "" {
		user = "stork"
	}
	password, err := requiredEnvironment("STORK_DATABASE_PASSWORD")
	if err != nil {
		return "", err
	}
	sslMode, rootCert, err := postgresTLSSettings()
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, "psql", "-h", host, "-p", port, "-U", user, "-d", database, "-tAc", query)
	command.Env = environmentWithValues(os.Environ(), map[string]string{
		"PGPASSWORD":    password,
		"PGSSLMODE":     sslMode,
		"PGSSLROOTCERT": rootCert,
	})
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("query configured PostgreSQL service: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func requiredEnvironment(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("environment variable %s is not set", key)
	}
	return value, nil
}

func postgresTLSSettings() (string, string, error) {
	externalSetting := os.Getenv("DIB_STORK_DATABASE_EXTERNAL")
	if externalSetting == "" && os.Getenv("STORK_DATABASE_HOST") != "" {
		return "", "", fmt.Errorf("DIB_STORK_DATABASE_EXTERNAL must be set when STORK_DATABASE_HOST is configured")
	}
	if externalSetting == "" || externalSetting == "false" {
		mode := os.Getenv("STORK_DATABASE_SSLMODE")
		if mode == "" {
			mode = "disable"
		}
		return mode, os.Getenv("STORK_DATABASE_SSLROOTCERT"), nil
	}
	if externalSetting != "true" {
		return "", "", fmt.Errorf("invalid DIB_STORK_DATABASE_EXTERNAL value %q", externalSetting)
	}

	mode := os.Getenv("PGSSLMODE")
	if mode == "" {
		mode = os.Getenv("STORK_DATABASE_SSLMODE")
	}
	if mode == "" {
		mode = "verify-full"
	}
	switch mode {
	case "disable", "require":
		if os.Getenv("DIB_ALLOW_INSECURE_POSTGRESQL") != "true" {
			return "", "", fmt.Errorf("external PostgreSQL sslmode %q requires DIB_ALLOW_INSECURE_POSTGRESQL=true", mode)
		}
	case "verify-ca", "verify-full":
	default:
		return "", "", fmt.Errorf("unsupported external PostgreSQL sslmode %q", mode)
	}

	rootCert := os.Getenv("PGSSLROOTCERT")
	if rootCert == "" {
		rootCert = os.Getenv("STORK_DATABASE_SSLROOTCERT")
	}
	if mode == "verify-ca" || mode == "verify-full" {
		if rootCert == "" {
			return "", "", fmt.Errorf("external PostgreSQL sslmode %q requires PGSSLROOTCERT or STORK_DATABASE_SSLROOTCERT", mode)
		}
		info, err := os.Stat(rootCert)
		if err != nil {
			return "", "", fmt.Errorf("read external PostgreSQL CA certificate %s: %w", rootCert, err)
		}
		if info.Size() == 0 {
			return "", "", fmt.Errorf("external PostgreSQL CA certificate %s is empty", rootCert)
		}
	}
	return mode, rootCert, nil
}

func ldapTLSVerificationSetting() (string, error) {
	skipVerification := os.Getenv("STORK_SERVER_HOOK_LDAP_SKIP_SERVER_TLS_VERIFICATION")
	if skipVerification == "" {
		skipVerification = "false"
	}
	if skipVerification != "true" && skipVerification != "false" {
		return "", fmt.Errorf("invalid LDAP TLS verification value %q", skipVerification)
	}
	if skipVerification == "true" && os.Getenv("DIB_ALLOW_INSECURE_LDAP_TLS") != "true" {
		return "", fmt.Errorf("skipping LDAP certificate verification requires DIB_ALLOW_INSECURE_LDAP_TLS=true")
	}
	return skipVerification, nil
}

func environmentWithValues(environment []string, values map[string]string) []string {
	for key, value := range values {
		prefix := key + "="
		filtered := environment[:0]
		for _, entry := range environment {
			if !strings.HasPrefix(entry, prefix) {
				filtered = append(filtered, entry)
			}
		}
		environment = append(filtered, prefix+value)
	}
	return environment
}

func bootstrapAttempts() int {
	if seconds, err := strconv.Atoi(os.Getenv("DIB_BOOTSTRAP_TIMEOUT_SECONDS")); err == nil && seconds > 0 {
		return seconds
	}
	return 120
}

func writeStorkServerEnvironment() error {
	return writeStorkServerEnvironmentAt("/etc/stork/server.env")
}

func writeStorkServerEnvironmentAt(path string) error {
	existing, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	reconcile := os.Getenv("DIB_STORK_RECONCILE_SERVER_ENV") == "true"
	if readErr == nil {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("restrict Stork environment file permissions: %w", err)
		}
		if !reconcile {
			return nil
		}
	}
	var err error
	dnsDomain := os.Getenv("STORK_LDAP_DOMAIN")
	if dnsDomain == "" {
		dnsDomain, err = requiredEnvironment("DNS_DOMAIN")
	}
	if err != nil {
		return err
	}
	ip := os.Getenv("STORK_REST_HOST")
	if ip == "" {
		ip, err = requiredEnvironment("IP")
	}
	if err != nil {
		return err
	}
	hostname := os.Getenv("STORK_LDAP_HOST")
	if hostname == "" {
		hostname, err = requiredEnvironment("CONTAINER_HOSTNAME")
	}
	if err != nil {
		return err
	}
	ldapPassword := os.Getenv("DIB_LDAP_PASSWORD")
	if ldapPassword == "" {
		ldapPassword = os.Getenv("STORK_LDAP_PASSWORD")
	}
	if ldapPassword == "" {
		ldapPassword, err = randomBase64(18)
		if err != nil {
			return err
		}
	}
	databaseHost := os.Getenv("STORK_DATABASE_HOST")
	if databaseHost == "" {
		databaseHost = "127.0.0.1"
	}
	databasePort := os.Getenv("STORK_DATABASE_PORT")
	if databasePort == "" {
		databasePort = "5432"
	}
	databaseName := os.Getenv("STORK_DATABASE_NAME")
	if databaseName == "" {
		databaseName = "stork"
	}
	databaseUser := os.Getenv("STORK_DATABASE_USER_NAME")
	if databaseUser == "" {
		databaseUser = "stork"
	}
	databasePassword := os.Getenv("STORK_DATABASE_PASSWORD")
	if databasePassword == "" {
		databasePassword, err = randomAlphaNumeric(32)
		if err != nil {
			return err
		}
	}
	tls, err := storkTLSSettings()
	if err != nil {
		return err
	}
	databaseSSLMode, databaseRootCert, err := postgresTLSSettings()
	if err != nil {
		return err
	}
	ldapRoot := domainComponents(dnsDomain)
	lines := []string{
		"STORK_DATABASE_HOST=" + databaseHost,
		"STORK_DATABASE_PORT=" + databasePort,
		"STORK_DATABASE_NAME=" + databaseName,
		"STORK_DATABASE_USER_NAME=" + databaseUser,
		"STORK_DATABASE_PASSWORD=" + databasePassword,
		"STORK_DATABASE_SSLMODE=" + databaseSSLMode,
		"",
		"STORK_REST_HOST=" + ip,
		"STORK_REST_PORT=" + tls.port,
	}
	if tls.enabled {
		lines = append(lines, "STORK_REST_TLS_CERTIFICATE="+tls.certificate, "STORK_REST_TLS_PRIVATE_KEY="+tls.privateKey)
	}
	if ca := os.Getenv("STORK_REST_TLS_CA_CERTIFICATE"); ca != "" {
		lines = append(lines, "STORK_REST_TLS_CA_CERTIFICATE="+ca)
	}
	if databaseRootCert != "" {
		lines = append(lines, "STORK_DATABASE_SSLROOTCERT="+databaseRootCert)
	}
	for _, key := range []string{"STORK_DATABASE_SSLCERT", "STORK_DATABASE_SSLKEY"} {
		if value := os.Getenv(key); value != "" {
			lines = append(lines, key+"="+value)
		}
	}
	ldapURL := os.Getenv("STORK_LDAP_URL")
	if ldapURL == "" {
		ldapURL = "ldaps://" + hostname + "." + dnsDomain + ":636"
	}
	skipLDAPVerification, err := ldapTLSVerificationSetting()
	if err != nil {
		return err
	}
	lines = append(lines,
		"",
		"STORK_SERVER_HOOK_LDAP_URL="+ldapURL,
		"STORK_SERVER_HOOK_LDAP_SKIP_SERVER_TLS_VERIFICATION="+skipLDAPVerification,
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
	contents := strings.Join(lines, "\n")
	if readErr == nil {
		contents = mergeEnvironmentFile(string(existing), lines)
	}
	return os.WriteFile(path, []byte(contents), 0o600)
}

func writeStorkAgentEnvironment() error {
	return writeStorkAgentEnvironmentAt("/etc/stork/agent.env")
}

func writeStorkAgentEnvironmentAt(path string) error {
	existing, err := os.ReadFile(path)
	if err == nil {
		if os.Getenv("DIB_STORK_RECONCILE_AGENT_ENV") != "true" {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	bindAddress := os.Getenv("DIB_STORK_AGENT_BIND_ADDRESS")
	if bindAddress == "" {
		var err error
		bindAddress, err = requiredEnvironment("IP")
		if err != nil {
			return err
		}
	}
	lines := []string{
		"STORK_AGENT_HOST=" + bindAddress,
		"STORK_AGENT_PORT=8081",
		"STORK_AGENT_PROMETHEUS_BIND9_EXPORTER_ADDRESS=0.0.0.0",
		"STORK_AGENT_PROMETHEUS_BIND9_EXPORTER_PORT=9119",
		"STORK_AGENT_PROMETHEUS_KEA_EXPORTER_ADDRESS=0.0.0.0",
		"STORK_AGENT_PROMETHEUS_KEA_EXPORTER_PORT=9547",
	}
	contents := strings.Join(lines, "\n") + "\n"
	if err == nil {
		contents = mergeEnvironmentFile(string(existing), lines) + "\n"
	}
	return os.WriteFile(path, []byte(contents), 0o644)
}

type tlsSettings struct {
	enabled     bool
	port        string
	certificate string
	privateKey  string
}

func storkTLSSettings() (tlsSettings, error) {
	certificate := os.Getenv("STORK_REST_TLS_CERTIFICATE")
	privateKey := os.Getenv("STORK_REST_TLS_PRIVATE_KEY")
	if certificate == "" {
		certificate = "/certs/cert.pem"
	}
	if privateKey == "" {
		privateKey = "/certs/key.pem"
	}
	explicit := os.Getenv("DIB_STORK_REST_TLS_ENABLED")
	allowInsecureHTTP := os.Getenv("DIB_ALLOW_INSECURE_STORK_HTTP") == "true"
	paths := []string{certificate, privateKey}
	present := 0
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			present++
		} else if err != nil && !os.IsNotExist(err) {
			return tlsSettings{}, err
		}
	}
	if explicit == "false" {
		if !allowInsecureHTTP {
			return tlsSettings{}, fmt.Errorf("disabling Stork REST TLS requires DIB_ALLOW_INSECURE_STORK_HTTP=true")
		}
		return tlsSettings{port: storkRESTPort("80")}, nil
	}
	if explicit != "" && explicit != "true" {
		return tlsSettings{}, fmt.Errorf("invalid Stork REST TLS setting %q", explicit)
	}
	if present == 0 {
		return tlsSettings{}, fmt.Errorf("Stork REST TLS requires non-empty certificate and private key files")
	}
	if present != len(paths) {
		return tlsSettings{}, fmt.Errorf("custom Stork TLS requires non-empty certificate and private key files")
	}
	return tlsSettings{enabled: true, port: storkRESTPort("443"), certificate: certificate, privateKey: privateKey}, nil
}

func storkRESTPort(defaultPort string) string {
	if port := os.Getenv("STORK_REST_PORT"); port != "" {
		return port
	}
	return defaultPort
}

func mergeEnvironmentFile(existing string, generated []string) string {
	managed := make(map[string]struct{}, len(generated))
	for _, line := range generated {
		if key, _, ok := strings.Cut(line, "="); ok {
			managed[key] = struct{}{}
		}
	}
	merged := make([]string, 0, len(strings.Split(existing, "\n"))+len(generated))
	for _, line := range strings.Split(existing, "\n") {
		key, _, ok := strings.Cut(line, "=")
		_, replace := managed[key]
		if !ok || (!replace && !strings.HasPrefix(key, "STORK_REST_TLS_") && !strings.HasPrefix(key, "STORK_DATABASE_SSL")) {
			merged = append(merged, line)
		}
	}
	merged = append(merged, generated...)
	return strings.Join(merged, "\n")
}

func installTrustedCA(ctx context.Context, source, name string) error {
	contents, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read trusted CA certificate %s: %w", source, err)
	}
	if len(contents) == 0 {
		return fmt.Errorf("trusted CA certificate %s is empty", source)
	}
	target := filepath.Join("/usr/local/share/ca-certificates", name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(target, contents, 0o644); err != nil {
		return err
	}
	return run(ctx, "update-ca-certificates")
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
