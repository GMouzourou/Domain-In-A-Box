package network

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// LDAPClient wraps LDAP operations
type LDAPClient struct {
	Server   string
	Port     string
	Timeout  time.Duration
	UseTLS   bool
	Username string
	Password string
}

// NewLDAPClient creates a new LDAP client
func NewLDAPClient(server string) *LDAPClient {
	client := &LDAPClient{
		Server:  server,
		Timeout: 5 * time.Second,
		UseTLS:  false,
	}

	// Use mapped ports when connecting to localhost (for Docker port mapping)
	if server == "localhost" || server == "127.0.0.1" {
		client.Port = "3389" // Mapped LDAP port
	} else {
		client.Port = "389" // Standard LDAP port
	}

	return client
}

// WithCredentials sets username and password and enables TLS-backed LDAP access.
func (lc *LDAPClient) WithCredentials(username, password string) *LDAPClient {
	lc.Username = username
	lc.Password = password
	lc.UseTLS = true
	if lc.Server == "localhost" || lc.Server == "127.0.0.1" {
		lc.Port = "3636" // Mapped LDAPS port
	} else {
		lc.Port = "636" // Standard LDAPS port
	}
	return lc
}

// WithTLSCredentials is an explicit alias for secure LDAP binds.
func (lc *LDAPClient) WithTLSCredentials(username, password string) *LDAPClient {
	return lc.WithCredentials(username, password)
}

// Connect establishes a connection to LDAP server
func (lc *LDAPClient) Connect() (*ldap.Conn, error) {
	addr := net.JoinHostPort(lc.Server, lc.Port)

	if lc.UseTLS {
		conn, err := ldap.DialTLS("tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			return nil, fmt.Errorf("LDAP TLS connection failed: %w", err)
		}
		return conn, nil
	}

	conn, err := ldap.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("LDAP connection failed: %w", err)
	}
	return conn, nil
}

// CheckConnectivity tests if LDAP server is reachable
func (lc *LDAPClient) CheckConnectivity() error {
	conn, err := lc.Connect()
	if err != nil {
		return err
	}
	defer conn.Close()
	return nil
}

// Bind authenticates to the LDAP server
func (lc *LDAPClient) Bind(conn *ldap.Conn) error {
	if lc.Username == "" || lc.Password == "" {
		// Anonymous bind
		return conn.UnauthenticatedBind("")
	}

	// Authenticated bind
	dn := lc.Username
	if !strings.Contains(dn, "=") && !strings.Contains(dn, "@") {
		// If username doesn't look like a DN or UPN, assume it's a simple username
		// For AD, we might need to construct the full DN
		dn = fmt.Sprintf("%s@%s", lc.Username, lc.Server)
	}

	return conn.Bind(dn, lc.Password)
}

// SearchBase performs a search at the base DN
func (lc *LDAPClient) SearchBase(baseDN, filter string) ([]*ldap.Entry, error) {
	if lc.UseTLS && lc.Username != "" && lc.Password != "" {
		return lc.searchWithLDAPTool(baseDN, filter)
	}

	conn, err := lc.Connect()
	if err != nil {
		return nil, fmt.Errorf("LDAP connection failed: %w", err)
	}
	defer conn.Close()

	// Bind if credentials are provided
	if lc.Username != "" && lc.Password != "" {
		if err := lc.Bind(conn); err != nil {
			return nil, fmt.Errorf("LDAP bind failed for user '%s': %w", lc.Username, err)
		}
	}

	searchRequest := ldap.NewSearchRequest(
		baseDN,
		ldap.ScopeBaseObject,
		ldap.NeverDerefAliases,
		0,
		0,
		false,
		filter,
		[]string{"*"},
		nil,
	)

	sr, err := conn.Search(searchRequest)
	if err != nil {
		return nil, fmt.Errorf("LDAP search failed for base '%s' filter '%s': %w", baseDN, filter, err)
	}

	return sr.Entries, nil
}

// SearchSubtree performs a subtree search
func (lc *LDAPClient) SearchSubtree(baseDN, filter string, attrs ...string) ([]*ldap.Entry, error) {
	if lc.UseTLS && lc.Username != "" && lc.Password != "" {
		return lc.searchWithLDAPTool(baseDN, filter, attrs...)
	}

	conn, err := lc.Connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Bind if credentials are provided
	if lc.Username != "" && lc.Password != "" {
		if err := lc.Bind(conn); err != nil {
			return nil, fmt.Errorf("LDAP bind failed for user '%s': %w", lc.Username, err)
		}
	}

	if len(attrs) == 0 {
		attrs = []string{"*"}
	}

	searchRequest := ldap.NewSearchRequest(
		baseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		0,
		false,
		filter,
		attrs,
		nil,
	)

	sr, err := conn.Search(searchRequest)
	if err != nil {
		return nil, fmt.Errorf("LDAP search failed for base '%s' filter '%s': %w", baseDN, filter, err)
	}

	return sr.Entries, nil
}

func (lc *LDAPClient) searchWithLDAPTool(baseDN, filter string, attrs ...string) ([]*ldap.Entry, error) {
	args := []string{
		"-LLL",
		"-H", fmt.Sprintf("ldaps://%s:%s", lc.Server, lc.Port),
		"-x",
		"-D", lc.Username,
		"-w", lc.Password,
		"-b", baseDN,
		filter,
	}
	args = append(args, attrs...)

	cmd := exec.Command("ldapsearch", args...)
	cmd.Env = append(os.Environ(), "LDAPTLS_REQCERT=never")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ldapsearch failed for user '%s': %w\n%s", lc.Username, err, strings.TrimSpace(string(output)))
	}

	entries := make([]*ldap.Entry, 0)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "dn: ") {
			dn := strings.TrimSpace(strings.TrimPrefix(line, "dn: "))
			entries = append(entries, ldap.NewEntry(dn, map[string][]string{}))
		}
	}

	return entries, nil
}
