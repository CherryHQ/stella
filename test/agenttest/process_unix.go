//go:build unix

package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func currentIdentity() (processIdentity, error) { return identityFor(os.Getpid()) }
func sameIdentity(a, b processIdentity) bool    { return a.PID == b.PID && a.Started == b.Started }
func signalProcess(pid int) error               { return syscall.Kill(pid, syscall.SIGTERM) }
func setProcessGroup(cmd *exec.Cmd)             { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func stopServer(cmd *exec.Cmd, done <-chan struct{}) {
	select {
	case <-done:
		return
	default:
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-done:
		return
	case <-time.After(shutdownWait):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

func ready(baseURL string) error {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(baseURL + "/readyz")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readyz: %s", resp.Status)
	}
	return nil
}
