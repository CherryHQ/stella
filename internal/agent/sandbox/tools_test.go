package sandbox

import (
	"path/filepath"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestLiteralToolPathsDoNotRequireSessionPolicy(t *testing.T) {
	for _, path := range []string{filepath.Join(t.TempDir(), "literal.txt"), "relative.txt"} {
		got, err := resolveToolExpression(nil, "", path)
		if err != nil {
			t.Fatalf("resolve literal path: %v", err)
		}
		if got != path {
			t.Errorf("resolved literal path = %q, want %q", got, path)
		}
	}
}

func TestToolIntArg(t *testing.T) {
	tests := []struct {
		args map[string]any
		key  string
		def  int
		want int
	}{
		{map[string]any{"n": float64(5)}, "n", 0, 5},
		{map[string]any{"n": 7}, "n", 0, 7},
		{map[string]any{"n": int32(3)}, "n", 0, 3},
		{map[string]any{"n": int64(9)}, "n", 0, 9},
		{map[string]any{}, "n", 42, 42},
		{map[string]any{"n": "bad"}, "n", 99, 99},
	}
	for _, tc := range tests {
		got := toolIntArg(tc.args, tc.key, tc.def)
		if got != tc.want {
			t.Errorf("toolIntArg(%v, %q, %d) = %d, want %d", tc.args, tc.key, tc.def, got, tc.want)
		}
	}
}

// Root expansion used to be covered through the read/write/edit tools. They are
// gone, so it is asserted here directly.
//
// Note what this does NOT cover: resolveToolExpression expands "$HOME/../x" to
// "/x" without complaint. Confinement is enforced downstream by the sandbox
// FileView, whose path resolution refuses anything outside a mount — the
// removed tools' traversal test was really exercising that layer through them,
// and it still holds for every remaining caller.
func TestResolveToolExpressionExpandsRoots(t *testing.T) {
	env := map[string]string{
		pkgsandbox.EnvHome:            "/workspace",
		pkgsandbox.EnvStellaAssetsDir: "/user/assets",
		pkgsandbox.EnvTempDir:         "/tmp",
	}

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"home", "$HOME/output.txt", "/workspace/output.txt"},
		{"assets", "$STELLA_ASSETS_DIR/upload.txt", "/user/assets/upload.txt"},
		{"tmpdir", "$TMPDIR/edit.txt", "/tmp/edit.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveToolExpression(env, "", tc.path)
			if err != nil {
				t.Fatalf("resolve %q: %v", tc.path, err)
			}
			if got != tc.want {
				t.Errorf("resolve %q = %q, want %q", tc.path, got, tc.want)
			}
		})
	}

	t.Run("unknown variable is refused", func(t *testing.T) {
		if got, err := resolveToolExpression(env, "/workspace", "$NOT_A_ROOT/file.txt"); err == nil {
			t.Fatalf("resolve unknown variable = %q, want an error", got)
		}
	})
}
