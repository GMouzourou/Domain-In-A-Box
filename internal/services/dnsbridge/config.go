package dnsbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type Controller struct {
	Hostname string `json:"hostname"`
	Address  string `json:"address"`
}

type Config struct {
	Realm           string       `json:"realm"`
	Zone            string       `json:"zone"`
	Username        string       `json:"username"`
	PasswordFile    string       `json:"passwordFile"`
	CAFile          string       `json:"caFile"`
	OwnerID         string       `json:"ownerID"`
	TXTPrefix       string       `json:"txtPrefix"`
	Controllers     []Controller `json:"controllers"`
	AllowedNetworks []string     `json:"allowedNetworks"`
	ProtectedNames  []string     `json:"protectedNames"`
	Listen          string       `json:"listen"`
	DefaultTTL      int64        `json:"defaultTTL"`
}

var (
	dnsLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	accountPattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,19}$`)
	ownerPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	prefixPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,29}-$`)
)

func LoadConfig(path string) (Config, error) {
	var cfg Config
	if path == "" {
		return cfg, errors.New("bridge configuration path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("open bridge configuration: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode bridge configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return cfg, errors.New("bridge configuration must contain exactly one JSON object")
	}
	return cfg.normalized()
}

func (cfg Config) normalized() (Config, error) {
	cfg.Realm = strings.ToUpper(strings.TrimSuffix(cfg.Realm, "."))
	cfg.Zone = normalizeName(cfg.Zone)
	if cfg.Zone == "" {
		cfg.Zone = strings.ToLower(cfg.Realm)
	}
	if !validDomain(strings.ToLower(cfg.Realm)) || !validDomain(cfg.Zone) ||
		(cfg.Zone != strings.ToLower(cfg.Realm) && !strings.HasSuffix(cfg.Zone, "."+strings.ToLower(cfg.Realm))) {
		return cfg, errors.New("zone must be the realm's DNS domain or one of its subdomains")
	}
	if !accountPattern.MatchString(cfg.Username) || !ownerPattern.MatchString(cfg.OwnerID) {
		return cfg, errors.New("a dedicated account name and a stable valid ownership ID are required")
	}
	if cfg.PasswordFile == "" || cfg.CAFile == "" {
		return cfg, errors.New("password file and trusted CA file are required")
	}
	if cfg.TXTPrefix == "" {
		cfg.TXTPrefix = "dib-edns-"
	}
	if !prefixPattern.MatchString(cfg.TXTPrefix) {
		return cfg, errors.New("TXT prefix must be a lowercase DNS label prefix ending in a hyphen")
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8888"
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	address, parseErr := netip.ParseAddr(host)
	number, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || parseErr != nil || portErr != nil || !address.IsLoopback() || number == 0 {
		return cfg, errors.New("webhook listen address must be an explicit loopback address and port")
	}
	if cfg.DefaultTTL == 0 {
		cfg.DefaultTTL = 60
	}
	if cfg.DefaultTTL < 1 || cfg.DefaultTTL > 86400 {
		return cfg, errors.New("default TTL must be between 1 and 86400 seconds")
	}
	if len(cfg.Controllers) == 0 || len(cfg.Controllers) > 2 {
		return cfg, errors.New("configure one or two domain controllers")
	}
	seen := make(map[string]bool)
	for i := range cfg.Controllers {
		controller := &cfg.Controllers[i]
		controller.Hostname = normalizeName(controller.Hostname)
		ip, err := netip.ParseAddr(controller.Address)
		if err != nil || !ip.IsGlobalUnicast() || ip.IsLoopback() ||
			!validDomain(controller.Hostname) || !strings.HasSuffix(controller.Hostname, "."+strings.ToLower(cfg.Realm)) ||
			seen[controller.Hostname] || seen[ip.String()] {
			return cfg, errors.New("controllers require unique realm-qualified hostnames and unicast IP addresses")
		}
		controller.Address = ip.String()
		seen[controller.Hostname], seen[controller.Address] = true, true
	}
	if len(cfg.AllowedNetworks) == 0 {
		return cfg, errors.New("explicit allowed address networks are required")
	}
	for _, raw := range cfg.AllowedNetworks {
		network, err := netip.ParsePrefix(raw)
		if err != nil || network.Bits() == 0 || network != network.Masked() {
			return cfg, errors.New("allowed networks must be canonical CIDRs, not unrestricted /0 networks")
		}
	}
	return cfg, nil
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

func validDomain(name string) bool {
	if len(name) > 253 || !strings.Contains(name, ".") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !dnsLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func readPassword(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect DNS credential file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0007 != 0 || info.Size() > 4096 {
		return "", errors.New("DNS credential must be a protected regular file of at most 4096 bytes")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read DNS credential file: %w", err)
	}
	password := strings.TrimSuffix(string(data), "\n")
	if password == "" || strings.ContainsAny(password, "\r\n\x00") {
		return "", errors.New("DNS credential must be nonempty and contain no line breaks or NUL bytes")
	}
	return password, nil
}
