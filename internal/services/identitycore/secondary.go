package identitycore

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/GMouzourou/domain-in-a-box/internal/platform/bootstrap"
)

const domainFunctionalLevel = "2016"

func joinSecondary(ctx context.Context) error {
	realm, err := environment("DIB_REALM")
	if err != nil {
		return err
	}
	joinServer, err := environment("DIB_JOIN_SERVER")
	if err != nil {
		return err
	}
	if net.ParseIP(joinServer).To4() == nil {
		return fmt.Errorf("DIB_JOIN_SERVER must be an IPv4 address, got %q", joinServer)
	}
	password, err := environment("DIB_DOMAIN_ADMIN_PASSWORD")
	if err != nil {
		return err
	}
	hostname, err := environment("CONTAINER_HOSTNAME")
	if err != nil {
		return err
	}
	if len(hostname) > 15 {
		return fmt.Errorf("secondary controller hostname must be no more than 15 characters, got %q", hostname)
	}
	ip, err := environment("IP")
	if err != nil {
		return err
	}
	if net.ParseIP(ip).To4() == nil {
		return fmt.Errorf("IP must be an IPv4 address, got %q", ip)
	}
	interfaceName, err := environment("DIB_INTERFACE")
	if err != nil {
		return err
	}
	metrics := os.Getenv("DIB_SAMBA_METRICS_ENABLED")
	if metrics == "" {
		metrics = "off"
	}
	args := secondaryJoinArguments(realm, joinServer, ip, interfaceName, metrics)
	if err := runWithInput(ctx, password+"\n", "samba-tool", args...); err != nil {
		return err
	}
	settings := []struct{ key, value string }{
		{"bind interfaces only", "yes"},
		{"interfaces", "lo " + interfaceName},
		{"dns forwarder", ip + ":5353"},
		{"rpc server dynamic port range", "49152-49252"},
		{"idmap_ldb:use rfc2307", "yes"},
		{"ad dc functional level", domainFunctionalLevel},
		{"ntp signd socket directory", "/var/lib/samba/ntp_signd"},
		{"log file", "/var/log/samba/%m.log"},
		{"max log size", "10000"},
		{"smbd profiling level", metrics},
	}
	for _, setting := range settings {
		if err := setSambaParameter("/etc/samba/smb.conf", setting.key, setting.value); err != nil {
			return err
		}
	}
	if err := run(ctx, "testparm", "-s"); err != nil {
		return fmt.Errorf("validate joined Samba configuration: %w", err)
	}
	for _, setting := range settings {
		value, err := exec.CommandContext(ctx, "testparm", "-s", "--parameter-name="+setting.key).Output()
		if err != nil {
			return fmt.Errorf("verify Samba setting %q: %w", setting.key, err)
		}
		actual := strings.TrimSpace(string(value))
		matches := actual == setting.value
		if setting.value == "yes" || setting.value == "no" || setting.value == "on" || setting.value == "off" {
			matches = strings.EqualFold(actual, setting.value)
		}
		if !matches {
			return fmt.Errorf("Samba setting %q is %q, want %q", setting.key, actual, setting.value)
		}
	}
	return nil
}

func bootstrapSecondary(ctx context.Context, reverseZone, hostname, address, password string) error {
	recordName, err := reverseRecordName(address, reverseZone)
	if err != nil {
		return err
	}
	dnsDomain, err := environment("DNS_DOMAIN")
	if err != nil {
		return err
	}
	return bootstrap.RunOnce(ctx, "/var/lib/samba/.dib-reverse-ptr-created", func(ctx context.Context) error {
		return runWithInput(ctx, password+"\n", "samba-tool", "dns", "add", "127.0.0.1", reverseZone, recordName, "PTR", hostname+"."+dnsDomain, "-U", "Administrator")
	})
}

func secondaryJoinArguments(realm, joinServer, ip, interfaceName, metrics string) []string {
	return []string{
		"domain", "join", realm, "DC",
		"--server=" + joinServer,
		"--dns-backend=SAMBA_INTERNAL",
		"--option=bind interfaces only = yes",
		"--option=interfaces = lo " + interfaceName,
		"--option=dns forwarder = " + ip + ":5353",
		"--option=rpc server dynamic port range = 49152-49252",
		"--option=idmap_ldb:use rfc2307 = yes",
		"--option=ad dc functional level = " + domainFunctionalLevel,
		"--option=smbd profiling level = " + metrics,
		"-U", "Administrator",
	}
}

