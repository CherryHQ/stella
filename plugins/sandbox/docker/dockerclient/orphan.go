package dockerclient

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/containerd/errdefs"
	mobyclient "github.com/moby/moby/client"
)

// CleanupOrphanedContainers force-removes stella-labeled containers whose owning
// process is clearly gone:
//   - dead-state containers (exited, dead, created) are always removed
//   - running / paused containers are removed only when their owner_pid label
//     points to a process that no longer exists on this host — this keeps
//     peer stella processes with live sessions safe from another stella startup
//   - transitional states (restarting, …) fall back to an age cutoff so
//     truly-hung containers eventually clear
//
// Best-effort: errors are logged, not returned.
func CleanupOrphanedContainers(ctx context.Context, c *Client, stellaHome string) {
	filters := mobyclient.Filters{}.Add("label", LabelStellaHome+"="+stellaHome)

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

// SessionIDsWithContainers returns session IDs still represented by any scoped
// container, including stopped containers whose removal failed. Callers use it
// after CleanupOrphanedContainers before deleting session-owned host resources.
func (c *Client) SessionIDsWithContainers(ctx context.Context, stellaHome string) (map[string]struct{}, error) {
	filters := mobyclient.Filters{}.Add("label", LabelStellaHome+"="+stellaHome)
	list, err := c.api.ContainerList(ctx, mobyclient.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("dockerclient: list session containers: %w", err)
	}
	ids := make(map[string]struct{}, len(list.Items))
	for _, container := range list.Items {
		if id := container.Labels[LabelSessionID]; id != "" {
			ids[id] = struct{}{}
		}
	}
	return ids, nil
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
	labels := map[string]string{}
	if res.Container.Config != nil && res.Container.Config.Labels != nil {
		labels = res.Container.Config.Labels
	}

	if !isContainerStale(status, labels[LabelOwnerPID], labels[LabelCreatedAt]) {
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

// isContainerStale reports whether a labelled container should be force-removed
// during startup cleanup. Age alone never triggers removal of a running or
// paused container — that would race with peer stella processes — so live states
// are gated on whether the recorded owner PID is still a live process.
func isContainerStale(status, ownerPID, createdAt string) bool {
	switch status {
	case "exited", "dead", "created":
		return true
	case "running", "paused":
		return ownerProcessGone(ownerPID)
	}
	// restarting, removing, or unknown: fall back to age so truly-hung
	// transitional containers eventually clear. Peer stella processes that
	// are actively running sit in "running" above, not here.
	if createdAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return false
	}
	return time.Since(t) > time.Hour
}

// ownerProcessGone reports whether the PID label refers to a process that no
// longer exists on this host. Missing or unparseable labels return false —
// without positive evidence of death we leave the container alone.
func ownerProcessGone(pidStr string) bool {
	if pidStr == "" {
		return false
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	if runtime.GOOS == "windows" {
		// FindProcess on Windows fails for dead PIDs, so success == alive.
		return false
	}
	// Unix: FindProcess is a no-op. Signal(0) probes liveness without effect.
	return proc.Signal(syscall.Signal(0)) != nil
}
