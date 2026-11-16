package sysvolsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	platformlog "github.com/GMouzourou/domain-in-a-box/internal/platform/log"
	"github.com/GMouzourou/domain-in-a-box/internal/platform/process"
	"golang.org/x/sys/unix"
)

type Runner struct {
	log *platformlog.Logger
}

func NewRunner() *Runner {
	return &Runner{log: platformlog.New("sysvol-sync")}
}

func (r *Runner) Name() string { return "sysvol-sync" }

func (r *Runner) Configure(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.role == roleDisabled {
		return nil
	}
	if err := os.MkdirAll(sysvolRuntimePath, 0o700); err != nil {
		return fmt.Errorf("create SYSVOL runtime directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(sysvolSyncStagePath), 0o700); err != nil {
		return fmt.Errorf("create SYSVOL data directory: %w", err)
	}
	if cfg.role == roleWriter {
		if err := validateSSHPrivateKey(hostKeyPath); err != nil {
			return fmt.Errorf("validate SYSVOL SSH host key: %w", err)
		}
		return configureWriter(ctx, cfg)
	}
	return configureStandby(ctx, cfg)
}

func configureWriter(ctx context.Context, cfg config) error {
	if err := os.MkdirAll("/run/sshd", 0o755); err != nil {
		return fmt.Errorf("create OpenSSH privilege-separation directory: %w", err)
	}
	publicKey, err := validateClientPublicKey(clientPublicPath)
	if err != nil {
		return err
	}
	if err := writeAuthorizedKeys(authorizedKeysPath, publicKey); err != nil {
		return fmt.Errorf("write restricted SYSVOL SSH authorized key: %w", err)
	}

	serverConfig := strings.Join([]string{
		"Port " + strconv.Itoa(cfg.sshPort),
		"AddressFamily inet",
		"ListenAddress " + cfg.address,
		"HostKey " + hostKeyPath,
		"PidFile /run/sshd-dib-sysvol.pid",
		"AuthorizedKeysFile " + authorizedKeysPath,
		"AllowUsers dibsysvolsync@" + cfg.peerAddress,
		"AuthenticationMethods publickey",
		"PubkeyAuthentication yes",
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"PermitEmptyPasswords no",
		"PermitRootLogin no",
		"AllowAgentForwarding no",
		"AllowTcpForwarding no",
		"X11Forwarding no",
		"PermitTunnel no",
		"PermitTTY no",
		"PermitUserRC no",
		"PermitUserEnvironment no",
		"UsePAM no",
		"StrictModes yes",
		"AllowStreamLocalForwarding no",
		"ForceCommand /usr/bin/rrsync -ro " + sysvolExportPath,
		"LogLevel VERBOSE",
		"",
	}, "\n")
	if err := writeAtomic(sshServerConfigPath, []byte(serverConfig), 0o600); err != nil {
		return fmt.Errorf("write SYSVOL SSH server configuration: %w", err)
	}
	if err := runCommand(ctx, "/usr/sbin/sshd", "-t", "-f", sshServerConfigPath); err != nil {
		return err
	}
	if err := ensureDirectory(sysvolExportPath); err != nil {
		return err
	}
	return nil
}

func writeAuthorizedKeys(path, publicKey string) error {
	return writeAtomic(path, []byte("restrict "+publicKey+"\n"), 0o644)
}

func configureStandby(ctx context.Context, cfg config) error {
	if err := validateSSHPrivateKey(clientKeyPath); err != nil {
		return fmt.Errorf("validate SYSVOL SSH client key: %w", err)
	}
	if err := requireRegularFile(knownHostsPath); err != nil {
		return err
	}
	hostReference := cfg.writerAddress
	if cfg.sshPort != 22 {
		hostReference = "[" + cfg.writerAddress + "]:" + strconv.Itoa(cfg.sshPort)
	}
	if err := runCommand(ctx, "ssh-keygen", "-F", hostReference, "-f", knownHostsPath); err != nil {
		return fmt.Errorf("SYSVOL writer host key is not pinned in %s: %w", knownHostsPath, err)
	}
	clientConfig := strings.Join([]string{
		"Host dib-sysvol-writer",
		"  HostName " + cfg.writerAddress,
		"  Port " + strconv.Itoa(cfg.sshPort),
		"  User dibsysvolsync",
		"  IdentityFile " + clientKeyPath,
		"  IdentitiesOnly yes",
		"  UserKnownHostsFile " + knownHostsPath,
		"  StrictHostKeyChecking yes",
		"  HostKeyAlgorithms ssh-ed25519",
		"  BatchMode yes",
		"  ConnectTimeout 10",
		"  ServerAliveInterval 15",
		"  ServerAliveCountMax 3",
		"",
	}, "\n")
	if err := writeAtomic(sshClientConfigPath, []byte(clientConfig), 0o600); err != nil {
		return fmt.Errorf("write SYSVOL SSH client configuration: %w", err)
	}
	return nil
}

func (r *Runner) Bootstrap(context.Context) error { return nil }

func (r *Runner) Validate(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.role == roleDisabled {
		return nil
	}
	if cfg.role == roleWriter {
		if err := runCommand(ctx, "/usr/sbin/sshd", "-t", "-f", sshServerConfigPath); err != nil {
			return err
		}
		return nil
	}
	if _, err := os.Stat(sshClientConfigPath); err != nil {
		return fmt.Errorf("SYSVOL SSH client configuration is missing: %w", err)
	}
	return nil
}

func (r *Runner) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.role == roleDisabled {
		r.log.Infof("SYSVOL synchronization is disabled")
		return nil
	}
	r.log.Infof("starting SYSVOL %s worker with interval %s", cfg.role, cfg.interval)
	if err := r.runOnce(ctx, cfg); err != nil {
		r.log.Errorf("initial SYSVOL %s operation failed: %v", cfg.role, err)
	}

	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.runOnce(ctx, cfg); err != nil {
				r.log.Errorf("SYSVOL %s operation failed; the last published tree is retained: %v", cfg.role, err)
			}
		}
	}
}

