package dockerclient

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/containerd/errdefs"
	mobyclient "github.com/moby/moby/client"
)

// CleanupOrphanedContainers force-removes only stella-labeled containers that
// Docker itself reports as non-running terminal states. Host PIDs and age are
// diagnostics, not ownership proofs: PID reuse, a different PID namespace, and
// daemon restarts make both unsafe for reclaiming a live container.
//
// The returned guard is true only when every scoped container was in a terminal
// state and was either removed or was already absent. A running, transitional,
// or uninspectable container keeps the scope pending so callers must retain all
// host resources whose ownership cannot be reconstructed.
func CleanupOrphanedContainers(ctx context.Context, c *Client, stellaHome string) (bool, error) {
	guard, err := CaptureOrphanedContainers(ctx, c, stellaHome)
	if err != nil {
		return false, err
	}
	return guard(ctx)
}

// CaptureOrphanedContainers snapshots the scoped container IDs at startup and
// returns a repeatable recovery check for that exact set. Containers created
// after the snapshot are owned by the current runtime and are deliberately not
// considered by the guard.
func CaptureOrphanedContainers(ctx context.Context, c *Client, stellaHome string) (func(context.Context) (bool, error), error) {
	filters := mobyclient.Filters{}.Add("label", LabelStellaHome+"="+stellaHome)

	list, err := c.api.ContainerList(ctx, mobyclient.ContainerListOptions{
		All:     true,
		Filters: filters,
	})
	if err != nil {
		slog.Warn("dockerclient: orphan cleanup: list containers", "error", err)
		return nil, err
	}

	ids := make([]string, 0, len(list.Items))
	seen := make(map[string]struct{}, len(list.Items))
	for _, cs := range list.Items {
		if cs.ID == "" {
			continue
		}
		// Generation-owned containers are reconciled through AgentSandboxGeneration
		// and its backend controller. Legacy startup GC must not delete one merely
		// because the creating executor is absent.
		if isGenerationManaged(cs.Labels) {
			continue
		}
		if _, ok := seen[cs.ID]; ok {
			continue
		}
		seen[cs.ID] = struct{}{}
		ids = append(ids, cs.ID)
	}
	return func(checkCtx context.Context) (bool, error) {
		clean := true
		for _, id := range ids {
			removed, err := cleanupContainer(checkCtx, c, id)
			if err != nil {
				return false, err
			}
			if !removed {
				clean = false
			}
		}
		return clean, nil
	}, nil
}

func isGenerationManaged(labels map[string]string) bool {
	return labels[LabelGeneration] != "" || labels[LabelOwnerBootID] != ""
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

func cleanupContainer(ctx context.Context, c *Client, id string) (bool, error) {
	res, err := c.api.ContainerInspect(ctx, id, mobyclient.ContainerInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return true, nil
		}
		slog.Warn("dockerclient: orphan cleanup: inspect container", "id", id, "error", err)
		return false, err
	}

	status := ""
	if res.Container.State != nil {
		status = string(res.Container.State.Status)
	}
	if !isContainerTerminal(status) {
		return false, nil
	}

	if _, err := c.api.ContainerRemove(ctx, id, mobyclient.ContainerRemoveOptions{}); err != nil {
		if errdefs.IsNotFound(err) {
			return true, nil
		}
		slog.Warn("dockerclient: orphan cleanup: remove container", "id", id, "error", err)
		return false, err
	}
	slog.Info("dockerclient: orphan cleanup: removed container", "id", id, "status", status)
	return true, nil
}

// isContainerTerminal reports whether Docker has already proved that a
// labelled container is stopped and therefore safe to remove.
func isContainerTerminal(status string) bool {
	switch status {
	case "exited", "dead":
		return true
	default:
		return false
	}
}
