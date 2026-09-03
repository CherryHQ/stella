//go:build !unix

package testbed

import (
	"errors"
	"io/fs"
	"net/http"
	"os/exec"
	"time"
)

var errUnsupportedPlatform = errors.New("the Stella testbed is supported only on Linux and macOS")

func currentIdentity() (processIdentity, error)      { return processIdentity{}, errUnsupportedPlatform }
func identityFor(int) (processIdentity, error)       { return processIdentity{}, fs.ErrNotExist }
func sameIdentity(a, b processIdentity) bool         { return a == b }
func signalProcess(int) error                        { return errUnsupportedPlatform }
func terminateProcess(p *os.Process) error           { return p.Kill() }
func killProcessGroup(cmd *exec.Cmd)                 { _ = cmd.Process.Kill() }
func processGroupAlive(*exec.Cmd) bool               { return false }
func setProcessGroup(*exec.Cmd)                      {}
func stopServer(cmd *exec.Cmd, done <-chan struct{}) { _ = cmd.Process.Kill(); <-done }

func ready(baseURL string) error {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(baseURL + "/readyz")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.New("server is not ready")
	}
	return nil
}