func (r *Runner) RunSSHServer(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.role != roleWriter {
		return nil
	}
	return process.Replace("/usr/sbin/sshd", "-D", "-e", "-f", sshServerConfigPath)
}

func (r *Runner) Health(context.Context) error { return nil }

func (r *Runner) runOnce(ctx context.Context, cfg config) error {
	if err := os.MkdirAll(sysvolRuntimePath, 0o700); err != nil {
		return fmt.Errorf("create SYSVOL runtime directory: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(sysvolRuntimePath, "sync.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open SYSVOL synchronization lock: %w", err)
	}
	// Closing the descriptor releases the lock.
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("another SYSVOL synchronization operation is active: %w", err)
	}

	if cfg.role == roleWriter {
		return r.exportOnce(ctx, cfg)
	}
	return r.pullOnce(ctx, cfg)
}

func (r *Runner) exportOnce(ctx context.Context, cfg config) error {
	activePath, err := sambaSysvolPath(ctx)
	if err != nil {
		return err
	}
	if activePath != sysvolSourcePath {
		return fmt.Errorf("Samba SYSVOL path is %q, expected %q", activePath, sysvolSourcePath)
	}
	if err := validateDomainTree(activePath, cfg.dnsDomain); err != nil {
		return err
	}
	before, err := buildTreeManifest(activePath, false)
	if err != nil {
		return err
	}
	adBefore, err := readADGPOVersions(ctx, cfg.realm)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(sysvolExportStagePath); err != nil {
		return fmt.Errorf("clear prior SYSVOL export staging tree: %w", err)
	}
	if err := os.Mkdir(sysvolExportStagePath, 0o700); err != nil {
		return fmt.Errorf("create SYSVOL export staging tree: %w", err)
	}
	if err := runCommand(ctx,
		"rsync",
		"-rlt",
		"--delete",
		"--no-owner",
		"--no-group",
		"--no-perms",
		"--timeout=60",
		activePath+"/",
		sysvolExportStagePath+"/",
	); err != nil {
		return err
	}
	if err := normalizeExportPermissions(sysvolExportStagePath); err != nil {
		return err
	}
	after, err := buildTreeManifest(activePath, false)
	if err != nil {
		return err
	}
	staged, err := buildTreeManifest(sysvolExportStagePath, false)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("writer SYSVOL changed during export; retrying on the next interval")
	}
	if !reflect.DeepEqual(after, staged) {
		return fmt.Errorf("staged writer SYSVOL does not match the live tree")
	}
	adAfter, err := readADGPOVersions(ctx, cfg.realm)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(adBefore, adAfter) {
		return fmt.Errorf("writer AD GPO versions changed during export; retrying on the next interval")
	}
	sysvolVersions, err := readSYSVOLGPOVersions(filepath.Join(sysvolExportStagePath, cfg.dnsDomain))
	if err != nil {
		return err
	}
	manifest := ExportManifest{
		Version:              1,
		GeneratedAt:          time.Now().UTC(),
		Entries:              staged,
		GPOVersionMismatches: findGPOVersionMismatches(adAfter, sysvolVersions),
	}
	if err := writeManifest(filepath.Join(sysvolExportStagePath, manifestName), manifest); err != nil {
		return err
	}
	if err := publishDirectory(sysvolExportStagePath, sysvolExportPath); err != nil {
		return err
	}
	if err := os.RemoveAll(sysvolExportStagePath); err != nil {
		r.log.Errorf("remove prior SYSVOL export after publication: %v", err)
	}
	for _, mismatch := range manifest.GPOVersionMismatches {
		r.log.Warnf("writer GPO version mismatch: %s AD=%s SYSVOL=%s", mismatch.GUID, optionalVersion(mismatch.ADVersion), optionalVersion(mismatch.SYSVOLVersion))
	}
	r.log.Infof("published verified SYSVOL export with %d entries", len(manifest.Entries))
	return nil
}

