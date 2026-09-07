package toolinstall

import "context"

// Warm downloads release-declared tools into the shared mise cache. It publishes
// no selection; only an authorized snapshot may do that.
func Warm(ctx context.Context, stellaHome string, tools []Tool) error {
	if len(tools) == 0 {
		return nil
	}
	miseInstallMu.Lock()
	defer miseInstallMu.Unlock()
	return withNativeMiseInstall(ctx, stellaHome, miseToolsDir(stellaHome), tools, nil)
}
