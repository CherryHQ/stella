package manifest

import "context"

// WarmBuiltinArtifacts downloads release-declared binaries into the shared mise
// cache. It publishes no session selection; only an authorized snapshot may do
// that. mise owns installed-version state, so there is no parallel cache index.
func WarmBuiltinArtifacts(ctx context.Context, manifest *Manifest, stellaHome string) error {
	var tools []miseTool
	for _, plugin := range manifest.Plugins {
		if !plugin.Enabled {
			continue
		}
		for _, binary := range plugin.Binaries {
			tools = append(tools, miseToolFromBinary(binary))
		}
	}
	if len(tools) == 0 {
		return nil
	}
	miseInstallMu.Lock()
	defer miseInstallMu.Unlock()
	return withNativeMiseInstall(ctx, stellaHome, miseToolsDir(stellaHome), tools, nil)
}
