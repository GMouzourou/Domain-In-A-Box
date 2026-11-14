package sysvolsync

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	roleDisabled = "disabled"
	roleWriter   = "writer"
	roleStandby  = "standby"

	defaultSyncInterval = time.Minute
	defaultSSHPort      = 22

	hostKeyPath           = "/etc/ssh/dib-sysvol-hostkey"
	clientKeyPath         = "/etc/ssh/dib-sysvol-clientkey"
	clientPublicPath      = "/etc/ssh/dib-sysvol-client.pub"
	knownHostsPath        = "/etc/ssh/dib-sysvol-known-hosts"
	authorizedKeysPath    = "/etc/ssh/dib-sysvol-authorized-keys"
	sshServerConfigPath   = "/etc/ssh/sshd_config.dib-sysvol"
	sshClientConfigPath   = "/etc/ssh/dib-sysvol-client.conf"
	sysvolSourcePath      = "/var/lib/samba/sysvol"
	sysvolExportPath      = "/var/lib/samba/.dib-sysvol-export"
	sysvolExportStagePath = "/var/lib/samba/.dib-sysvol-export.stage"
	sysvolSyncStagePath   = "/var/lib/samba/.dib-sysvol-sync.stage"
	sysvolSyncStatePath   = "/var/lib/samba/.dib-sysvol-sync-state.json"
	sambaConfigPath       = "/etc/samba/smb.conf"
	sysvolRuntimePath     = "/run/dib-sysvol-sync"
)

type config struct {
	role          string
	address       string
	peerAddress   string
	writerAddress string
	sshPort       int
	interval      time.Duration
	realm         string
	dnsDomain     string
}

func loadConfig() (config, error) {
	role := strings.ToLower(strings.TrimSpace(os.Getenv("DIB_SYSVOL_SYNC_ROLE")))
	if role == "" {
		role = roleDisabled
	}
	if role != roleDisabled && role != roleWriter && role != roleStandby {
		return config{}, fmt.Errorf("DIB_SYSVOL_SYNC_ROLE must be disabled, writer, or standby")
	}

	cfg := config{role: role, interval: defaultSyncInterval, sshPort: defaultSSHPort}
	if role == roleDisabled {
		return cfg, nil
	}

	realm := strings.TrimSpace(os.Getenv("DIB_REALM"))
	if realm == "" {
		return config{}, fmt.Errorf("DIB_REALM is required when SYSVOL synchronization is enabled")
	}
	if _, err := domainDNFromRealm(realm); err != nil {
		return config{}, err
	}
	cfg.realm = realm
	cfg.dnsDomain = strings.ToLower(strings.TrimSuffix(realm, "."))

	if value := strings.TrimSpace(os.Getenv("DIB_SYSVOL_SYNC_INTERVAL_SECONDS")); value != "" {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < 10 {
			return config{}, fmt.Errorf("DIB_SYSVOL_SYNC_INTERVAL_SECONDS must be an integer of at least 10")
		}
		cfg.interval = time.Duration(seconds) * time.Second
	}
	if value := strings.TrimSpace(os.Getenv("DIB_SYSVOL_SYNC_SSH_PORT")); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return config{}, fmt.Errorf("DIB_SYSVOL_SYNC_SSH_PORT must be between 1 and 65535")
		}
		cfg.sshPort = port
	}

	if role == roleWriter {
		cfg.address = strings.TrimSpace(os.Getenv("IP"))
		cfg.peerAddress = strings.TrimSpace(os.Getenv("DIB_SYSVOL_SYNC_PEER_ADDRESS"))
		if !validIP(cfg.address) {
			return config{}, fmt.Errorf("IP must be a valid IPv4 address when SYSVOL writer synchronization is enabled")
		}
		if !validIP(cfg.peerAddress) {
			return config{}, fmt.Errorf("DIB_SYSVOL_SYNC_PEER_ADDRESS must be the standby controller's valid IPv4 address")
		}
		return cfg, nil
	}

	cfg.writerAddress = strings.TrimSpace(os.Getenv("DIB_SYSVOL_SYNC_WRITER_ADDRESS"))
	if !validIP(cfg.writerAddress) {
		return config{}, fmt.Errorf("DIB_SYSVOL_SYNC_WRITER_ADDRESS must be the writer controller's valid IPv4 address")
	}
	return cfg, nil
}

func validIP(value string) bool {
	return net.ParseIP(value).To4() != nil
}

func validateSSHPrivateKey(path string) error {
	if err := requireRegularFile(path); err != nil {
		return err
	}
	command := exec.Command("ssh-keygen", "-y", "-f", path)
	command.Stdin = strings.NewReader("")
	if output, err := command.Output(); err != nil {
		return fmt.Errorf("validate unencrypted SSH key %s: %w", path, err)
	} else if !strings.HasPrefix(string(output), "ssh-ed25519 ") {
		return fmt.Errorf("SSH key %s must be an Ed25519 key", path)
	}
	return nil
}

func validateClientPublicKey(path string) (string, error) {
	if err := requireRegularFile(path); err != nil {
		return "", err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read SYSVOL synchronization public key: %w", err)
	}
	fields := strings.Fields(string(contents))
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return "", fmt.Errorf("SYSVOL synchronization client key must be an Ed25519 public key")
	}
	if output, err := exec.Command("ssh-keygen", "-lf", path).Output(); err != nil {
		return "", fmt.Errorf("validate SYSVOL synchronization public key: %w", err)
	} else if !strings.Contains(string(output), "(ED25519)") {
		return "", fmt.Errorf("SYSVOL synchronization client key is not an Ed25519 key")
	}
	return strings.Join(fields[:2], " "), nil
}

func requireRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect required file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("required file %s is not a regular file", path)
	}
	return nil
}
