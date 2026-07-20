package dockerclient

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/containerd/errdefs"
	mobyclient "github.com/moby/moby/client"
)

// CleanupOrphanedContainers force-removes Stella-labelled containers whose owner
// is conclusively gone. Dead-state containers are always removed; running and
// paused containers are removed only when their recorded owner is gone. Unknown
// owner state and Docker inspection errors fail closed. Transitional states keep
// the existing age cutoff so genuinely hung containers eventually clear.
//
// current identifies this factory's cached owner. A sandbox marked with the
// current container ID predates this process: cleanup runs before it creates
// sessions, so it is removed even if Docker has reused that container ID after a
// restart.
//
// Best-effort: errors are logged, not returned.
func CleanupOrphanedContainers(ctx context.Context, c *Client, stellaHome string, current Owner) {
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
		cleanupContainer(ctx, c, cs.ID, current)
	}
}

func cleanupContainer(ctx context.Context, c *Client, id string, current Owner) {
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

	if !isContainerStale(status, labels, current, func() bool {
		return sandboxOwnerGone(ctx, c, labels, current)
	}) {
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
// during startup cleanup. ownerGone is evaluated for live and created states so
// a concurrent peer cannot lose a newly-created session before it starts. Dead
// and transitional state handling remains independent of owner lookup failures.
func isContainerStale(status string, labels map[string]string, current Owner, ownerGone func() bool) bool {
	switch status {
	case "exited", "dead":
		return true
	case "created", "running", "paused":
		return ownerGone()
	}
	// restarting, removing, or unknown: fall back to age so truly-hung
	// transitional containers eventually clear. Peer Stella processes that are
	// actively running sit in "running" above, not here.
	createdAt := labels[LabelCreatedAt]
	if createdAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return false
	}
	return time.Since(t) > time.Hour
}

// sandboxOwnerGone determines liveness from the labels' declared ownership
// scheme. Container owners are checked through the Docker daemon, never through
// the stellad container's PID namespace. Legacy owner_pid is safe to probe only
// in native host mode; in DooD it is deliberately preserved.
func sandboxOwnerGone(ctx context.Context, c *Client, labels map[string]string, current Owner) bool {
	switch labels[LabelOwnerKind] {
	case OwnerKindProcess:
		ownerID := labels[LabelOwnerID]
		if current.Kind != OwnerKindProcess || ownerID == "" {
			return false
		}
		// Cleanup precedes this process's first session, so a matching PID is a
		// prior-process leftover whose PID the OS has reused.
		return ownerID == current.ID || ownerProcessGone(ownerID)
	case OwnerKindContainer:
		ownerID := labels[LabelOwnerID]
		if ownerID == "" {
			return false
		}
		if current.Kind == OwnerKindContainer && ownerID == current.ID {
			return true
		}
		state, err := c.InspectContainerState(ctx, ownerID)
		return err == nil && (state == nil || !state.Running)
	default:
		if current.Kind != OwnerKindProcess {
			return false
		}
		legacyPID := labels[LabelOwnerPID]
		return legacyPID != "" && (legacyPID == current.ID || ownerProcessGone(legacyPID))
	}
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
