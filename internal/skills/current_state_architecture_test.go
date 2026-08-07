package skills

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Guard the cutover boundary across production packages. The two migration
// files are the only code permitted to read the retired SQL current state.
func TestNoLegacyPostgresSkillCurrentStateAuthority(t *testing.T) {
	repoRoot := repositoryRoot(t)
	legacyForbiddenQueryCalls := map[string]struct{}{
		"CreateSkill": {}, "DeleteReflectOwnedUserAgentSkill": {}, "DeleteSkill": {}, "DeleteSkillFile": {},
		"DeleteSystemSkill": {}, "GetSkill": {}, "GetSkillByID": {}, "GetSkillFile": {}, "GetSkillForUpdate": {},
		"GetSystemAgentSkillByName": {}, "GetSystemSkillByName": {}, "GetUserAgentSkillByName": {}, "GetUserSkillByName": {},
		"GetSkillMigrationChangelogBounds": {}, "GetSkillMigrationFileBounds": {}, "GetSkillUsageForUpdate": {},
		"InsertSkillChangelog": {}, "ListActiveReflectOwnedUserAgentSkills": {}, "ListAllSkills": {}, "ListSkillChangelogBySkill": {},
		"ListSkillFilePaths": {}, "ListSkillFiles": {}, "ListSkillMigrationChangelog": {}, "ListSkillMigrationSource": {},
		"ListSkillUsageForMigration": {}, "ListSkillsByScope": {}, "ListSkillsForAdmin": {},
		"ListSkillsForAgentContext": {}, "ListSkillsForUser": {}, "ListSkillsVisible": {}, "ResolveSkill": {},
		"CountSkillMigrationSource": {}, "LockSkillMigrationSource": {},
		"UpdateManagedSkill": {}, "UpdateReflectOwnedUserAgentSkill": {}, "UpdateSkillMetadata": {},
		"UpdateSystemSkillMetadata": {}, "UpsertSkillFile": {},
	}
	migrationAllowedReadCalls := map[string]map[string]struct{}{
		"internal/skills/home_migration.go": {
			"GetSkillByID": {}, "GetSkillMigrationChangelogBounds": {}, "GetSkillMigrationFileBounds": {},
			"GetSkillUsageForUpdate": {}, "ListSkillFiles": {}, "ListSkillMigrationChangelog": {},
			"ListSkillMigrationSource": {}, "ListSkillUsageForMigration": {},
		},
		"internal/skills/home_authority.go": {
			"CountSkillMigrationSource": {}, "LockSkillMigrationSource": {},
		},
	}

	for _, sourceRoot := range []string{"cmd", "internal", "plugins", "pkg"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, sourceRoot), func(filename string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() && (entry.Name() == "vendor" || filename == filepath.Join(repoRoot, "pkg", "db", "sqlc")) {
				return filepath.SkipDir
			}
			if entry.IsDir() || !strings.HasSuffix(filename, ".go") || strings.HasSuffix(filename, "_test.go") {
				return nil
			}

			relative, err := filepath.Rel(repoRoot, filename)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.Ident:
					if node.Name == "PGStore" {
						t.Errorf("%s revives legacy PG Skill current state: %s", relative, node.Name)
					}
				case *ast.CallExpr:
					if symbol, forbidden := legacySQLCQueryCall(node, legacyForbiddenQueryCalls); forbidden {
						if _, allowed := migrationAllowedReadCalls[relative][symbol]; !allowed {
							t.Errorf("%s revives legacy PG Skill current state: %s", relative, symbol)
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// Some retired sqlc method names are also the current Home Store interface.
// Restrict those names to query-shaped receivers so legitimate Home calls do
// not mask a PostgreSQL regression or become false positives.
func legacySQLCQueryCall(call *ast.CallExpr, retired map[string]struct{}) (string, bool) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		_, ok := retired[fun.Name]
		return fun.Name, ok
	case *ast.SelectorExpr:
		if _, ok := retired[fun.Sel.Name]; !ok {
			return "", false
		}
		switch fun.Sel.Name {
		case "DeleteReflectOwnedUserAgentSkill", "ListActiveReflectOwnedUserAgentSkills", "UpdateManagedSkill", "UpdateReflectOwnedUserAgentSkill":
			return fun.Sel.Name, isSQLCQueryReceiver(fun.X)
		default:
			return fun.Sel.Name, true
		}
	}
	return "", false
}

func isSQLCQueryReceiver(expr ast.Expr) bool {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name == "q" || expr.Name == "qtx" || expr.Name == "queries"
	case *ast.SelectorExpr:
		return expr.Sel.Name == "q"
	default:
		return false
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root containing go.mod not found")
		}
		dir = parent
	}
}
