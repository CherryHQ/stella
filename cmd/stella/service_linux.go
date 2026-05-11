//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const userUnitTemplate = `[Unit]
Description=stella — self-hosted AI assistant daemon
Documentation=https://stella.vaayne.com/docs
After=network-online.target
Wants=network-online.target

[Service]
Type=simple

ExecStart=STELLA_BIN
Restart=on-failure
RestartSec=5
TimeoutStopSec=30

Environment=STELLA_HOME=%h/.stella

[Install]
WantedBy=default.target
`

// System-mode template: no User=/Group=/WorkingDirectory= — runs as root.
const systemUnitTemplate = `[Unit]
Description=stella — self-hosted AI assistant daemon
Documentation=https://stella.vaayne.com/docs
After=network-online.target
Wants=network-online.target

[Service]
Type=simple

ExecStart=STELLA_BIN
Restart=on-failure
RestartSec=5
TimeoutStopSec=30

NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
`

const (
	unitName        = "stella.service"
	serviceModeFile = ".service-mode"
)

type systemdManager struct{}

func newServiceManager() serviceManager {
	return &systemdManager{}
}

func (m *systemdManager) Install(system bool) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl not found: stella service requires systemd")
	}

	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	var unitPath string
	var unitContent string

	if system {
		unitPath = filepath.Join("/etc/systemd/system", unitName)
		unitContent = strings.ReplaceAll(systemUnitTemplate, "STELLA_BIN", bin)
	} else {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("resolve config dir: %w", err)
		}
		unitDir := filepath.Join(configDir, "systemd", "user")
		if err := os.MkdirAll(unitDir, 0o755); err != nil {
			return fmt.Errorf("create systemd user dir: %w", err)
		}
		unitPath = filepath.Join(unitDir, unitName)
		unitContent = strings.ReplaceAll(userUnitTemplate, "STELLA_BIN", bin)
	}

	if err := os.WriteFile(unitPath, []byte(unitContent), 0o644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	if err := persistMode(system); err != nil {
		return err
	}

	if system {
		if err := systemctl(false, "daemon-reload"); err != nil {
			return err
		}
		if err := systemctl(false, "enable", "--now", "stella"); err != nil {
			return err
		}
	} else {
		if err := systemctl(true, "daemon-reload"); err != nil {
			return err
		}
		if err := systemctl(true, "enable", "--now", "stella"); err != nil {
			return err
		}
		// Allow service to survive user logout.
		_ = exec.Command("loginctl", "enable-linger", os.Getenv("USER")).Run()
	}

	fmt.Println("stella service installed and started.")
	return nil
}

func (m *systemdManager) Uninstall(system bool) error {
	_ = systemctl(system, "disable", "--now", "stella")

	var unitPath string
	if system {
		unitPath = filepath.Join("/etc/systemd/system", unitName)
	} else {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("resolve config dir: %w", err)
		}
		unitPath = filepath.Join(configDir, "systemd", "user", unitName)
	}

	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file: %w", err)
	}

	_ = systemctl(system, "daemon-reload")

	if err := removeMode(); err != nil {
		return err
	}

	fmt.Println("stella service uninstalled.")
	return nil
}

func (m *systemdManager) Start() error {
	system, err := readMode()
	if err != nil {
		return err
	}
	return systemctl(system, "start", "stella")
}

func (m *systemdManager) Stop() error {
	system, err := readMode()
	if err != nil {
		return err
	}
	return systemctl(system, "stop", "stella")
}

func (m *systemdManager) Restart() error {
	system, err := readMode()
	if err != nil {
		return err
	}
	return systemctl(system, "restart", "stella")
}

func (m *systemdManager) Status() error {
	system, err := readMode()
	if err != nil {
		return err
	}
	return systemctl(system, "status", "stella")
}

func (m *systemdManager) Logs(follow bool) error {
	system, err := readMode()
	if err != nil {
		return err
	}
	args := []string{"-u", "stella"}
	if !system {
		args = append([]string{"--user"}, args...)
	}
	if follow {
		args = append(args, "-f")
	}
	cmd := exec.Command("journalctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func systemctl(user bool, args ...string) error {
	var fullArgs []string
	if user {
		fullArgs = append([]string{"--user"}, args...)
	} else {
		fullArgs = args
	}
	cmd := exec.Command("systemctl", fullArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func modeFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".stella", serviceModeFile), nil
}

func persistMode(system bool) error {
	path, err := modeFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create stella dir: %w", err)
	}
	mode := "user"
	if system {
		mode = "system"
	}
	return os.WriteFile(path, []byte(mode), 0o644)
}

func readMode() (system bool, err error) {
	path, err := modeFilePath()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "warning: service mode file not found, defaulting to user mode")
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read service mode: %w", err)
	}
	return strings.TrimSpace(string(data)) == "system", nil
}

func removeMode() error {
	path, err := modeFilePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove service mode file: %w", err)
	}
	return nil
}