func (r *Runner) pullOnce(ctx context.Context, cfg config) error {
	if err := os.MkdirAll(sysvolSourcePath, 0o700); err != nil {
		return fmt.Errorf("create active SYSVOL directory: %w", err)
	}
	if err := os.RemoveAll(sysvolSyncStagePath); err != nil {
		return fmt.Errorf("clear prior SYSVOL synchronization staging tree: %w", err)
	}
	if err := os.Mkdir(sysvolSyncStagePath, 0o700); err != nil {
		return fmt.Errorf("create SYSVOL synchronization staging tree: %w", err)
	}
	if err := runCommand(ctx,
		"rsync",
		"-rlt",
		"--delete",
		"--no-owner",
		"--no-group",
		"--no-perms",
		"--timeout=60",
		"-e", "ssh -F "+sshClientConfigPath,
		"dib-sysvol-writer:/",
		sysvolSyncStagePath+"/",
	); err != nil {
		return err
	}
	manifestPath := filepath.Join(sysvolSyncStagePath, manifestName)
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return err
	}
	if err := verifyTree(sysvolSyncStagePath, manifest); err != nil {
		return err
	}
	if err := validateDomainTree(sysvolSyncStagePath, cfg.dnsDomain); err != nil {
		return err
	}
	if err := os.Remove(manifestPath); err != nil {
		return fmt.Errorf("remove transfer manifest from staged SYSVOL: %w", err)
	}

	state, err := loadSyncState(sysvolSyncStatePath)
	if err != nil {
		return err
	}
	state, err = resolvePendingState(state, sysvolSyncStatePath, sysvolSourcePath)
	if err != nil {
		return err
	}
	activeEntries, err := buildTreeManifest(sysvolSourcePath, false)
	if err != nil {
		return err
	}
	var drift []string
	if state.Current != nil {
		drift = changedPaths(activeEntries, state.Current.Entries)
		for _, path := range drift {
			r.log.Warnf("standby SYSVOL change will be overwritten: %s", path)
		}
	}
	for _, mismatch := range manifest.GPOVersionMismatches {
		r.log.Warnf("writer GPO version mismatch: %s AD=%s SYSVOL=%s", mismatch.GUID, optionalVersion(mismatch.ADVersion), optionalVersion(mismatch.SYSVOLVersion))
	}

	if state.Current != nil && reflect.DeepEqual(activeEntries, manifest.Entries) {
		aclErr := runSambaTool(ctx, "ntacl", "sysvolcheck")
		if aclErr == nil {
			state.Current = &manifest
			if err := writeSyncState(sysvolSyncStatePath, state); err != nil {
				return err
			}
			if err := os.RemoveAll(sysvolSyncStagePath); err != nil {
				return fmt.Errorf("remove unchanged SYSVOL staging tree: %w", err)
			}
			r.log.Infof("verified SYSVOL is current; no directory publish required")
			return nil
		}
		r.log.Warnf("active SYSVOL ACL check failed; rebuilding the staged tree: %v", aclErr)
	}

	configPath, err := writeStagedSambaConfig(sysvolSyncStagePath, cfg.dnsDomain)
	if err != nil {
		return err
	}
	defer os.Remove(configPath)
	if err := runSambaToolWithConfig(ctx, configPath, "ntacl", "sysvolreset"); err != nil {
		return err
	}
	if err := runSambaToolWithConfig(ctx, configPath, "ntacl", "sysvolcheck"); err != nil {
		return err
	}
	stagedEntries, err := buildTreeManifest(sysvolSyncStagePath, false)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(stagedEntries, manifest.Entries) {
		return fmt.Errorf("SYSVOL ACL reset changed file content or tree structure")
	}
	if err := syncTree(sysvolSyncStagePath); err != nil {
		return err
	}

	state.Pending = &manifest
	if err := writeSyncState(sysvolSyncStatePath, state); err != nil {
		return err
	}
	if err := publishDirectory(sysvolSyncStagePath, sysvolSourcePath); err != nil {
		return err
	}
	state.Current = &manifest
	state.Pending = nil
	if err := writeSyncState(sysvolSyncStatePath, state); err != nil {
		return err
	}
	if err := os.RemoveAll(sysvolSyncStagePath); err != nil {
		return fmt.Errorf("remove previous active SYSVOL tree after publication: %w", err)
	}
	r.log.Infof("published verified SYSVOL tree with %d entries; overwritten standby changes: %d", len(manifest.Entries), len(drift))
	return nil
}

