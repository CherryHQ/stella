package dockerclient

import (
	"context"
	"fmt"
	"log/slog"
)

// EnsureImageReady makes sure image exists locally, pulling it once per Client
// when needed. Concurrent callers for the same image share the same inspect/pull.
func (c *Client) EnsureImageReady(ctx context.Context, image, containerName string) error {
	if image == "" {
		return fmt.Errorf("dockerclient: image is required")
	}
	if c.isImageReady(image) {
		return nil
	}

	_, err, _ := c.imageReadyGroup.Do(image, func() (any, error) {
		if c.isImageReady(image) {
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
		c.markImageReady(image)
		return nil, nil
	})
	return err
}

func (c *Client) isImageReady(image string) bool {
	c.imageReadyMu.Lock()
	defer c.imageReadyMu.Unlock()
	_, ok := c.imageReady[image]
	return ok
}

func (c *Client) markImageReady(image string) {
	c.imageReadyMu.Lock()
	defer c.imageReadyMu.Unlock()
	c.imageReady[image] = struct{}{}
}
