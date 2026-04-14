package agent

import (
	"context"
	"path/filepath"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type hostPathInfo struct {
	Exists bool
	IsDir  bool
}

func resolvePresetRuntime(runtime pkgplugins.ToolRuntime, path string) pkgplugins.ToolRuntime {
	if runtime != nil {
		return runtime
	}
	workingDir := path
	if workingDir == "" {
		workingDir = "."
	}
	if ext := filepath.Ext(workingDir); ext != "" {
		workingDir = filepath.Dir(workingDir)
	}
	return pkgplugins.NewLocalToolRuntime(workingDir)
}

func statHostPath(ctx context.Context, runtime pkgplugins.ToolRuntime, path string) (hostPathInfo, error) {
	stat, err := resolvePresetRuntime(runtime, path).Stat(ctx, path)
	if err != nil {
		return hostPathInfo{}, err
	}
	return hostPathInfo{Exists: stat.Exists, IsDir: stat.IsDir}, nil
}

func readHostDir(ctx context.Context, runtime pkgplugins.ToolRuntime, path string) ([]pkgplugins.DirEntry, error) {
	return resolvePresetRuntime(runtime, path).ListDir(ctx, path)
}

func readHostFile(ctx context.Context, runtime pkgplugins.ToolRuntime, path string) ([]byte, error) {
	result, err := resolvePresetRuntime(runtime, path).ReadFile(ctx, path, 0, 0)
	if err != nil {
		return nil, err
	}
	return result.Content, nil
}
