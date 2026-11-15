// Package startup validates the controller's start-up inputs and derives the
// values that every service configurator reads from its environment.
package startup

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// Settings are the validated controller start-up inputs.
type Settings struct {
	Realm          string
	Domain         string
	DNSDomain      string
	Mode           string
	Interface      string
	JoinServer     string
	Hostname       string
	ReverseZone    string
	ClusterDomain  string
	MetricsEnabled bool
	MetricsPort    string
}

// LoadSettings reads and validates the controller environment.
func LoadSettings(getenv func(string) string, hostname string) (Settings, error) {
	for _, key := range []string{"DIB_REALM", "DIB_INTERFACE", "DIB_DHCP_POOL", "DIB_DNS_FORWARDERS"} {
		if getenv(key) == "" {
			return Settings{}, fmt.Errorf("environment variable %s is not set", key)
		}
	}
	realm := strings.ToUpper(getenv("DIB_REALM"))
	s := Settings{
		Realm:          realm,
		Domain:         getenv("DIB_DOMAIN"),
		DNSDomain:      strings.ToLower(realm),
		Mode:           getenv("DIB_CONTROLLER_MODE"),
		Interface:      getenv("DIB_INTERFACE"),
		JoinServer:     getenv("DIB_JOIN_SERVER"),
		Hostname:       strings.ToLower(hostname),
		ReverseZone:    getenv("DIB_REVERSE_ZONE"),
		ClusterDomain:  getenv("DIB_CLUSTER_DOMAIN"),
		MetricsEnabled: getenv("DIB_SAMBA_METRICS_ENABLED") == "true",
		MetricsPort:    getenv("DIB_SAMBA_METRICS_PORT"),
	}
	if s.Domain == "" {
		// The NetBIOS name defaults to the realm's first label and is only used at provisioning.
		s.Domain, _, _ = strings.Cut(realm, ".")
		if len(s.Domain) > 15 {
			s.Domain = s.Domain[:15]
		}
	}
	if s.Mode == "" {
		s.Mode = "primary"
	}
	if s.MetricsPort == "" {
		s.MetricsPort = "9922"
	}
	switch s.Mode {
	case "primary":
	case "secondary":
		if s.JoinServer == "" {
			return Settings{}, errors.New("DIB_JOIN_SERVER is required for a secondary controller")
		}
		if len(s.Hostname) > 15 {
			return Settings{}, fmt.Errorf("A secondary controller hostname must be no more than 15 characters, got %s", s.Hostname)
		}
	default:
		return Settings{}, fmt.Errorf("DIB_CONTROLLER_MODE must be primary or secondary, got %s", s.Mode)
	}
	return s, nil
}

// ReverseZone returns the IPv4 reverse DNS zone for a subnet. An explicit
// override wins. Prefixes longer than /24 share their enclosing /24 zone;
// other prefixes must be octet aligned.
func ReverseZone(subnet, override string) (string, error) {
	if override != "" {
		return strings.ToLower(strings.TrimSuffix(override, ".")), nil
	}
	unsupported := fmt.Errorf("Cannot derive a reverse DNS zone for subnet %s; set DIB_REVERSE_ZONE, for example 168.192.in-addr.arpa", subnet)
	prefix, err := netip.ParsePrefix(subnet)
	if err != nil || !prefix.Addr().Is4() {
		return "", unsupported
	}
	var octets int
	switch bits := prefix.Bits(); {
	case bits == 8:
		octets = 1
	case bits == 16:
		octets = 2
	case bits >= 24:
		octets = 3
	default:
		return "", unsupported
	}
	address := prefix.Addr().As4()
	labels := make([]string, 0, octets+1)
	for i := octets - 1; i >= 0; i-- {
		labels = append(labels, fmt.Sprint(address[i]))
	}
	return strings.Join(append(labels, "in-addr.arpa"), "."), nil
}

// Network is the controller's LAN addressing, discovered from its interface.
type Network struct {
	IP      string
	Subnet  string
	Gateway string
}

