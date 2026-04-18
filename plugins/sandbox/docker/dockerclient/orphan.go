package dockerclient

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// CleanupOrphanedContainers lists containers whose LabelAnnaHome equals the
// current annaHome and force-removes any that are stale. A container is
// considered stale if its status is "exited" or "dead", or if its creation
// label age is older than 1 hour. This is best-effort; errors are logged, not
// returned.
func CleanupOrphanedContainers(ctx context.Context, c *Client, annaHome string) {
	filter := fmt.Sprintf("label=%s=%s", LabelAnnaHome, annaHome)

	var stdout, stderr bytes.Buffer
	listCmd := exec.CommandContext(ctx, c.binaryPath,
		"ps", "--all",
		"--filter", filter,
		"--format", "{{.ID}}")
	listCmd.Stdout = &stdout
	listCmd.Stderr = &stderr

	if err := listCmd.Run(); err != nil {
		slog.Warn("dockerclient: orphan cleanup: list containers",
			"error", err, "stderr", stderr.String())
		return
	}

	ids := strings.Fields(stdout.String())
	if len(ids) == 0 {
		return
	}

	for _, id := range ids {
		cleanupContainer(ctx, c, id)
	}
}

func cleanupContainer(ctx context.Context, c *Client, id string) {
	var stdout, stderr bytes.Buffer
	inspectCmd := exec.CommandContext(ctx, c.binaryPath,
		"inspect",
		"--format", fmt.Sprintf(`{{.State.Status}} {{index .Config.Labels "%s"}}`, LabelCreatedAt),
		id)
	inspectCmd.Stdout = &stdout
	inspectCmd.Stderr = &stderr

	if err := inspectCmd.Run(); err != nil {
		msg := stderr.String()
		if strings.Contains(msg, "No such container") {
			return
		}
		slog.Warn("dockerclient: orphan cleanup: inspect container",
			"id", id, "error", err, "stderr", msg)
		return
	}

	parts := strings.SplitN(strings.TrimSpace(stdout.String()), " ", 2)
	status := ""
	createdAt := ""
	if len(parts) >= 1 {
		status = parts[0]
	}
	if len(parts) >= 2 {
		createdAt = parts[1]
	}

	stale := false

	switch status {
	case "exited", "dead":
		stale = true
	}

	if !stale && createdAt != "" {
		t, err := time.Parse(time.RFC3339, createdAt)
		if err == nil && time.Since(t) > time.Hour {
			stale = true
		}
	}

	if !stale {
		return
	}

	var rmStderr bytes.Buffer
	rmCmd := exec.CommandContext(ctx, c.binaryPath, "rm", "--force", id)
	rmCmd.Stderr = &rmStderr

	if err := rmCmd.Run(); err != nil {
		msg := rmStderr.String()
		if !strings.Contains(msg, "No such container") {
			slog.Warn("dockerclient: orphan cleanup: remove container",
				"id", id, "error", err, "stderr", msg)
		}
		return
	}

	slog.Info("dockerclient: orphan cleanup: removed container", "id", id, "status", status)
}
