//go:build !linux && !darwin

package local

import (
	"os"
	"path/filepath"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestOtherPlatformTmpMountsExposeConfiguredAndFallbackViews(t *testing.T) {
	workspace := t.TempDir()
	configuredTemp := t.TempDir()

	for _, tc := range []struct {
		name   string
		policy sandboxpkg.Policy
		want   string
	}{
		{
			name: "configured",
			policy: sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{
				WorkspaceRoot: workspace,
				WorkingDir:    workspace,
				TempDirHost:   configuredTemp,
			}},
			want: configuredTemp,
		},
		{
			name: "fallback",
			policy: sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{
				WorkspaceRoot: workspace,
				WorkingDir:    workspace,
			}},
			want: os.TempDir(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mounts, err := createSessionTmpMounts(tc.policy)
			if err != nil {
				t.Fatalf("createSessionTmpMounts: %v", err)
			}
			if len(mounts) != 1 {
				t.Fatalf("tmp mounts = %#v, want one identity mount", mounts)
			}
			if got := mounts[0]; got.sandboxPath != tc.want || got.realPath != tc.want || got.owned {
				t.Errorf("tmp mount = %#v, want identity mount for %q", got, tc.want)
			}
			if got := filesystemTempDir(mounts); got != tc.want {
				t.Errorf("filesystemTempDir = %q, want %q", got, tc.want)
			}

			session := &localSession{
				realRoot:    workspace,
				sandboxRoot: workspace,
				tmpMounts:   mounts,
				policy:      tc.policy,
			}
			wantPath := filepath.Join(tc.want, "tmp-file")
			if got, err := session.ResolveWritePath(wantPath); err != nil || got != wantPath {
				t.Errorf("ResolveWritePath(%q) = %q, %v; want %q, nil", wantPath, got, err, wantPath)
			}
		})
	}
}
