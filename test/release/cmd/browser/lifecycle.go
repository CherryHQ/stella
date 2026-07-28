//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const browserGracefulTimeout = 45 * time.Second

type candidateProcess struct {
	command *exec.Cmd
	done    chan struct{}

	mu      sync.Mutex
	waitErr error
}

func startCandidate(
	binary string,
	root string,
	home string,
	runtimeRoot string,
	vaultKey string,
	logPath string,
) (*candidateProcess, string, error) {
	port, err := freePort()
	if err != nil {
		return nil, "", err
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, "", fmt.Errorf("create candidate log: %w", err)
	}
	command := exec.Command(binary, "serve")
	command.Dir = root
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = append(browserBaseEnv(),
		"STELLA_HOME="+home,
		"STELLA_POSTGRES_RUNTIME="+runtimeRoot,
		"STELLA_VAULT_KEY="+vaultKey,
		"HOST=127.0.0.1",
		fmt.Sprintf("PORT=%d", port),
		"LOG_LEVEL=debug",
		"LOCAL_PASSWORD_ALLOW_REGISTRATION=true",
	)
	// The candidate owns embedded PostgreSQL and may spawn sandbox helpers.
	// A dedicated process group lets teardown address only this runner's tree.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return nil, "", fmt.Errorf("start candidate: %w", err)
	}
	_ = logFile.Close()

	process := &candidateProcess{command: command, done: make(chan struct{})}
	go func() {
		waitErr := command.Wait()
		process.mu.Lock()
		process.waitErr = waitErr
		process.mu.Unlock()
		close(process.done)
	}()
	if err := process.waitReady(baseURL, browserReadyTimeout); err != nil {
		stopErr := process.Stop()
		return process, baseURL, errors.Join(err, stopErr)
	}
	return process, baseURL, nil
}

func (p *candidateProcess) waitReady(baseURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-p.done:
			return fmt.Errorf("candidate exited before /readyz: %w", p.WaitErr())
		default:
		}
		response, err := client.Get(baseURL + "/readyz")
		if err == nil {
			ok := response.StatusCode == http.StatusOK
			_ = response.Body.Close()
			if ok {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("candidate did not become ready within %s", timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (p *candidateProcess) Stop() error {
	select {
	case <-p.done:
	default:
		if err := p.command.Process.Signal(syscall.SIGTERM); err != nil {
			_ = syscall.Kill(-p.command.Process.Pid, syscall.SIGKILL)
		}
		select {
		case <-p.done:
		case <-time.After(browserGracefulTimeout):
			_ = syscall.Kill(-p.command.Process.Pid, syscall.SIGKILL)
			select {
			case <-p.done:
			case <-time.After(10 * time.Second):
				return fmt.Errorf("candidate process group survived SIGKILL")
			}
		}
	}

	waitErr := p.WaitErr()
	groupErr := syscall.Kill(-p.command.Process.Pid, 0)
	if groupErr == nil || errors.Is(groupErr, syscall.EPERM) {
		_ = syscall.Kill(-p.command.Process.Pid, syscall.SIGKILL)
		return errors.Join(waitErr, fmt.Errorf("candidate left processes in its process group"))
	}
	if !errors.Is(groupErr, syscall.ESRCH) {
		return errors.Join(waitErr, fmt.Errorf("probe candidate process group: %w", groupErr))
	}
	return waitErr
}

func (p *candidateProcess) WaitErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func repositoryRoot(explicit string) (string, error) {
	if explicit != "" {
		if !filepath.IsAbs(explicit) {
			return "", fmt.Errorf("--root must be absolute")
		}
		return validateRepositoryRoot(explicit)
	}
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if root, err := validateRepositoryRoot(current); err == nil {
			return root, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("could not find Stella repository root from %s", current)
		}
		current = parent
	}
}

func validateRepositoryRoot(value string) (string, error) {
	root, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("repository root must be a real directory")
	}
	for _, required := range []string{"go.mod", filepath.Join("web", "package.json")} {
		info, err := os.Lstat(filepath.Join(root, required))
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a Stella repository root", root)
		}
	}
	return root, nil
}

func candidateBinary(root string) (string, error) {
	path := strings.TrimSpace(os.Getenv("STELLA_SYSTEM_BINARY"))
	if path == "" {
		path = filepath.Join(root, "dist", "bin", "stellad")
	} else if !filepath.IsAbs(path) {
		return "", fmt.Errorf("STELLA_SYSTEM_BINARY must be absolute")
	}
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect candidate binary %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("candidate binary %s must be a regular non-symlink file", path)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("candidate binary %s is not executable", path)
	}
	return path, nil
}

func browserBaseEnv() []string {
	allow := []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "XDG_RUNTIME_DIR"}
	env := make([]string, 0, len(allow)+16)
	for _, name := range allow {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve candidate port: %w", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}
