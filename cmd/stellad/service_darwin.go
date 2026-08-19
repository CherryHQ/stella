//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.cherryai.stella</string>

    <key>ProgramArguments</key>
    <array>
        <string>STELLAD_BIN</string>
        <string>server</string>
    </array>

    <key>EnvironmentVariables</key>
    <dict>
        <key>STELLA_HOME</key>
        <string>HOME_DIR/.stella</string>
        <key>STELLA_DOCKER_SANDBOX_MODE</key>
        <string>host</string>
    </dict>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>

    <key>StandardOutPath</key>
    <string>HOME_DIR/Library/Logs/stella/stella.log</string>
    <key>StandardErrorPath</key>
    <string>HOME_DIR/Library/Logs/stella/stella.log</string>

    <key>ThrottleInterval</key>
    <integer>10</integer>
</dict>
</plist>
`

const (
	serviceLabel   = "com.cherryai.stella"
	plistName      = "com.cherryai.stella.plist"
	stellaLogDir   = "Library/Logs/stella"
	stellaLogFile  = "Library/Logs/stella/stella.log"
	launchAgentDir = "Library/LaunchAgents"
)

type launchdManager struct{}

func newServiceManager() serviceManager {
	return &launchdManager{}
}

func (m *launchdManager) Install() error {
	if _, err := exec.LookPath("launchctl"); err != nil {
		return fmt.Errorf("launchctl not found: stella service requires macOS launchd")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}

	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	if info, statErr := os.Stat(bin); statErr == nil && info.Mode()&0o002 != 0 {
		fmt.Fprintf(os.Stderr, "WARNING: %s is world-writable — consider installing to a protected path\n", bin)
	}

	plist := strings.ReplaceAll(plistTemplate, "HOME_DIR", home)
	plist = strings.ReplaceAll(plist, "STELLAD_BIN", bin)

	agentDir := filepath.Join(home, launchAgentDir)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	logDir := filepath.Join(home, stellaLogDir)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	plistPath := filepath.Join(agentDir, plistName)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	if err := launchctl("load", plistPath); err != nil {
		return fmt.Errorf("load service: %w", err)
	}

	fmt.Printf("stella service installed and started.\nLogs: %s\n", filepath.Join(home, stellaLogFile))
	return nil
}

func (m *launchdManager) Uninstall() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}

	plistPath := filepath.Join(home, launchAgentDir, plistName)
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return fmt.Errorf("service is not installed")
	}

	if err := launchctl("unload", plistPath); err != nil {
		return fmt.Errorf("unload service: %w", err)
	}

	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("remove plist: %w", err)
	}

	fmt.Println("stella service uninstalled.")
	return nil
}

func (m *launchdManager) Start() error {
	if err := launchctl("start", serviceLabel); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	fmt.Println("stella service started.")
	return nil
}

func (m *launchdManager) Stop() error {
	if err := launchctl("stop", serviceLabel); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	fmt.Println("stella service stopped.")
	return nil
}

func (m *launchdManager) Restart() error {
	if err := m.Stop(); err != nil {
		return err
	}
	return m.Start()
}

func (m *launchdManager) Status() error {
	out, err := exec.Command("launchctl", "list").Output()
	if err != nil {
		return fmt.Errorf("launchctl list: %w", err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.Contains(line, serviceLabel) {
			fmt.Println(line)
			return nil
		}
	}
	fmt.Printf("%s is not running\n", serviceLabel)
	return nil
}

func (m *launchdManager) Logs(follow bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	logPath := filepath.Join(home, stellaLogFile)
	args := []string{logPath}
	if follow {
		args = append([]string{"-f"}, args...)
	}
	cmd := exec.Command("tail", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func launchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