func sambaSysvolPath(ctx context.Context) (string, error) {
	command := exec.CommandContext(ctx, "testparm", "-s", "--parameter-name=path", "--section-name=sysvol", sambaConfigPath)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("read Samba SYSVOL path: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return "", fmt.Errorf("Samba did not report a SYSVOL path")
	}
	path := strings.TrimSpace(lines[len(lines)-1])
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("Samba SYSVOL path %q is not absolute", path)
	}
	return filepath.Clean(path), nil
}

func normalizeExportPermissions(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk SYSVOL export %s: %w", path, walkErr)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("SYSVOL export contains unsupported symlink %q", path)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect SYSVOL export entry %s: %w", path, err)
		}
		mode := fs.FileMode(0o644)
		if info.IsDir() {
			mode = 0o755
		} else if !info.Mode().IsRegular() {
			return fmt.Errorf("SYSVOL export contains unsupported file type %q", path)
		}
		if err := os.Chmod(path, mode); err != nil { //nolint:gosec // G122: private staging tree; symlinks are rejected above.
			return fmt.Errorf("set SYSVOL export permissions on %s: %w", path, err)
		}
		return nil
	})
}

func syncTree(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk staged SYSVOL for sync %s: %w", path, walkErr)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("staged SYSVOL contains unsupported symlink %q", path)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect staged SYSVOL entry %s: %w", path, err)
		}
		if info.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("staged SYSVOL contains unsupported file type %q", path)
		}
		file, err := os.Open(path) //nolint:gosec // G122: private staging tree; symlinks are rejected above.
		if err != nil {
			return fmt.Errorf("open staged SYSVOL file %s for sync: %w", path, err)
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return fmt.Errorf("sync staged SYSVOL file %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close staged SYSVOL file %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := syncDirectory(directories[i]); err != nil {
			return fmt.Errorf("sync staged SYSVOL directory: %w", err)
		}
	}
	return nil
}

