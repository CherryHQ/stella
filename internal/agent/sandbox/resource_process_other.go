//go:build !linux && !windows

package sandbox

import (
	"context"
	"fmt"
)

func cleanupHostProcessResource(_ context.Context, resourceID string) error {
	return fmt.Errorf("cannot prove host sandbox process resource %q absent on this platform", resourceID)
}
