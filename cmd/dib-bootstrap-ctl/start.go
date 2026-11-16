package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/GMouzourou/domain-in-a-box/internal/platform/config"
	"github.com/GMouzourou/domain-in-a-box/internal/platform/startup"
)

const (
	resolvConf             = "/etc/resolv.conf"
	hostsFile              = "/etc/hosts"
	upstreamNameserverFile = "/etc/bind/.dib-upstream-nameservers"
)

// start is the container entrypoint. It validates the environment, prepares
// controller state, configures every service, and then replaces itself with
// supervisord, or with the given command.
func start(args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("dib-bootstrap-ctl start must be run as root")
	}
	if err := os.Remove(coreReadyFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear core readiness: %w", err)
	}
	if err := config.LoadSecretFiles("DIB_DOMAIN_ADMIN_PASSWORD", "DIB_LDAP_PASSWORD", "STORK_DATABASE_PASSWORD"); err != nil {
		return err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read hostname: %w", err)
	}
	settings, err := startup.LoadSettings(os.Getenv, hostname)
	if err != nil {
		return err
	}
	network, err := discoverNetwork(settings.Interface)
	if err != nil {
		return err
	}
	reverseZone, err := startup.ReverseZone(network.Subnet, settings.ReverseZone)
	if err != nil {
		return err
	}

	state := startup.SambaState{Directory: "/var/lib/samba", ConfigDir: "/etc/samba"}
	upstream, err := configureResolver(settings, network.IP, state.Provisioned())
	if err != nil {
		return err
	}
	fmt.Println("Updating /etc/hosts...")
	if err := rewrite(hostsFile, func(content []byte) []byte {
		return startup.Hosts(content, network.IP, settings.Hostname, settings.DNSDomain)
	}); err != nil {
		return err
	}
	initDomain, err := state.Prepare(settings.Mode)
	if err != nil {
		return err
	}
	if initDomain {
		fmt.Println("Running first time setup...")
		if err := prepareLogDirectories(); err != nil {
			return err
		}
	}

	metrics := "off"
	if settings.MetricsEnabled {
		metrics = "on"
	}
	storkPending := os.Getenv("STORK_SERVER_HOST") != "" && !fileExists(storkMarker)
	exports := map[string]string{
		"DIB_REALM":                   settings.Realm,
		"DIB_DOMAIN":                  settings.Domain,
		"DIB_CONTROLLER_MODE":         settings.Mode,
		"DIB_SAMBA_METRICS_ENABLED":   metrics,
		"DIB_SAMBA_METRICS_PORT":      settings.MetricsPort,
		"INIT_DOMAIN":                 flag(initDomain),
		"DNS_DOMAIN":                  settings.DNSDomain,
		"CONTAINER_HOSTNAME":          settings.Hostname,
		"IP":                          network.IP,
		"SUBNET":                      network.Subnet,
		"REVERSE_ZONE":                reverseZone,
		"GATEWAY":                     network.Gateway,
		"DIB_UPSTREAM_NAMESERVERS":    strings.Join(upstream, " "),
		"DIB_BOOTSTRAP_PENDING":       flag(state.BootstrapPending()),
		"DIB_STORK_BOOTSTRAP_PENDING": flag(storkPending),
	}
	for key, value := range exports {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
	}

	if err := runController("dib-identity-core-ctl", "configure"); err != nil {
		return err
	}
	if initDomain {
		if err := state.MarkProvisioned(); err != nil {
			return err
		}
	}
	for _, controller := range []string{"dib-network-core-ctl", "dib-observability-ctl", "dib-sysvol-sync-ctl"} {
		if err := runController(controller, "configure"); err != nil {
			return err
		}
	}

	if len(args) > 0 {
		fmt.Printf("Executing provided command: %s\n", strings.Join(args, " "))
		return execute(args)
	}
	for _, step := range []struct{ message, controller string }{
		{"Validating Samba config...", "dib-identity-core-ctl"},
		{"Validating network service configuration...", "dib-network-core-ctl"},
		{"Validating Stork environment files...", "dib-observability-ctl"},
		{"Validating SYSVOL synchronization configuration...", "dib-sysvol-sync-ctl"},
	} {
		fmt.Println(step.message)
		if err := runController(step.controller, "validate"); err != nil {
			return err
		}
	}
	fmt.Println("Launching supervisord...")
	return execute([]string{"/usr/bin/supervisord", "-c", "/etc/supervisord.conf"})
}

