package dockerclient

import (
	"context"
	"log/slog"
	"time"

	"github.com/containerd/errdefs"
	mobyclient "github.com/moby/moby/client"
)

// CleanupOrphanedContainers force-removes anna-labeled containers that are
// either in a terminal state (exited, dead, created) or have a created-at
// label older than 1 hour. Best-effort: errors are logged, not returned.
func CleanupOrphanedContainers(ctx context.Context, c *Client, annaHome string) {
	filters := mobyclient.Filters{}.Add("label", LabelAnnaHome+"="+annaHome)

	list, err := c.api.ContainerList(ctx, mobyclient.ContainerListOptions{
		All:     true,
		Filters: filters,
	})
	if err != nil {
		slog.Warn("dockerclient: orphan cleanup: list containers", "error", err)
		return
	}

	for _, cs := range list.Items {
		cleanupContainer(ctx, c, cs.ID)
	}
}

func cleanupContainer(ctx context.Context, c *Client, id string) {
	res, err := c.api.ContainerInspect(ctx, id, mobyclient.ContainerInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return
		}
		slog.Warn("dockerclient: orphan cleanup: inspect container", "id", id, "error", err)
		return
	}

	status := ""
	if res.Container.State != nil {
		status = string(res.Container.State.Status)
	}
	createdAt := ""
	if res.Container.Config != nil {
		createdAt = res.Container.Config.Labels[LabelCreatedAt]
	}

	stale := false
	switch status {
	case "exited", "dead", "created":
		stale = true
	}
	if !stale && createdAt != "" {
		if t, err := time.Parse(time.RFC3339, createdAt); err == nil && time.Since(t) > time.Hour {
			stale = true
		}
	}
	if !stale {
		return
	}

	if _, err := c.api.ContainerRemove(ctx, id, mobyclient.ContainerRemoveOptions{Force: true}); err != nil {
		if errdefs.IsNotFound(err) {
			return
		}
		slog.Warn("dockerclient: orphan cleanup: remove container", "id", id, "error", err)
		return
	}
	slog.Info("dockerclient: orphan cleanup: removed container", "id", id, "status", status)
}