func runWithInput(ctx context.Context, input, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = strings.NewReader(input)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	return nil
}

func setSambaParameter(path, key, value string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Samba configuration: %w", err)
	}
	directive := regexp.MustCompile(`(?mi)^[ \t]*` + regexp.QuoteMeta(key) + `[ \t]*=.*$`)
	matches := directive.FindAllIndex(contents, -1)
	switch len(matches) {
	case 0:
		global := regexp.MustCompile(`(?mi)^[ \t]*\[global\][ \t]*$`)
		location := global.FindIndex(contents)
		if location == nil {
			return fmt.Errorf("Samba configuration does not contain a global section")
		}
		lineEnd := strings.IndexByte(string(contents[location[1]:]), '\n')
		if lineEnd < 0 {
			contents = append(contents, []byte("\n"+key+" = "+value+"\n")...)
		} else {
			insertAt := location[1] + lineEnd + 1
			updated := make([]byte, 0, len(contents)+len(key)+len(value)+5)
			updated = append(updated, contents[:insertAt]...)
			updated = append(updated, []byte(key+" = "+value+"\n")...)
			updated = append(updated, contents[insertAt:]...)
			contents = updated
		}
	case 1:
		contents = directive.ReplaceAll(contents, []byte(key+" = "+value))
	default:
		return fmt.Errorf("Samba configuration contains multiple %q directives", key)
	}
	return writeAtomically(path, contents)
}

func enforceSecondarySYSVOLReadOnly(ctx context.Context) error {
	for _, share := range []string{"sysvol", "netlogon"} {
		if err := setSambaShareReadOnly("/etc/samba/smb.conf", share); err != nil {
			return fmt.Errorf("configure standby Samba [%s] share read-only: %w", share, err)
		}
		value, err := exec.CommandContext(ctx, "testparm", "-s", "--section-name="+share, "--parameter-name=read only").Output()
		if err != nil {
			return fmt.Errorf("verify standby Samba [%s] share read-only: %w", share, err)
		}
		if !strings.EqualFold(strings.TrimSpace(string(value)), "yes") {
			return fmt.Errorf("standby Samba [%s] share has effective read only setting %q, want yes", share, strings.TrimSpace(string(value)))
		}
	}
	return nil
}

func setSambaShareReadOnly(path, share string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Samba configuration: %w", err)
	}
	updated, err := rewriteSambaShareReadOnly(contents, share)
	if err != nil {
		return err
	}
	if string(updated) == string(contents) {
		return nil
	}
	return writeAtomically(path, updated)
}

func rewriteSambaShareReadOnly(contents []byte, share string) ([]byte, error) {
	share = strings.ToLower(strings.TrimSpace(share))
	lines := strings.Split(string(contents), "\n")
	updated := make([]string, 0, len(lines)+1)
	section := ""
	foundShare := false
	inShare := false
	settingAdded := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inShare && !settingAdded {
				updated = append(updated, "read only = yes")
				settingAdded = true
			}
			section = strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			inShare = section == share
			if inShare {
				if foundShare {
					return nil, fmt.Errorf("Samba configuration contains multiple [%s] sections", share)
				}
				foundShare = true
			}
			updated = append(updated, line)
			continue
		}

		if inShare {
			key, _, ok := strings.Cut(trimmed, "=")
			if ok && (strings.EqualFold(strings.TrimSpace(key), "read only") ||
				strings.EqualFold(strings.TrimSpace(key), "writeable") ||
				strings.EqualFold(strings.TrimSpace(key), "writable")) {
				if !settingAdded {
					indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
					updated = append(updated, indent+"read only = yes")
					settingAdded = true
				}
				continue
			}
		}
		updated = append(updated, line)
	}

	if !foundShare {
		return nil, fmt.Errorf("Samba configuration does not contain a [%s] section", share)
	}
	if inShare && !settingAdded {
		if len(updated) > 0 && updated[len(updated)-1] == "" {
			updated = append(updated[:len(updated)-1], "read only = yes", "")
		} else {
			updated = append(updated, "read only = yes")
		}
	}
	return []byte(strings.Join(updated, "\n")), nil
}
