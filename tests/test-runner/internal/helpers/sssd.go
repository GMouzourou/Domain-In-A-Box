package helpers

import (
	"fmt"
	"os"
)

// SSSDConfig represents SSSD configuration
type SSSDConfig struct {
	Domain             string
	RealmFQDN          string
	LDAPServer         string
	LDAPPort           string
	AdminUser          string
	AdminPassword      string
	DomainControllerIP string
	DomainNameContext  string
}

// WriteSSSDConfig writes sssd.conf file
func WriteSSSDConfig(cfg SSSDConfig) error {
	LogStep("Configuring SSSD for LDAP authentication...")

	config := fmt.Sprintf(`[sssd]
services = nss, pam
domains = %s

[domain/%s]
cache_credentials = true
id_provider = ldap
auth_provider = kerberos
ldap_uri = ldap://%s:%s
ldap_search_base = %s
ldap_default_bind_dn = cn=%s,cn=Users,%s
ldap_default_authtok = %s
ldap_default_authtok_type = password
ldap_user_search_base = cn=Users,%s
ldap_group_search_base = cn=Users,%s
ldap_id_use_start_tls = false
krb5_realm = %s
krb5_server = %s
use_fully_qualified_names = true
`, cfg.Domain,
		cfg.Domain,
		cfg.LDAPServer,
		cfg.LDAPPort,
		cfg.DomainNameContext,
		cfg.AdminUser,
		cfg.DomainNameContext,
		cfg.AdminPassword,
		cfg.DomainNameContext,
		cfg.DomainNameContext,
		cfg.RealmFQDN,
		cfg.DomainControllerIP)

	if err := os.WriteFile("/etc/sssd/sssd.conf", []byte(config), 0600); err != nil {
		return fmt.Errorf("failed to write sssd.conf: %w", err)
	}

	LogInfo("SSSD configured")
	return nil
}
