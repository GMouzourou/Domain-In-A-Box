package observability

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapSkipsStorkWhenNoServerIsConfigured(t *testing.T) {
	t.Setenv("DIB_OBSERVABILITY_COMPONENT", "stork-agent")
	t.Setenv("STORK_SERVER_HOST", "")
	if err := NewRunner().Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
}

func TestStorkTLSSettingsRequireTLSOrExplicitInsecureOptIn(t *testing.T) {
	t.Setenv("STORK_REST_TLS_CERTIFICATE", filepath.Join(t.TempDir(), "missing.crt"))
	t.Setenv("STORK_REST_TLS_PRIVATE_KEY", filepath.Join(t.TempDir(), "missing.key"))
	t.Setenv("DIB_STORK_REST_TLS_ENABLED", "")
	t.Setenv("DIB_ALLOW_INSECURE_STORK_HTTP", "")
	if _, err := storkTLSSettings(); err == nil {
		t.Fatal("storkTLSSettings() accepted missing TLS files without an insecure opt-in")
	}

	t.Setenv("DIB_STORK_REST_TLS_ENABLED", "false")
	if _, err := storkTLSSettings(); err == nil {
		t.Fatal("storkTLSSettings() accepted disabled TLS without an insecure opt-in")
	}
	t.Setenv("DIB_ALLOW_INSECURE_STORK_HTTP", "true")
	settings, err := storkTLSSettings()
	if err != nil {
		t.Fatalf("storkTLSSettings() with explicit insecure opt-in error = %v", err)
	}
	if settings.enabled || settings.port != "80" {
		t.Fatalf("insecure Stork TLS settings = %+v, want disabled on port 80", settings)
	}
}

func TestStorkTLSSettingsDefaultsToHTTPSWhenCertificatesExist(t *testing.T) {
	dir := t.TempDir()
	certificate := filepath.Join(dir, "server.crt")
	privateKey := filepath.Join(dir, "server.key")
	for _, path := range []string{certificate, privateKey} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("STORK_REST_TLS_CERTIFICATE", certificate)
	t.Setenv("STORK_REST_TLS_PRIVATE_KEY", privateKey)
	t.Setenv("DIB_STORK_REST_TLS_ENABLED", "")
	t.Setenv("DIB_ALLOW_INSECURE_STORK_HTTP", "")

	settings, err := storkTLSSettings()
	if err != nil {
		t.Fatalf("storkTLSSettings() error = %v", err)
	}
	if !settings.enabled || settings.port != "443" {
		t.Fatalf("default Stork TLS settings = %+v, want enabled on port 443", settings)
	}
}

