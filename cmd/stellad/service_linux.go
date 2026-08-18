//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const systemUnitTemplate = `[Unit]
Description=stella — self-hosted AI assistant daemon
Documentation=https://stella.cherryin.com/docs
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=stella
Group=stella
WorkingDirectory=/var/lib/stella
Environment=STELLA_HOME=/var/lib/stella
Environment=STELLA_DOCKER_SANDBOX_MODE=host
Environment=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

ExecStart=STELLAD_EXEC
Restart=on-failure
RestartSec=5
TimeoutStopSec=30

NoNewPrivileges=true
PrivateTmp=true
StateDirectory=stella
StateDirectoryMode=0750
CacheDirectory=stella
CacheDirectoryMode=0750
LogsDirectory=stella
LogsDirectoryMode=0750

[Install]
WantedBy=multi-user.target
`

const (
	unitName    = "stella.service"
	systemUser  = "stella"
	systemGroup = "stella"
	systemHome  = "/var/lib/stella"
	systemPATH  = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

type systemdManager struct{}

func newServiceManager() serviceManager {
	return &systemdManager{}
}

func (m *systemdManager) Install() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl not found: stella service requires systemd")
	}

	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	if err := ensureSystemServiceAccount(); err != nil {
		return err
	}
	if err := checkSystemServiceRuntime(bin); err != nil {
		return err
	}
	if err := checkSystemServiceConfig(); err != nil {
		return err
	}

	unitPath := filepath.Join("/etc/systemd/system", unitName)
	unitContent := strings.ReplaceAll(systemUnitTemplate, "STELLAD_EXEC", unitExecStart(bin))
	if err := os.WriteFile(unitPath, []byte(unitContent), 0o644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", "--now", "stella"); err != nil {
		return err
	}

	fmt.Println("stella service installed and started.")
	return nil
}

func (m *systemdManager) Uninstall() error {
	_ = systemctl("disable", "--now", "stella")

	unitPath := filepath.Join("/etc/systemd/system", unitName)
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file: %w", err)
	}

	_ = systemctl("daemon-reload")

	fmt.Printf("stella service uninstalled.\nNote: the %s user and %s were preserved. Remove them manually only if you want to delete service data.\n", systemUser, systemHome)
	return nil
}

func (m *systemdManager) Start() error {
	return systemctl("start", "stella")
}

func (m *systemdManager) Stop() error {
	return systemctl("stop", "stella")
}

func (m *systemdManager) Restart() error {
	return systemctl("restart", "stella")
}

func (m *systemdManager) Status() error {
	return systemctl("status", "stella")
}

func (m *systemdManager) Logs(follow bool) error {
	args := []string{"-u", "stella"}
	if follow {
		args = append(args, "-f")
	}
	cmd := exec.Command("journalctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func unitExecStart(bin string) string {
	return systemdQuoteArg(bin) + " server"
}

func systemdQuoteArg(arg string) string {
	if strings.ContainsAny(arg, "\n\r") {
		panic("systemdQuoteArg: path contains newline or carriage return")
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(arg)
	return `"` + escaped + `"`
}

func ensureSystemServiceAccount() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("system service install requires root; rerun with sudo")
	}
	if err := ensureGroupExists(); err != nil {
		return err
	}
	if err := ensureUserExists(); err != nil {
		return err
	}
	for _, dir := range []string{systemHome, "/var/cache/stella", "/var/log/stella"} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		if err := runCommand("chown", systemUser+":"+systemGroup, dir); err != nil {
			return fmt.Errorf("chown %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o750); err != nil {
			return fmt.Errorf("chmod %s: %w", dir, err)
		}
	}
	return nil
}

func ensureGroupExists() error {
	if groupExists(systemGroup) {
		return nil
	}
	if _, err := exec.LookPath("groupadd"); err != nil {
		return fmt.Errorf("create %s group: groupadd not found", systemGroup)
	}
	if err := runCommand("groupadd", "--system", systemGroup); err != nil {
		return fmt.Errorf("create %s group: %w", systemGroup, err)
	}
	return nil
}

func groupExists(group string) bool {
	if exec.Command("getent", "group", group).Run() == nil {
		return true
	}
	data, err := os.ReadFile("/etc/group")
	if err != nil {
		return false
	}
	prefix := group + ":"
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func ensureUserExists() error {
	if exec.Command("id", "-u", systemUser).Run() == nil {
		return nil
	}
	if _, err := exec.LookPath("useradd"); err != nil {
		return fmt.Errorf("create %s user: useradd not found", systemUser)
	}
	args := []string{
		"--system",
		"--gid", systemGroup,
		"--home-dir", systemHome,
		"--create-home",
		"--shell", nologinShell(),
		systemUser,
	}
	if err := runCommand("useradd", args...); err != nil {
		return fmt.Errorf("create %s user: %w", systemUser, err)
	}
	return nil
}

func nologinShell() string {
	for _, shell := range []string{"/usr/sbin/nologin", "/sbin/nologin", "/bin/false"} {
		if _, err := os.Stat(shell); err == nil {
			return shell
		}
	}
	return "/bin/false"
}

func checkSystemServiceConfig() error {
	envPath := filepath.Join(systemHome, ".env")
	data, err := os.ReadFile(envPath)
	if os.IsNotExist(err) {
		return fmt.Errorf(
			"%s is required before starting the system service\n"+
				"  copy or create a .env containing STELLA_VAULT_KEY, then run:\n"+
				"  chown %s:%s %s && chmod 600 %s",
			envPath,
			systemUser,
			systemGroup,
			envPath,
			envPath,
		)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", envPath, err)
	}
	if !dotenvHasKey(data, "STELLA_VAULT_KEY") {
		return fmt.Errorf("%s must contain STELLA_VAULT_KEY before starting the system service", envPath)
	}
	if err := runAsSystemUser("test", "-r", envPath); err != nil {
		return fmt.Errorf("%s cannot read %s: %w", systemUser, envPath, err)
	}
	return nil
}

func dotenvHasKey(data []byte, key string) bool {
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name = strings.TrimPrefix(strings.TrimSpace(name), "export ")
		v := strings.TrimSpace(value)
		v = strings.Trim(v, `"'`)
		if name == key && v != "" {
			return true
		}
	}
	return false
}

