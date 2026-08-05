package access

import (
	"errors"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestWorkspacePathCanonicalizesAliasesAndCanonicalMounts(t *testing.T) {
	tests := []struct {
		input     string
		scope     WorkspaceScope
		allowRoot bool
		wantScope WorkspaceScope
		wantRoot  string
		wantPath  string
		wantErr   error
	}{
		{"notes/todo.md", WorkspaceScopeAgent, false, WorkspaceScopeAgent, "/workspace", "/workspace/notes/todo.md", nil},
		{"$HOME/notes/todo.md", WorkspaceScopeUser, false, WorkspaceScopeAgent, "/workspace", "/workspace/notes/todo.md", nil},
		{"$STELLA_ASSETS_DIR/202608/file.png", WorkspaceScopeAgent, false, WorkspaceScopeUser, "/user", "/user/assets/202608/file.png", nil},
		{"$STELLA_ASSETS_DIR", WorkspaceScopeAgent, true, WorkspaceScopeUser, "/user", "/user/assets", nil},
		{"/workspace/notes/todo.md", WorkspaceScopeUser, false, WorkspaceScopeAgent, "/workspace", "/workspace/notes/todo.md", nil},
		{"/user/assets/202608/file.png", WorkspaceScopeAgent, false, WorkspaceScopeUser, "/user", "/user/assets/202608/file.png", nil},
		{"/workspace", WorkspaceScopeUser, true, WorkspaceScopeAgent, "/workspace", "/workspace", nil},
		{"/user", WorkspaceScopeAgent, false, "", "", "", ErrInvalid},
		{"/workspace/../user/assets/file.png", WorkspaceScopeAgent, false, "", "", "", ErrInvalid},
		{"/private/stella/secret", WorkspaceScopeUser, false, "", "", "", ErrInvalid},
		{"C:/private/stella/secret", WorkspaceScopeUser, false, "", "", "", ErrInvalid},
		{"../secret", WorkspaceScopeUser, false, "", "", "", ErrInvalid},
		{"assets//secret", WorkspaceScopeUser, false, "", "", "", ErrInvalid},
		{"$UNKNOWN/secret", WorkspaceScopeUser, false, "", "", "", ErrInvalid},
		{"${STELLA_ASSETS_DIR", WorkspaceScopeUser, false, "", "", "", ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			scope, root, got, err := workspacePath(tt.scope, tt.input, tt.allowRoot)
			if !errors.Is(err, tt.wantErr) || scope != tt.wantScope || root != tt.wantRoot || got != tt.wantPath {
				t.Fatalf("workspacePath(%q) = (%q, %q, %q, %v), want (%q, %q, %q, %v)", tt.input, scope, root, got, err, tt.wantScope, tt.wantRoot, tt.wantPath, tt.wantErr)
			}
		})
	}
}

func TestWorkspaceFilesystemErrorMapsReadLimitsAndMisses(t *testing.T) {
	if !errors.Is(workspaceFilesystemError(pkgsandbox.ErrReadLimit), ErrTooLarge) {
		t.Fatal("read limit did not map to ErrTooLarge")
	}
}
