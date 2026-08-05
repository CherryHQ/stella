package share

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestProductionShareHasNoHostArtifactAuthority keeps artifact publication on
// the exact-session filesystem boundary rather than reintroducing host paths.
func TestProductionShareHasNoHostArtifactAuthority(t *testing.T) {
	for _, name := range []string{"service.go", "access.go", "tool.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, "`\"")
			switch path {
			case "github.com/CherryHQ/stella/internal/asset", "github.com/CherryHQ/stella/internal/home", "os", "path/filepath":
				t.Errorf("%s imports forbidden host authority %q", name, path)
			}
		}
	}
}