// ParseNetwork reads `ip -j addr show dev X` and `ip -j route show dev X`
// output. It takes the first IPv4 address, the first link-scope route as the
// connected subnet, and the first gateway.
func ParseNetwork(addresses, routes []byte) (Network, error) {
	var links []struct {
		AddrInfo []struct {
			Family string `json:"family"`
			Local  string `json:"local"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(addresses, &links); err != nil {
		return Network{}, fmt.Errorf("parse interface addresses: %w", err)
	}
	var table []struct {
		Dst     string `json:"dst"`
		Scope   string `json:"scope"`
		Gateway string `json:"gateway"`
	}
	if err := json.Unmarshal(routes, &table); err != nil {
		return Network{}, fmt.Errorf("parse interface routes: %w", err)
	}
	var network Network
	for _, link := range links {
		for _, address := range link.AddrInfo {
			if network.IP == "" && address.Family == "inet" {
				network.IP = address.Local
			}
		}
	}
	for _, route := range table {
		if network.Subnet == "" && route.Scope == "link" {
			network.Subnet = route.Dst
		}
		if network.Gateway == "" && route.Gateway != "" {
			network.Gateway = route.Gateway
		}
	}
	return network, nil
}

// ParseResolvConf returns the nameservers other than self and the domains on
// the first search line.
func ParseResolvConf(content []byte, self string) (nameservers, search []string) {
	foundSearch := false
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		switch {
		case strings.HasPrefix(line, "nameserver") && len(fields) > 1 && fields[1] != self:
			nameservers = append(nameservers, fields[1])
		case strings.HasPrefix(line, "search") && !foundSearch:
			foundSearch = true
			for _, domain := range fields[1:] {
				// Docker writes "search ." when the host has no search domains.
				if domain != "." {
					search = append(search, domain)
				}
			}
		}
	}
	return nameservers, search
}

// ResolvConf renders the controller's resolver file.
func ResolvConf(search []string, nameserver string) []byte {
	return fmt.Appendf(nil, "search %s\nnameserver %s\n", strings.Join(search, " "), nameserver)
}

// Hosts maps the controller name to its LAN address. It drops other entries
// for the address or the name, such as an internal database network, so the
// controller name always resolves to its LAN address.
func Hosts(content []byte, ip, hostname, dnsDomain string) []byte {
	var kept strings.Builder
	if text := strings.TrimSuffix(string(content), "\n"); text != "" {
		for _, line := range strings.Split(text, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && fields[0] == ip && strings.HasPrefix(line, ip) || namesHost(line, fields, hostname) {
				continue
			}
			kept.WriteString(line + "\n")
		}
	}
	fmt.Fprintf(&kept, "%s    %s.%s    %s\n", ip, hostname, dnsDomain, hostname)
	return []byte(kept.String())
}

func namesHost(line string, fields []string, hostname string) bool {
	for i, field := range fields {
		// The name must follow whitespace, so a line's leading address never matches.
		if field == hostname && (i > 0 || strings.TrimLeft(line, " \t") != line) {
			return true
		}
	}
	return false
}

// SambaState names the persistent markers that guard controller identity.
type SambaState struct {
	Directory string
	ConfigDir string
}

func (s SambaState) marker(name string) string { return filepath.Join(s.Directory, name) }

// BootstrapPending reports whether the one-time core bootstrap is incomplete.
func (s SambaState) BootstrapPending() bool {
	return !exists(s.marker(".dib-bootstrap-complete"))
}

// Prepare keeps Samba configuration on the persistent volume and refuses to
// start on state that does not match the requested controller identity. It
// returns true when the domain must be provisioned or joined.
func (s SambaState) Prepare(mode string) (bool, error) {
	persistentConfig := s.marker("etc-samba")
	if err := os.MkdirAll(persistentConfig, 0o755); err != nil {
		return false, fmt.Errorf("create persistent Samba configuration directory: %w", err)
	}
	if info, err := os.Lstat(s.ConfigDir); err != nil || info.Mode()&os.ModeSymlink == 0 {
		if err := os.RemoveAll(s.ConfigDir); err != nil {
			return false, fmt.Errorf("remove image Samba configuration: %w", err)
		}
		if err := os.Symlink(persistentConfig, s.ConfigDir); err != nil {
			return false, fmt.Errorf("link Samba configuration to the data volume: %w", err)
		}
	}

	provisioned := s.Provisioned()
	modeMarker := s.marker(".dib-controller-mode")
	recorded, err := os.ReadFile(modeMarker)
	switch {
	case err == nil:
		if previous := strings.TrimSpace(string(recorded)); previous != mode {
			return false, fmt.Errorf("Controller mode changed from %s to %s on existing data. Use a fresh data volume to change controller identity.", previous, mode)
		}
	case !errors.Is(err, os.ErrNotExist):
		return false, fmt.Errorf("read recorded controller mode: %w", err)
	case provisioned && mode != "primary":
		return false, errors.New("Existing Samba data has no recorded controller mode; it cannot be adopted as a secondary controller. Use a fresh data volume.")
	default:
		if err := os.WriteFile(modeMarker, []byte(mode+"\n"), 0o644); err != nil {
			return false, fmt.Errorf("record controller mode: %w", err)
		}
	}

	smbConf := filepath.Join(s.ConfigDir, "smb.conf")
	if mode == "secondary" && !provisioned {
		if exists(s.marker(".dib-secondary-join-started")) || exists(smbConf) {
			return false, errors.New("A previous secondary join may have left partial Samba state. Do not retry on this volume; inspect it and use a fresh volume if rebuilding.")
		}
		if err := os.WriteFile(s.marker(".dib-secondary-join-started"), nil, 0o644); err != nil {
			return false, fmt.Errorf("record secondary join start: %w", err)
		}
	}
	if provisioned && !exists(smbConf) {
		return false, fmt.Errorf("Samba state exists (%s) but %s is missing. Restore %s from backup or remove Samba state to re-provision.", s.marker(".dib-provisioned"), smbConf, s.Directory)
	}
	return !provisioned, nil
}

// MarkProvisioned records a completed provision or join.
func (s SambaState) MarkProvisioned() error {
	if err := os.WriteFile(s.marker(".dib-provisioned"), nil, 0o644); err != nil {
		return fmt.Errorf("record Samba provisioning: %w", err)
	}
	if err := os.Remove(s.marker(".dib-secondary-join-started")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear secondary join marker: %w", err)
	}
	return nil
}

// Provisioned reports whether Samba has been provisioned or joined.
func (s SambaState) Provisioned() bool { return exists(s.marker(".dib-provisioned")) }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
