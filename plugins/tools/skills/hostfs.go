package skills

import (
	"context"
	"path/filepath"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type skillPathInfo struct {
	Exists bool
	IsDir  bool
}

func resolveSkillRuntime(_ context.Context, runtime pkgplugins.ToolRuntime, path string) (pkgplugins.ToolRuntime, func(), error) {
	if runtime != nil {
		return runtime, func() {}, nil
	}
	workingDir := path
	if workingDir == "" {
		workingDir = "."
	}
	if ext := filepath.Ext(workingDir); ext != "" {
		workingDir = filepath.Dir(workingDir)
	}
	return pkgplugins.NewLocalToolRuntime(workingDir), func() {}, nil
}

func statSkillPath(ctx context.Context, runtime pkgplugins.ToolRuntime, path string) (skillPathInfo, error) {
	runtime, closeRuntime, err := resolveSkillRuntime(ctx, runtime, path)
	if err != nil {
		return skillPathInfo{}, err
	}
	defer closeRuntime()
	stat, err := runtime.Stat(ctx, path)
	if err != nil {
		return skillPathInfo{}, err
	}
	return skillPathInfo{Exists: stat.Exists, IsDir: stat.IsDir}, nil
}

func entryIsRegular(ctx context.Context, runtime pkgplugins.ToolRuntime, path string) bool {
	info, err := statSkillPath(ctx, runtime, path)
	return err == nil && info.Exists && !info.IsDir
}

func readSkillDir(ctx context.Context, runtime pkgplugins.ToolRuntime, path string) ([]pkgplugins.DirEntry, error) {
	runtime, closeRuntime, err := resolveSkillRuntime(ctx, runtime, path)
	if err != nil {
		return nil, err
	}
	defer closeRuntime()
	return runtime.ListDir(ctx, path)
}

func readSkillFile(ctx context.Context, runtime pkgplugins.ToolRuntime, path string) ([]byte, error) {
	runtime, closeRuntime, err := resolveSkillRuntime(ctx, runtime, path)
	if err != nil {
		return nil, err
	}
	defer closeRuntime()
	result, err := runtime.ReadFile(ctx, path, 0, 0)
	if err != nil {
		return nil, err
	}
	return result.Content, nil
}

func writeSkillFile(ctx context.Context, runtime pkgplugins.ToolRuntime, path string, data []byte, _ uint32) error {
	runtime, closeRuntime, err := resolveSkillRuntime(ctx, runtime, path)
	if err != nil {
		return err
	}
	defer closeRuntime()
	if err := runtime.MkdirAll(ctx, filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, err = runtime.WriteFile(ctx, path, data)
	return err
}