func discoverNetwork(device string) (startup.Network, error) {
	addresses, err := exec.Command("ip", "-j", "addr", "show", "dev", device).Output()
	if err != nil {
		return startup.Network{}, fmt.Errorf("Failed to determine an IPv4 address for DIB_INTERFACE=%s. Check that the interface exists and is configured.", device)
	}
	routes, err := exec.Command("ip", "-j", "route", "show", "dev", device).Output()
	if err != nil {
		return startup.Network{}, fmt.Errorf("read routes for %s: %w", device, err)
	}
	network, err := startup.ParseNetwork(addresses, routes)
	if err != nil {
		return startup.Network{}, err
	}
	if network.IP == "" {
		return startup.Network{}, fmt.Errorf("Failed to determine an IPv4 address for DIB_INTERFACE=%s. Check that the interface exists and is configured.", device)
	}
	if network.Subnet == "" || network.Gateway == "" {
		return startup.Network{}, fmt.Errorf("Failed to determine subnet/gateway for DIB_INTERFACE=%s. Ensure the selected interface has a connected subnet and a route via your LAN gateway.", device)
	}
	return network, nil
}

// configureResolver points the controller at its own DNS service, or at the
// existing controller while a new secondary joins. It returns the platform
// resolvers that the DNS service forwards cluster names to.
func configureResolver(settings startup.Settings, ip string, provisioned bool) ([]string, error) {
	content, err := os.ReadFile(resolvConf)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", resolvConf, err)
	}
	upstream, search := startup.ParseResolvConf(content, ip)
	if settings.ClusterDomain != "" {
		if len(upstream) > 0 {
			if err := os.WriteFile(upstreamNameserverFile, []byte(strings.Join(upstream, " ")+"\n"), 0o644); err != nil {
				return nil, fmt.Errorf("record cluster DNS resolver: %w", err)
			}
		} else if saved, err := os.ReadFile(upstreamNameserverFile); err == nil && len(strings.Fields(string(saved))) > 0 {
			// Container restarts retain the resolver file rewritten below.
			upstream = strings.Fields(string(saved))
		} else {
			return nil, errors.New("Cannot discover the cluster DNS resolver. Recreate the pod to restore its platform resolv.conf.")
		}
	}
	server := ip
	if settings.Mode == "secondary" && !provisioned {
		server = settings.JoinServer
	}
	fmt.Println("Writing /etc/resolv.conf...")
	return upstream, writeInPlace(resolvConf, startup.ResolvConf(append([]string{settings.DNSDomain}, search...), server))
}

func rewrite(path string, change func([]byte) []byte) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return writeInPlace(path, change(content))
}

// writeInPlace truncates rather than replaces the file because the container
// runtime bind-mounts it.
func writeInPlace(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func prepareLogDirectories() error {
	steps := []func() error{
		func() error { return os.MkdirAll("/var/log/kea", 0o755) },
		func() error { return os.MkdirAll("/var/log/samba/cores", 0o700) },
		func() error {
			file, err := os.OpenFile("/var/log/kea/kea-dhcp4.log", os.O_CREATE|os.O_WRONLY, 0o664)
			if err != nil {
				return err
			}
			return file.Close()
		},
		// Kea runs as _kea and must write its own log, which the network
		// controller keeps permissive on every start.
		func() error { return exec.Command("chown", "-R", "root:_kea", "/var/log/kea").Run() },
		func() error { return os.Chmod("/var/log/kea", 0o775|os.ModeSetgid) },
		func() error { return os.Chmod("/var/log/kea/kea-dhcp4.log", 0o664) },
		func() error { return os.Chmod("/var/log/samba/cores", 0o700) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return fmt.Errorf("prepare log directories: %w", err)
		}
	}
	return nil
}

func runController(name, action string) error {
	command := exec.Command(name, action)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, action, err)
	}
	return nil
}

func execute(args []string) error {
	path, err := exec.LookPath(args[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, args, os.Environ())
}

func flag(value bool) string {
	if value {
		return "TRUE"
	}
	return "FALSE"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