func checkSystemServiceRuntime(bin string) error {
	if err := checkBinaryPathSecurity(bin); err != nil {
		return err
	}
	if err := runAsSystemUser("test", "-x", bin); err != nil {
		return fmt.Errorf("%s cannot execute %s; install stellad somewhere world-executable, such as /usr/local/bin/stellad: %w", systemUser, bin, err)
	}
	return checkBwrapAsSystemUser()
}

// checkBinaryPathSecurity rejects binaries outside root-owned, non-user-writable
// paths. A system service should not execute binaries that non-root users can replace.
func checkBinaryPathSecurity(bin string) error {
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return fmt.Errorf("resolve binary path %s: %w", bin, err)
	}

	seen := map[string]bool{}
	for _, path := range append(pathChain(bin), pathChain(resolved)...) {
		if seen[path] {
			continue
		}
		seen[path] = true
		if err := checkRootOwnedNotWritable(path, bin); err != nil {
			return err
		}
	}
	return nil
}

func pathChain(path string) []string {
	clean := filepath.Clean(path)
	paths := []string{clean}
	for {
		parent := filepath.Dir(clean)
		if parent == clean {
			break
		}
		paths = append(paths, parent)
		clean = parent
	}
	return paths
}

func checkRootOwnedNotWritable(path, bin string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("stat %s: unsupported file info", path)
	}
	if stat.Uid != 0 {
		return fmt.Errorf(
			"%s is owned by uid %d — a system service must execute a root-owned binary path\n"+
				"  install stellad to a root-owned directory such as /usr/local/bin:\n"+
				"  sudo cp %s /usr/local/bin/stellad && sudo chmod 755 /usr/local/bin/stellad",
			path, stat.Uid, bin,
		)
	}
	mode := info.Mode()
	if mode&0o002 != 0 {
		return fmt.Errorf(
			"%s is world-writable (mode %s) — a non-root user could replace the binary\n"+
				"  install stellad to a root-owned directory such as /usr/local/bin:\n"+
				"  sudo cp %s /usr/local/bin/stellad && sudo chmod 755 /usr/local/bin/stellad",
			path, mode.String(), bin,
		)
	}
	if mode&0o020 != 0 {
		return fmt.Errorf(
			"%s is group-writable (mode %s) — members of the owning group could replace the binary\n"+
				"  install stellad to a root-owned directory such as /usr/local/bin:\n"+
				"  sudo cp %s /usr/local/bin/stellad && sudo chmod 755 /usr/local/bin/stellad",
			path, mode.String(), bin,
		)
	}
	return nil
}

func checkBwrapAsSystemUser() error {
	bwrap, err := lookPathIn(systemPATH, "bwrap")
	if err != nil {
		return fmt.Errorf(
			"bwrap (bubblewrap) is required but not found in the system PATH\n" +
				"  install: apt install bubblewrap  |  dnf install bubblewrap  |  pacman -S bubblewrap",
		)
	}
	if err := runAsSystemUser(bwrap, "--dev-bind", "/", "/", "--", "true"); err != nil {
		return fmt.Errorf(
			"bwrap is installed but cannot create a user namespace for %s (got: %w)\n"+
				"  ensure unprivileged user namespaces are enabled before starting the service",
			systemUser,
			err,
		)
	}
	return nil
}

func lookPathIn(pathEnv, file string) (string, error) {
	for _, dir := range filepath.SplitList(pathEnv) {
		path := filepath.Join(dir, file)
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func runAsSystemUser(args ...string) error {
	if _, err := exec.LookPath("runuser"); err == nil {
		return runCommand("runuser", append([]string{"-u", systemUser, "--"}, args...)...)
	}
	if _, err := exec.LookPath("su"); err == nil {
		return runCommand("su", "-s", "/bin/sh", systemUser, "-c", strings.Join(shellQuoteArgs(args), " "))
	}
	return fmt.Errorf("runuser or su not found")
}

func shellQuoteArgs(args []string) []string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", `'\''`)+"'")
	}
	return quoted
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