func TestPostgresTLSSettingsRequireVerifiedExternalTLSOrExplicitOptIn(t *testing.T) {
	t.Setenv("DIB_STORK_DATABASE_EXTERNAL", "true")
	t.Setenv("PGSSLMODE", "")
	t.Setenv("STORK_DATABASE_SSLMODE", "disable")
	t.Setenv("DIB_ALLOW_INSECURE_POSTGRESQL", "")
	if _, _, err := postgresTLSSettings(); err == nil {
		t.Fatal("postgresTLSSettings() accepted external PostgreSQL without verified TLS or opt-in")
	}

	t.Setenv("DIB_ALLOW_INSECURE_POSTGRESQL", "true")
	mode, _, err := postgresTLSSettings()
	if err != nil {
		t.Fatalf("postgresTLSSettings() with explicit insecure opt-in error = %v", err)
	}
	if mode != "disable" {
		t.Fatalf("external PostgreSQL SSL mode = %q, want disable", mode)
	}

	t.Setenv("DIB_ALLOW_INSECURE_POSTGRESQL", "false")
	t.Setenv("PGSSLMODE", "")
	t.Setenv("STORK_DATABASE_SSLMODE", "")
	t.Setenv("PGSSLROOTCERT", "")
	t.Setenv("STORK_DATABASE_SSLROOTCERT", "")
	if _, _, err := postgresTLSSettings(); err == nil {
		t.Fatal("postgresTLSSettings() accepted verify-full without a root CA")
	}

	rootCert := filepath.Join(t.TempDir(), "postgres-ca.crt")
	if err := os.WriteFile(rootCert, []byte("test CA"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGSSLROOTCERT", rootCert)
	mode, gotRootCert, err := postgresTLSSettings()
	if err != nil {
		t.Fatalf("postgresTLSSettings() with verify-full CA error = %v", err)
	}
	if mode != "verify-full" || gotRootCert != rootCert {
		t.Fatalf("postgres TLS settings = (%q, %q), want (verify-full, %q)", mode, gotRootCert, rootCert)
	}
}

func TestLDAPTLSVerificationRequiresExplicitInsecureOptIn(t *testing.T) {
	t.Setenv("STORK_SERVER_HOOK_LDAP_SKIP_SERVER_TLS_VERIFICATION", "")
	if got, err := ldapTLSVerificationSetting(); err != nil || got != "false" {
		t.Fatalf("default ldapTLSVerificationSetting() = (%q, %v), want (false, nil)", got, err)
	}

	t.Setenv("STORK_SERVER_HOOK_LDAP_SKIP_SERVER_TLS_VERIFICATION", "true")
	t.Setenv("DIB_ALLOW_INSECURE_LDAP_TLS", "")
	if _, err := ldapTLSVerificationSetting(); err == nil {
		t.Fatal("ldapTLSVerificationSetting() accepted skipped verification without opt-in")
	}

	t.Setenv("DIB_ALLOW_INSECURE_LDAP_TLS", "true")
	if got, err := ldapTLSVerificationSetting(); err != nil || got != "true" {
		t.Fatalf("ldapTLSVerificationSetting() = (%q, %v), want (true, nil)", got, err)
	}
}

func TestExistingStorkEnvironmentFilePermissionsAreRestricted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.env")
	if err := os.WriteFile(path, []byte("STORK_DATABASE_PASSWORD=secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeStorkServerEnvironmentAt(path); err != nil {
		t.Fatalf("writeStorkServerEnvironmentAt() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("server.env mode = %04o, want 0600", got)
	}
}

func TestWriteStorkServerEnvironmentUsesIndependentTLSSettings(t *testing.T) {
	dir := t.TempDir()
	certificate := filepath.Join(dir, "server.crt")
	privateKey := filepath.Join(dir, "server.key")
	for _, path := range []string{certificate, privateKey} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("DIB_STORK_REST_TLS_ENABLED", "true")
	t.Setenv("STORK_REST_TLS_CERTIFICATE", certificate)
	t.Setenv("STORK_REST_TLS_PRIVATE_KEY", privateKey)
	t.Setenv("STORK_REST_PORT", "443")
	t.Setenv("STORK_REST_HOST", "0.0.0.0")
	t.Setenv("STORK_LDAP_DOMAIN", "domain.home.arpa")
	t.Setenv("STORK_LDAP_HOST", "dib-svc")
	t.Setenv("STORK_LDAP_URL", "ldaps://dib-svc:636")
	t.Setenv("STORK_SERVER_HOOK_LDAP_SKIP_SERVER_TLS_VERIFICATION", "false")
	t.Setenv("STORK_DATABASE_SSLMODE", "verify-full")
	t.Setenv("STORK_DATABASE_SSLROOTCERT", "/etc/stork/postgresql-tls/ca.crt")
	t.Setenv("STORK_DATABASE_SSLCERT", "/etc/stork/postgresql-tls/tls.crt")
	t.Setenv("STORK_DATABASE_SSLKEY", "/etc/stork/postgresql-tls/tls.key")

	path := filepath.Join(dir, "server.env")
	if err := writeStorkServerEnvironmentAt(path); err != nil {
		t.Fatalf("writeStorkServerEnvironmentAt() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"STORK_REST_PORT=443",
		"STORK_REST_TLS_CERTIFICATE=" + certificate,
		"STORK_REST_TLS_PRIVATE_KEY=" + privateKey,
		"STORK_SERVER_HOOK_LDAP_URL=ldaps://dib-svc:636",
		"STORK_SERVER_HOOK_LDAP_SKIP_SERVER_TLS_VERIFICATION=false",
		"STORK_DATABASE_SSLMODE=verify-full",
		"STORK_DATABASE_SSLROOTCERT=/etc/stork/postgresql-tls/ca.crt",
		"STORK_DATABASE_SSLCERT=/etc/stork/postgresql-tls/tls.crt",
		"STORK_DATABASE_SSLKEY=/etc/stork/postgresql-tls/tls.key",
	} {
		if !strings.Contains(string(contents), expected) {
			t.Errorf("server.env missing %q:\n%s", expected, contents)
		}
	}
}

func TestReconcileStorkServerEnvironmentPreservesUnmanagedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.env")
	if err := os.WriteFile(path, []byte("CUSTOM_SETTING=keep\nSTORK_REST_PORT=80\nSTORK_REST_TLS_CERTIFICATE=/old/cert.pem\nSTORK_DATABASE_SSLROOTCERT=/old/ca.pem\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIB_STORK_RECONCILE_SERVER_ENV", "true")
	t.Setenv("DIB_STORK_REST_TLS_ENABLED", "false")
	t.Setenv("DIB_ALLOW_INSECURE_STORK_HTTP", "true")
	t.Setenv("STORK_REST_PORT", "8080")
	t.Setenv("STORK_REST_HOST", "0.0.0.0")
	t.Setenv("STORK_LDAP_DOMAIN", "domain.home.arpa")
	t.Setenv("STORK_LDAP_HOST", "dib-svc")
	t.Setenv("STORK_SERVER_HOOK_LDAP_SKIP_SERVER_TLS_VERIFICATION", "false")

	if err := writeStorkServerEnvironmentAt(path); err != nil {
		t.Fatalf("writeStorkServerEnvironmentAt() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "CUSTOM_SETTING=keep") {
		t.Fatalf("unmanaged setting was lost:\n%s", contents)
	}
	if !strings.Contains(string(contents), "STORK_REST_PORT=8080") {
		t.Fatalf("REST port was not reconciled:\n%s", contents)
	}
	if !strings.Contains(string(contents), "STORK_SERVER_HOOK_LDAP_SKIP_SERVER_TLS_VERIFICATION=false") {
		t.Fatalf("LDAP verification was not reconciled independently of REST TLS:\n%s", contents)
	}
	if strings.Contains(string(contents), "STORK_REST_PORT=80\n") {
		t.Fatalf("stale REST port remains:\n%s", contents)
	}
	if strings.Contains(string(contents), "/old/") {
		t.Fatalf("stale TLS settings remain:\n%s", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("reconciled server.env mode = %04o, want 0600", got)
	}
}

func TestStorkAgentListenerBindIsIndependentOfAdvertisedAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.env")
	if err := os.WriteFile(path, []byte("STORK_AGENT_HOST=192.168.1.10\nCUSTOM_AGENT_SETTING=preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIB_STORK_RECONCILE_AGENT_ENV", "true")
	t.Setenv("DIB_STORK_AGENT_BIND_ADDRESS", "0.0.0.0")
	t.Setenv("DIB_STORK_AGENT_ADVERTISE_HOST", "dib-stork-agent.dib.svc.cluster.local")

	if err := writeStorkAgentEnvironmentAt(path); err != nil {
		t.Fatalf("writeStorkAgentEnvironmentAt() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "STORK_AGENT_HOST=0.0.0.0\n") {
		t.Fatalf("listener is not bound to the pod network:\n%s", contents)
	}
	if strings.Contains(string(contents), "192.168.1.10") || strings.Contains(string(contents), "dib-stork-agent.dib.svc.cluster.local") {
		t.Fatalf("registration address leaked into the listener bind setting:\n%s", contents)
	}
	if !strings.Contains(string(contents), "CUSTOM_AGENT_SETTING=preserve") {
		t.Fatalf("unmanaged agent setting was lost:\n%s", contents)
	}
}

func TestRegisterStorkAgent(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail string
	}{
		{name: "flat credential files"},
		{name: "registration failure", fail: "register"},
		{name: "ownership failure", fail: "chown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			state := filepath.Join(dir, "state")
			if err := os.Mkdir(state, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("DIB_TEST_STORK_STATE", state)
			t.Setenv("DIB_TEST_STORK_FAIL", tc.fail)
			commands := map[string]string{
				"stork-agent": `#!/bin/sh
set -eu
[ "$#" -eq 2 ]
[ "$1" = register ]
[ "$2" = --non-interactive ]
[ ! -e "$DIB_TEST_STORK_STATE/.stork-agent-registered" ]
[ "$DIB_TEST_STORK_FAIL" != register ] || exit 1
for file in root-cert.pem server-cert.pem server-key.pem token.txt server-cert.sha256; do
    printf 'test credential\n' > "$DIB_TEST_STORK_STATE/$file"
done
`,
				"chown": `#!/bin/sh
set -eu
[ "$#" -eq 3 ]
[ "$1" = -R ]
[ "$2" = stork-agent:root ]
[ "$3" = "$DIB_TEST_STORK_STATE" ]
[ ! -e "$DIB_TEST_STORK_STATE/.stork-agent-registered" ]
for file in root-cert.pem server-cert.pem server-key.pem token.txt server-cert.sha256; do
    [ -s "$3/$file" ]
done
[ "$DIB_TEST_STORK_FAIL" != chown ] || exit 1
printf 'ownership updated\n' > "$3/ownership-updated"
`,
			}
			for name, script := range commands {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			const advertisedHost = "dib-stork-agent.domain-in-a-box.svc.cluster.local"
			err := registerStorkAgent(context.Background(), state, advertisedHost, "--non-interactive")
			if tc.fail != "" {
				if err == nil || !strings.Contains(err.Error(), "exit status 1") {
					t.Fatalf("registerStorkAgent() error = %v, want command failure", err)
				}
				if _, err := os.Stat(filepath.Join(state, ".stork-agent-registered")); !os.IsNotExist(err) {
					t.Fatalf("registration marker must not exist after failure; stat error = %v", err)
				}
				t.Setenv("DIB_TEST_STORK_FAIL", "")
				err = registerStorkAgent(context.Background(), state, advertisedHost, "--non-interactive")
			}
			if err != nil {
				t.Fatalf("registerStorkAgent() error = %v", err)
			}
			for _, name := range []string{"ownership-updated", ".stork-agent-registered"} {
				if _, err := os.Stat(filepath.Join(state, name)); err != nil {
					t.Fatalf("expected %s after registration: %v", name, err)
				}
			}
			marker, err := os.ReadFile(filepath.Join(state, ".stork-agent-registered"))
			if err != nil {
				t.Fatal(err)
			}
			if string(marker) != advertisedHost {
				t.Fatalf("registration marker = %q, want %q", marker, advertisedHost)
			}
		})
	}
}

func TestValidateRegisteredAgentEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registered")
	if err := os.WriteFile(path, []byte("agent.internal.svc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateRegisteredAgentEndpoint(path, "agent.internal.svc"); err != nil {
		t.Fatalf("validateRegisteredAgentEndpoint() error = %v", err)
	}
	if err := validateRegisteredAgentEndpoint(path, "agent.new.svc"); err == nil {
		t.Fatal("validateRegisteredAgentEndpoint() accepted a changed callback address")
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateRegisteredAgentEndpoint(path, "agent.internal.svc"); err == nil {
		t.Fatal("validateRegisteredAgentEndpoint() accepted a legacy marker")
	}
}
