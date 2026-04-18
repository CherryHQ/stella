package dockerclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
)

// Client wraps the docker binary path.
type Client struct {
	binaryPath string // resolved once at construction
}

// VersionInfo holds the minimal version data we care about.
type VersionInfo struct {
	Server struct {
		APIVersion string `json:"ApiVersion"`
	} `json:"Server"`
	Client struct {
		Version string `json:"Version"`
	} `json:"Client"`
}

// New resolves the docker binary on PATH. Checks DOCKER_BIN env first for
// testability. Returns an error if not found.
func New() (*Client, error) {
	if v := os.Getenv("DOCKER_BIN"); v != "" {
		return &Client{binaryPath: v}, nil
	}
	path, err := exec.LookPath("docker")
	if err != nil {
		return nil, fmt.Errorf("dockerclient: docker binary not found on PATH: %w", err)
	}
	return &Client{binaryPath: path}, nil
}

// NewWithPath constructs a Client with an explicit binary path.
// Used by tests with shims.
func NewWithPath(path string) *Client {
	return &Client{binaryPath: path}
}

// Version runs `docker version --format {{json .}}` and returns the parsed VersionInfo.
// Used by preflight to confirm daemon reachability.
func (c *Client) Version(ctx context.Context) (*VersionInfo, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.binaryPath, "version", "--format", "{{json .}}")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker version: %w: %s", err, stderr.String())
	}

	var info VersionInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return nil, fmt.Errorf("docker version: parse: %w", err)
	}
	return &info, nil
}

// ImageExists reports whether the image exists locally.
// Implemented as `docker image inspect <image>` — exit 0 = true, exit 1 = false,
// any other error surfaces.
func (c *Client) ImageExists(ctx context.Context, image string) (bool, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.binaryPath, "image", "inspect", image)
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("docker image inspect %s: %w: %s", image, err, stderr.String())
}

// PullImage runs `docker pull <image>`, piping stderr through slog.Info line-by-line.
func (c *Client) PullImage(ctx context.Context, image string) error {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, c.binaryPath, "pull", image)
	cmd.Stdout = &stdout

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("docker pull %s: stderr pipe: %w", image, err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("docker pull %s: start: %w", image, err)
	}

	scanner := bufio.NewScanner(stderrPipe)
	for scanner.Scan() {
		slog.Info("docker pull", "image", image, "output", scanner.Text())
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("docker pull %s: %w", image, err)
	}
	return nil
}
