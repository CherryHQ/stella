package sandbox

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyFilesystemEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		view FilesystemView
		want map[string]string
	}{
		{
			name: "POSIX sandbox view",
			env: map[string]string{
				"TOKEN":          "keep",
				EnvXDGRuntimeDir: "/run/user/1",
			},
			view: FilesystemView{
				Home:          "/workspace",
				SharedDataDir: "/user",
				TempDir:       "/tmp",
			},
			want: map[string]string{
				"TOKEN":            "keep",
				EnvHome:            "/workspace",
				EnvStellaAssetsDir: "/user/assets",
				EnvTempDir:         "/tmp",
				EnvXDGConfigHome:   "/user/.config",
				EnvXDGDataHome:     "/user/.local/share",
				EnvXDGStateHome:    "/user/.local/state",
				EnvXDGCacheHome:    "/user/.cache",
			},
		},
		{
			name: "group equivalent",
			env:  map[string]string{},
			view: FilesystemView{
				Home:          "/work/group-agent",
				SharedDataDir: "/data/group-team",
				TempDir:       "/scratch/group-agent",
			},
			want: map[string]string{
				EnvHome:            "/work/group-agent",
				EnvStellaAssetsDir: "/data/group-team/assets",
				EnvTempDir:         "/scratch/group-agent",
				EnvXDGConfigHome:   "/data/group-team/.config",
				EnvXDGDataHome:     "/data/group-team/.local/share",
				EnvXDGStateHome:    "/data/group-team/.local/state",
				EnvXDGCacheHome:    "/data/group-team/.cache",
			},
		},
		{
			name: "user less",
			env:  map[string]string{},
			view: FilesystemView{Home: "/workspace", TempDir: "/tmp"},
			want: map[string]string{
				EnvHome:          "/workspace",
				EnvTempDir:       "/tmp",
				EnvXDGConfigHome: "/workspace/.config",
				EnvXDGDataHome:   "/workspace/.local/share",
				EnvXDGStateHome:  "/workspace/.local/state",
				EnvXDGCacheHome:  "/workspace/.cache",
			},
		},
		{
			name: "Windows native view",
			env:  map[string]string{},
			view: FilesystemView{
				Home:          `C:\Stella\agents\agent-1`,
				SharedDataDir: `C:\Stella\users\user-1\`,
				TempDir:       `C:\Stella\tmp\agent-1`,
			},
			want: map[string]string{
				EnvHome:            `C:\Stella\agents\agent-1`,
				EnvStellaAssetsDir: `C:\Stella\users\user-1\assets`,
				EnvTempDir:         `C:\Stella\tmp\agent-1`,
				EnvXDGConfigHome:   `C:\Stella\users\user-1\.config`,
				EnvXDGDataHome:     `C:\Stella\users\user-1\.local\share`,
				EnvXDGStateHome:    `C:\Stella\users\user-1\.local\state`,
				EnvXDGCacheHome:    `C:\Stella\users\user-1\.cache`,
			},
		},
		{
			name: "UNC native view",
			env:  map[string]string{},
			view: FilesystemView{
				Home:          `\\server\share\agents\agent-1`,
				SharedDataDir: `\\server\share\users\user-1\`,
				TempDir:       `\\server\share\tmp\agent-1`,
			},
			want: map[string]string{
				EnvHome:            `\\server\share\agents\agent-1`,
				EnvStellaAssetsDir: `\\server\share\users\user-1\assets`,
				EnvTempDir:         `\\server\share\tmp\agent-1`,
				EnvXDGConfigHome:   `\\server\share\users\user-1\.config`,
				EnvXDGDataHome:     `\\server\share\users\user-1\.local\share`,
				EnvXDGStateHome:    `\\server\share\users\user-1\.local\state`,
				EnvXDGCacheHome:    `\\server\share\users\user-1\.cache`,
			},
		},
		{
			name: "missing shared mount clears stale roots",
			env: map[string]string{
				"STELLA_USER_DIR":  "/stale/user",
				EnvStellaAssetsDir: "/stale/user/assets",
				EnvXDGRuntimeDir:   "/run/user/1",
			},
			view: FilesystemView{Home: "/workspace", TempDir: "/tmp"},
			want: map[string]string{
				EnvHome:          "/workspace",
				EnvTempDir:       "/tmp",
				EnvXDGConfigHome: "/workspace/.config",
				EnvXDGDataHome:   "/workspace/.local/share",
				EnvXDGStateHome:  "/workspace/.local/state",
				EnvXDGCacheHome:  "/workspace/.cache",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ApplyFilesystemEnv(tt.env, tt.view); err != nil {
				t.Fatalf("ApplyFilesystemEnv: %v", err)
			}
			if diff := mapDiff(tt.want, tt.env); diff != "" {
				t.Fatalf("environment mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestJoinFilesystemRootPreservesRootPrefixes(t *testing.T) {
	tests := []struct {
		name   string
		root   string
		suffix string
		want   string
	}{
		{name: "POSIX volume", root: "/", suffix: ".config", want: "/.config"},
		{name: "POSIX backslash filename", root: "/srv/user\\", suffix: "assets", want: "/srv/user\\/assets"},
		{name: "Windows volume", root: `C:\`, suffix: ".config", want: `C:\.config`},
		{name: "UNC share", root: `\\server\share\`, suffix: ".config", want: `\\server\share\.config`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := joinFilesystemRoot(tt.root, tt.suffix); got != tt.want {
				t.Errorf("joinFilesystemRoot(%q, %q) = %q, want %q", tt.root, tt.suffix, got, tt.want)
			}
		})
	}
}

func TestApplyFilesystemEnvRequiresHomeAndTempDir(t *testing.T) {
	tests := []struct {
		name string
		view FilesystemView
		want string
	}{
		{name: "home", view: FilesystemView{TempDir: "/tmp"}, want: EnvHome},
		{name: "temp", view: FilesystemView{Home: "/workspace"}, want: EnvTempDir},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{
				"UNCHANGED":        "yes",
				EnvHome:            "/old/home",
				EnvStellaAssetsDir: "/old/user/assets",
				EnvXDGRuntimeDir:   "/old/runtime",
				EnvXDGConfigHome:   "/old/user/.config",
				EnvXDGDataHome:     "/old/user/.local/share",
				EnvXDGStateHome:    "/old/user/.local/state",
				EnvXDGCacheHome:    "/old/user/.cache",
			}
			wantEnv := maps.Clone(env)
			err := ApplyFilesystemEnv(env, tt.view)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ApplyFilesystemEnv error = %v, want %s", err, tt.want)
			}
			if diff := mapDiff(wantEnv, env); diff != "" {
				t.Fatalf("environment changed on invalid view (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExpandPathVariables(t *testing.T) {
	env := map[string]string{
		EnvHome:            "/policy/workspace",
		EnvStellaAssetsDir: "/policy/user/assets",
		EnvTempDir:         "/policy/tmp",
		"TOKEN":            "top-secret-value",
	}
	t.Setenv(EnvHome, "/host/home-must-not-be-used")

	tests := []struct {
		path string
		want string
	}{
		{"$HOME", "/policy/workspace"},
		{"${HOME}", "/policy/workspace"},
		{"$HOME/a", "/policy/workspace/a"},
		{"${HOME}/a", "/policy/workspace/a"},
		{"$STELLA_ASSETS_DIR", "/policy/user/assets"},
		{"${STELLA_ASSETS_DIR}", "/policy/user/assets"},
		{"$STELLA_ASSETS_DIR/a", "/policy/user/assets/a"},
		{"${STELLA_ASSETS_DIR}/a", "/policy/user/assets/a"},
		{"$TMPDIR", "/policy/tmp"},
		{"${TMPDIR}", "/policy/tmp"},
		{"$TMPDIR/a", "/policy/tmp/a"},
		{"${TMPDIR}/a", "/policy/tmp/a"},
		{"relative/path", "relative/path"},
		{"/literal/path", "/literal/path"},
		{"relative/$HOME/a", "relative/$HOME/a"},
		{"/literal/$HOME/a", "/literal/$HOME/a"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := ExpandPathVariables(tt.path, env)
			if err != nil {
				t.Fatalf("ExpandPathVariables(%q): %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("ExpandPathVariables(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}

	t.Run("backslash follows platform separator rules", func(t *testing.T) {
		got, err := ExpandPathVariables("$HOME\\a", env)
		if os.IsPathSeparator('\\') {
			if err != nil {
				t.Fatalf("ExpandPathVariables: %v", err)
			}
			if want := "/policy/workspace\\a"; got != want {
				t.Errorf("ExpandPathVariables = %q, want %q", got, want)
			}
			return
		}
		if err == nil {
			t.Fatalf("ExpandPathVariables = %q, want error for a non-separator backslash", got)
		}
	})
}

func TestExpandPathVariablesRejectsUnsupportedUnavailableAndMalformedExpressions(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		env         map[string]string
		wantMessage string
		secret      string
		forbidden   string
	}{
		{
			name:        "unknown secret variable",
			path:        "$TOKEN/a",
			env:         map[string]string{"TOKEN": "top-secret-value"},
			wantMessage: "unsupported leading path variable",
			secret:      "top-secret-value",
			forbidden:   "TOKEN",
		},
		{
			name:        "removed user dir variable",
			path:        "${STELLA_USER_DIR}/a",
			env:         map[string]string{"STELLA_USER_DIR": "/policy/user"},
			wantMessage: "unsupported leading path variable",
			forbidden:   "STELLA_USER_DIR",
		},
		{
			name:        "unbraced root suffix without separator",
			path:        "$HOME.txt",
			env:         map[string]string{EnvHome: "/policy/workspace"},
			wantMessage: "whole path or followed by a path separator",
		},
		{
			name:        "unbraced root name suffix",
			path:        "$HOMEsuffix",
			env:         map[string]string{EnvHome: "/policy/workspace"},
			wantMessage: "unsupported leading path variable",
		},
		{
			name:        "braced root suffix without separator",
			path:        "${HOME}suffix",
			env:         map[string]string{EnvHome: "/policy/workspace"},
			wantMessage: "whole path or followed by a path separator",
		},
		{
			name:        "managed XDG variable",
			path:        "$XDG_CONFIG_HOME/tool",
			env:         map[string]string{EnvXDGConfigHome: "/policy/user/.config"},
			wantMessage: "unsupported leading path variable",
			forbidden:   "XDG_CONFIG_HOME",
		},
		{
			name:        "bare dollar",
			path:        "$",
			wantMessage: "malformed leading path variable",
		},
		{
			name:        "unclosed braces",
			path:        "${HOME/a",
			wantMessage: "malformed leading path variable",
		},
		{
			name:        "empty braces",
			path:        "${}/a",
			wantMessage: "malformed leading path variable",
		},
		{
			name:        "invalid variable start",
			path:        "$1/a",
			wantMessage: "malformed leading path variable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandPathVariables(tt.path, tt.env)
			if err == nil {
				t.Fatalf("ExpandPathVariables(%q) = %q, want error", tt.path, got)
			}
			if got != "" {
				t.Errorf("ExpandPathVariables(%q) = %q, want empty path on error", tt.path, got)
			}
			if !strings.Contains(err.Error(), tt.wantMessage) {
				t.Errorf("error = %q, want message containing %q", err, tt.wantMessage)
			}
			if tt.secret != "" && strings.Contains(err.Error(), tt.secret) {
				t.Errorf("error exposed secret value %q", tt.secret)
			}
			if tt.forbidden != "" && strings.Contains(err.Error(), tt.forbidden) {
				t.Errorf("error exposed unsupported variable name %q", tt.forbidden)
			}
		})
	}
}

func TestExpandPathVariablesKeepsPathResolverConfinement(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	resolver := NewPathResolver(PathResolverConfig{
		WorkspaceRoot: workspace,
		WorkingDir:    workspace,
		Mounts: []Mount{{
			HostPath:    workspace,
			SandboxPath: "/workspace",
			Access:      MountReadWrite,
		}},
	})
	env := map[string]string{EnvHome: "/workspace"}

	t.Run("traversal", func(t *testing.T) {
		path, err := ExpandPathVariables("$HOME/../outside/file", env)
		if err != nil {
			t.Fatalf("ExpandPathVariables: %v", err)
		}
		if _, err := resolver.ResolveWritePath(path); err == nil {
			t.Fatal("ResolveWritePath accepted traversal outside workspace")
		}
	})

	t.Run("symlink escape", func(t *testing.T) {
		if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
			t.Fatalf("create symlink: %v", err)
		}
		path, err := ExpandPathVariables("${HOME}/escape/file", env)
		if err != nil {
			t.Fatalf("ExpandPathVariables: %v", err)
		}
		if _, err := resolver.ResolveWritePath(path); err == nil {
			t.Fatal("ResolveWritePath accepted a symlink escape")
		}
	})
}

func mapDiff(want, got map[string]string) string {
	var lines []string
	for key, wantValue := range want {
		if gotValue, ok := got[key]; !ok {
			lines = append(lines, "- "+key+"="+wantValue)
		} else if gotValue != wantValue {
			lines = append(lines, "- "+key+"="+wantValue, "+ "+key+"="+gotValue)
		}
	}
	for key, gotValue := range got {
		if _, ok := want[key]; !ok {
			lines = append(lines, "+ "+key+"="+gotValue)
		}
	}
	return strings.Join(lines, "\n")
}
