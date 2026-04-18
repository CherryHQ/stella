package dockerclient

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// NetworkMode controls the container's network access.
type NetworkMode string

const (
	NetworkDisabled NetworkMode = "disabled"
	NetworkAllowAll NetworkMode = "allow_all"
)

// Mount represents a bind-mount from host to container.
type Mount struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

// CreateOptions configures a new sandbox container.
type CreateOptions struct {
	Image          string
	User           string      // "uid:gid"; empty omits --user
	WorkspaceHost  string      // absolute host path
	WorkspaceMount string      // absolute in-container path (e.g. "/workspace")
	ReadOnlyMounts []Mount     // host -> container, read-only
	ExtraMounts    []Mount     // parsed from config.ExtraMounts
	NetworkMode    NetworkMode // disabled | allow_all
	Env            map[string]string
	Labels         map[string]string // must include LabelSessionID + LabelAnnaHome + LabelCreatedAt
	Name           string            // optional; caller builds "anna-sandbox-<session-id>"
}

// CreateAndStart runs `docker create` + `docker start` with the entrypoint
// `/bin/sh -c 'tail -f /dev/null'` so the container stays up until Stop.
// Returns the container ID.
func (c *Client) CreateAndStart(ctx context.Context, opts CreateOptions) (string, error) {
	args := c.buildCreateArgs(opts)

	var stdout, stderr bytes.Buffer
	createCmd := exec.CommandContext(ctx, c.binaryPath, args...)
	createCmd.Stdout = &stdout
	createCmd.Stderr = &stderr

	if err := createCmd.Run(); err != nil {
		return "", fmt.Errorf("docker create: %w: %s", err, stderr.String())
	}

	containerID := strings.TrimSpace(stdout.String())

	var startStderr bytes.Buffer
	startCmd := exec.CommandContext(ctx, c.binaryPath, "start", containerID)
	startCmd.Stderr = &startStderr

	if err := startCmd.Run(); err != nil {
		return "", fmt.Errorf("docker start %s: %w: %s", containerID, err, startStderr.String())
	}

	return containerID, nil
}

// buildCreateArgs constructs the argv for `docker create`.
func (c *Client) buildCreateArgs(opts CreateOptions) []string {
	args := []string{"create"}

	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}

	// Labels
	for k, v := range opts.Labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}

	// Workspace bind mount
	if opts.WorkspaceHost != "" && opts.WorkspaceMount != "" {
		args = append(args, "--mount",
			fmt.Sprintf("type=bind,src=%s,dst=%s", opts.WorkspaceHost, opts.WorkspaceMount))
	}

	// Read-only mounts
	for _, m := range opts.ReadOnlyMounts {
		spec := fmt.Sprintf("type=bind,src=%s,dst=%s,readonly", m.HostPath, m.ContainerPath)
		args = append(args, "--mount", spec)
	}

	// Extra mounts
	for _, m := range opts.ExtraMounts {
		spec := fmt.Sprintf("type=bind,src=%s,dst=%s", m.HostPath, m.ContainerPath)
		if m.ReadOnly {
			spec += ",readonly"
		}
		args = append(args, "--mount", spec)
	}

	// Network
	switch opts.NetworkMode {
	case NetworkDisabled:
		args = append(args, "--network", "none")
	case NetworkAllowAll:
		// omit --network; use default bridge
	}

	// User
	if opts.User != "" {
		args = append(args, "--user", opts.User)
	}

	// Workdir
	if opts.WorkspaceMount != "" {
		args = append(args, "--workdir", opts.WorkspaceMount)
	}

	// Env
	for k, v := range opts.Env {
		args = append(args, "--env", fmt.Sprintf("%s=%s", k, v))
	}

	// Entrypoint + image + command
	args = append(args, "--entrypoint", "/bin/sh")
	args = append(args, opts.Image)
	args = append(args, "-c", "tail -f /dev/null")

	return args
}

// Stop sends SIGTERM via `docker stop --time 2 <id>` then removes.
// Swallows "No such container" as a non-error so Close is idempotent.
func (c *Client) Stop(ctx context.Context, containerID string) error {
	var stopStderr bytes.Buffer
	stopCmd := exec.CommandContext(ctx, c.binaryPath, "stop", "--time", "2", containerID)
	stopCmd.Stderr = &stopStderr

	if err := stopCmd.Run(); err != nil {
		msg := stopStderr.String()
		if !strings.Contains(msg, "No such container") {
			return fmt.Errorf("docker stop %s: %w: %s", containerID, err, msg)
		}
		// container already gone; skip rm
		return nil
	}

	var rmStderr bytes.Buffer
	rmCmd := exec.CommandContext(ctx, c.binaryPath, "rm", containerID)
	rmCmd.Stderr = &rmStderr

	if err := rmCmd.Run(); err != nil {
		msg := rmStderr.String()
		if !strings.Contains(msg, "No such container") {
			return fmt.Errorf("docker rm %s: %w: %s", containerID, err, msg)
		}
	}

	return nil
}

// ContainerAlive returns whether the container is running, via
// `docker inspect --format {{.State.Running}} <id>`.
func (c *Client) ContainerAlive(ctx context.Context, containerID string) (bool, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.binaryPath,
		"inspect", "--format", "{{.State.Running}}", containerID)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if strings.Contains(msg, "No such container") {
			return false, nil
		}
		return false, fmt.Errorf("docker inspect %s: %w: %s", containerID, err, msg)
	}

	return strings.TrimSpace(stdout.String()) == "true", nil
}