func validateDomainTree(root, dnsDomain string) error {
	for _, path := range []string{
		filepath.Join(root, dnsDomain, "Policies"),
		filepath.Join(root, dnsDomain, "scripts"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("required SYSVOL directory %s is unavailable: %w", path, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("required SYSVOL path %s is not a directory", path)
		}
	}
	return nil
}

func writeStagedSambaConfig(stagePath, dnsDomain string) (string, error) {
	if err := os.MkdirAll(sysvolRuntimePath, 0o700); err != nil {
		return "", fmt.Errorf("create SYSVOL runtime directory: %w", err)
	}
	contents, err := os.ReadFile(sambaConfigPath)
	if err != nil {
		return "", fmt.Errorf("read Samba configuration: %w", err)
	}
	updated, err := rewriteSambaConfig(contents, sysvolSourcePath, stagePath, dnsDomain)
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp(sysvolRuntimePath, "smb.conf-stage-*")
	if err != nil {
		return "", fmt.Errorf("create staged Samba configuration: %w", err)
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return "", fmt.Errorf("set staged Samba configuration permissions: %w", err)
	}
	if _, err := file.Write(updated); err != nil {
		file.Close()
		return "", fmt.Errorf("write staged Samba configuration: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close staged Samba configuration: %w", err)
	}
	return path, nil
}

func runSambaTool(ctx context.Context, args ...string) error {
	return runCommand(ctx, "samba-tool", args...)
}

func runSambaToolWithConfig(ctx context.Context, configPath string, args ...string) error {
	fullArgs := append(append([]string{}, args...), "-s", configPath)
	return runSambaTool(ctx, fullArgs...)
}

type syncState struct {
	Version int             `json:"version"`
	Current *ExportManifest `json:"current,omitempty"`
	Pending *ExportManifest `json:"pending,omitempty"`
}

func loadSyncState(path string) (syncState, error) {
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return syncState{Version: 1}, nil
	}
	if err != nil {
		return syncState{}, fmt.Errorf("read SYSVOL synchronization state: %w", err)
	}
	var state syncState
	if err := json.Unmarshal(contents, &state); err != nil {
		return syncState{}, fmt.Errorf("parse SYSVOL synchronization state: %w", err)
	}
	if state.Version != 1 {
		return syncState{}, fmt.Errorf("unsupported SYSVOL synchronization state version %d", state.Version)
	}
	if state.Current != nil {
		if err := validateManifest(*state.Current); err != nil {
			return syncState{}, fmt.Errorf("invalid committed SYSVOL synchronization state: %w", err)
		}
	}
	if state.Pending != nil {
		if err := validateManifest(*state.Pending); err != nil {
			return syncState{}, fmt.Errorf("invalid pending SYSVOL synchronization state: %w", err)
		}
	}
	return state, nil
}

func resolvePendingState(state syncState, statePath, activePath string) (syncState, error) {
	if state.Pending == nil {
		return state, nil
	}
	activeEntries, err := buildTreeManifest(activePath, false)
	if err != nil {
		return syncState{}, err
	}
	if reflect.DeepEqual(activeEntries, state.Pending.Entries) {
		state.Current = state.Pending
	}
	state.Pending = nil
	if err := writeSyncState(statePath, state); err != nil {
		return syncState{}, err
	}
	return state, nil
}

func writeSyncState(path string, state syncState) error {
	state.Version = 1
	contents, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode SYSVOL synchronization state: %w", err)
	}
	if err := writeAtomic(path, contents, 0o600); err != nil {
		return fmt.Errorf("write SYSVOL synchronization state: %w", err)
	}
	return nil
}

func runCommand(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	return nil
}

func ensureDirectory(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", path, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect directory %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s must be a real directory", path)
	}
	return nil
}

func optionalVersion(value *int) string {
	if value == nil {
		return "missing"
	}
	return strconv.Itoa(*value)
}
