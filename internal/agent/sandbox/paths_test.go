package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePaths_resolvesSymlinks(t *testing.T) {
	actualDir := t.TempDir()
	actualDir, _ = filepath.EvalSymlinks(actualDir)

	symlinkParent := t.TempDir()
	symlinkPath := filepath.Join(symlinkParent, "link")
	if err := os.Symlink(actualDir, symlinkPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	agentRoot := t.TempDir()
	agentRoot, _ = filepath.EvalSymlinks(agentRoot)

	cfg := Config{
		Paths: Paths{
			StellaHome: t.TempDir(),
			AgentRoot:  agentRoot,
			UserRoot:   symlinkPath,
		},
	}
	paths, err := ResolvePaths(cfg)
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}

	if paths.UserRoot != actualDir {
		t.Errorf("UserRoot = %q, want %q (symlink resolved)", paths.UserRoot, actualDir)
	}
	if paths.WorkDir != actualDir {
		t.Errorf("WorkDir = %q, want %q (symlink resolved)", paths.WorkDir, actualDir)
	}
}

func TestResolvePaths_projectRootSymlink(t *testing.T) {
	userRoot := t.TempDir()
	userRoot, _ = filepath.EvalSymlinks(userRoot)
	subDir := filepath.Join(userRoot, "project")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	symlinkParent := t.TempDir()
	projectLink := filepath.Join(symlinkParent, "proj-link")
	if err := os.Symlink(subDir, projectLink); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	agentRoot := t.TempDir()
	agentRoot, _ = filepath.EvalSymlinks(agentRoot)

	cfg := Config{
		Paths: Paths{
			StellaHome:  t.TempDir(),
			AgentRoot:   agentRoot,
			UserRoot:    userRoot,
			ProjectRoot: projectLink,
		},
	}
	paths, err := ResolvePaths(cfg)
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}

	if paths.ProjectRoot != subDir {
		t.Errorf("ProjectRoot = %q, want %q (symlink resolved)", paths.ProjectRoot, subDir)
	}
	if paths.WorkDir != subDir {
		t.Errorf("WorkDir = %q, want %q (symlink resolved)", paths.WorkDir, subDir)
	}
}

func TestResolvePathsAcceptsOnlyExactAuthorizedRoots(t *testing.T) {
	userRoot := t.TempDir()
	// ResolvePaths compares fully resolved paths, and macOS hands out temp dirs
	// under the /var -> /private/var symlink. Resolve here like the tests above.
	userRoot, _ = filepath.EvalSymlinks(userRoot)
	workspace := filepath.Join(userRoot, "agents", "agent")
	data := filepath.Join(userRoot, "data")
	for _, dir := range []string{workspace, data} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{
		UserID:  "user",
		AgentID: "agent",
		Paths: Paths{
			StellaHome:    t.TempDir(),
			AgentRoot:     t.TempDir(),
			UserRoot:      userRoot,
			WorkspaceRoot: workspace,
			UserDataDir:   data,
		},
	}
	paths, err := ResolvePaths(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if paths.WorkspaceRoot != workspace || paths.UserDataDir != data {
		t.Fatalf("explicit roots = %q, %q", paths.WorkspaceRoot, paths.UserDataDir)
	}

	cfg.Paths.WorkspaceRoot = filepath.Join(userRoot, "agents", "other")
	if _, err := ResolvePaths(cfg); err == nil {
		t.Fatal("mismatched workspace root accepted")
	}
	cfg.Paths.WorkspaceRoot = workspace
	cfg.Paths.UserDataDir = filepath.Join(userRoot, "other-data")
	if _, err := ResolvePaths(cfg); err == nil {
		t.Fatal("mismatched user data root accepted")
	}
}
