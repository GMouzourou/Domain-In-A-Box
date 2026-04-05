package helpers

import (
	"fmt"
	"os"
	"strings"
)

// KerberosConfig represents Kerberos configuration
type KerberosConfig struct {
	Realm              string
	DefaultRealm       string
	DnsLookupRealm     bool
	DnsLookupKdc       bool
	RDns               bool
	DomainFQDN         string
	DomainControllerIP string
}

// WriteKerberosConfig writes krb5.conf file
func WriteKerberosConfig(cfg KerberosConfig) error {
	LogStep("Configuring Kerberos client...")

	config := fmt.Sprintf(`[libdefaults]
    default_realm = %s
    dns_lookup_realm = %s
    dns_lookup_kdc = %s
    rdns = %s

[realms]
    %s = {
        kdc = %s
        admin_server = %s
        master_kdc = %s
    }

[domain_realm]
    .%s = %s
    %s = %s
`, cfg.Realm,
		boolToYesNo(cfg.DnsLookupRealm),
		boolToYesNo(cfg.DnsLookupKdc),
		boolToYesNo(!cfg.RDns),
		cfg.Realm,
		cfg.DomainControllerIP,
		cfg.DomainControllerIP,
		cfg.DomainControllerIP,
		cfg.DomainFQDN,
		cfg.Realm,
		cfg.DomainFQDN,
		cfg.Realm)

	if err := os.WriteFile("/etc/krb5.conf", []byte(config), 0644); err != nil {
		return fmt.Errorf("failed to write krb5.conf: %w", err)
	}

	LogInfo("Kerberos configured")
	return nil
}

// boolToYesNo converts a boolean to yes/no string
func boolToYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// ConvertDomainToLDAP converts domain.arpa to LDAP dn format
func ConvertDomainToLDAP(domain string) string {
	parts := strings.Split(domain, ".")
	ldapParts := make([]string, len(parts))
	for i, part := range parts {
		ldapParts[i] = fmt.Sprintf("dc=%s", part)
	}
	return strings.Join(ldapParts, ",")
}
