package dockerclient

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"golang.org/x/sync/singleflight"
)

var (
	imageReadyMu    sync.Mutex
	imageReady      = map[string]struct{}{}
	imageReadyGroup singleflight.Group
)

// EnsureImageReady makes sure image exists locally, pulling it once per process
// when needed. Concurrent callers for the same image share the same inspect/pull.
func (c *Client) EnsureImageReady(ctx context.Context, image, containerName string) error {
	if image == "" {
		return fmt.Errorf("dockerclient: image is required")
	}
	if isImageReady(image) {
		return nil
	}

	_, err, _ := imageReadyGroup.Do(image, func() (any, error) {
		if isImageReady(image) {
			return nil, nil
		}
		slog.Info("dockerclient: checking sandbox image", "image", image, "container_name", containerName)
		exists, err := c.ImageExists(ctx, image)
		if err != nil {
			slog.Warn("dockerclient: image check failed", "image", image, "container_name", containerName, "error", err)
			return nil, fmt.Errorf("dockerclient: image check %s: %w", image, err)
		}
		if !exists {
			slog.Info("dockerclient: image not found locally, pulling", "image", image, "container_name", containerName)
			if err := c.PullImage(ctx, image); err != nil {
				slog.Warn("dockerclient: image pull failed", "image", image, "container_name", containerName, "error", err)
				return nil, err
			}
		}
		markImageReady(image)
		return nil, nil
	})
	return err
}

func isImageReady(image string) bool {
	imageReadyMu.Lock()
	defer imageReadyMu.Unlock()
	_, ok := imageReady[image]
	return ok
}

func markImageReady(image string) {
	imageReadyMu.Lock()
	defer imageReadyMu.Unlock()
	imageReady[image] = struct{}{}
}

func resetImageReadyForTest() {
	imageReadyMu.Lock()
	defer imageReadyMu.Unlock()
	imageReady = map[string]struct{}{}
	imageReadyGroup = singleflight.Group{}
}
