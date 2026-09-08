package plugin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

func TestAccessReadPackageSkillChecksDeclarationDigestAndRetirement(t *testing.T) {
	db := dbtest.New(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	catalog := NewCatalog()
	definition := Definition{
		ID: "copy-builtin", DisplayName: "Copy builtin", Source: SourceBuiltin,
		Spec:           publishedSpec(t, fmt.Sprintf(`{"content":{"digest":%q},"skills":[{"name":"docs","description":"Docs"}]}`, digest)),
		DefaultEnabled: true, Revision: 1,
	}
	if err := catalog.Register(definition); err != nil {
		t.Fatal(err)
	}
	var reads int
	service := NewService(db, nil, catalog, BackendPolicy{Transition: inlineBackendPolicyTransition}, inlineBackendPolicyFence,
		WithBuiltinSkillReader(func(context.Context, string, string) (map[string][]byte, map[string]fs.FileMode, error) {
			reads++
			return map[string][]byte{"SKILL.md": {0, 1, 2, 255}}, map[string]fs.FileMode{"SKILL.md": 0o755}, nil
		}),
	)
	if err := service.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatalf("sync builtin defaults: %v", err)
	}
	authority, err := authz.NewUserAuthority("10000000-0000-0000-0000-000000000001", false)
	if err != nil {
		t.Fatal(err)
	}
	access, err := service.Begin(authority)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := access.ReadPackageSkill(t.Context(), definition.ID, "sha256:"+strings.Repeat("b", 64), "docs"); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong digest error = %v, want ErrConflict", err)
	}
	if reads != 0 {
		t.Fatalf("wrong digest opened builtin files %d times", reads)
	}
	if _, err := access.ReadPackageSkill(t.Context(), definition.ID, digest, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("undeclared Skill error = %v, want ErrNotFound", err)
	}
	if reads != 0 {
		t.Fatalf("undeclared Skill opened builtin files %d times", reads)
	}
	files, err := access.ReadPackageSkill(t.Context(), definition.ID, digest, "docs")
	if err != nil {
		t.Fatalf("read builtin Skill: %v", err)
	}
	if string(files.Files["SKILL.md"]) != string([]byte{0, 1, 2, 255}) || files.Modes["SKILL.md"] != 0o755 {
		t.Fatalf("builtin files/modes = %#v/%#v", files.Files, files.Modes)
	}
	if reads != 1 {
		t.Fatalf("builtin reader calls = %d, want 1", reads)
	}

	if _, err := db.Exec(t.Context(), `UPDATE plugin_definition SET retired_at = now() WHERE id = $1`, definition.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := access.ReadPackageSkill(t.Context(), definition.ID, digest, "docs"); !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("invalid retired builtin error = %v, want ErrInvalidDefinition", err)
	}
	if reads != 1 {
		t.Fatalf("retired definition opened builtin files %d times", reads)
	}
}

func TestAccessReadPackageSkillEnforcesCustomVisibilityAndRetirement(t *testing.T) {
	db := dbtest.New(t)
	owner := "10000000-0000-0000-0000-000000000002"
	other := "10000000-0000-0000-0000-000000000003"
	for _, user := range []string{owner, other} {
		if _, err := db.Exec(t.Context(), `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, user, user+"@skill.test"); err != nil {
			t.Fatal(err)
		}
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"$schema":"`+agentpackage.PluginSchemaV1+`","name":"private-package","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(source, "skills", "docs")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: docs\ndescription: private\n---\n\x00\xff\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	storeRoot := t.TempDir()
	published, err := agentpackage.PublishDirectory(source, storeRoot)
	if err != nil {
		t.Fatalf("publish package: %v", err)
	}
	spec := publishedSpec(t, fmt.Sprintf(`{"content":{"digest":%q},"skills":[{"name":"docs"}]}`, published.Digest))
	if _, err := db.Exec(t.Context(), `
		INSERT INTO plugin_definition (id, display_name, source, spec, default_enabled, revision, creator_user_id)
		VALUES ('private-package', 'Private package', 'custom', $1, false, 1, $2)
	`, spec, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `
		INSERT INTO plugin_config (plugin_id, scope, user_id, enabled, config, credential_refs, revision)
		VALUES ('private-package', 'user', $1, false, NULL, '{}'::jsonb, 1)
	`, owner); err != nil {
		t.Fatal(err)
	}
	contentStore, err := NewContentStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, NewCatalog(), BackendPolicy{Transition: inlineBackendPolicyTransition}, inlineBackendPolicyFence, WithContentStore(contentStore))
	ownerAuthority, err := authz.NewUserAuthority(authz.UserID(owner), false)
	if err != nil {
		t.Fatal(err)
	}
	ownerAccess, err := service.Begin(ownerAuthority)
	if err != nil {
		t.Fatal(err)
	}
	otherAuthority, err := authz.NewUserAuthority(authz.UserID(other), false)
	if err != nil {
		t.Fatal(err)
	}
	otherAccess, err := service.Begin(otherAuthority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherAccess.ReadPackageSkill(t.Context(), "private-package", published.Digest, "docs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign source visibility error = %v, want ErrNotFound", err)
	}
	read, err := ownerAccess.ReadPackageSkill(t.Context(), "private-package", published.Digest, "docs")
	if err != nil {
		t.Fatalf("owner package read: %v", err)
	}
	if string(read.Files["SKILL.md"]) == "" || read.Modes["SKILL.md"]&0o111 == 0 {
		t.Fatalf("owner package bytes/mode = %#v/%#o", read.Files, read.Modes["SKILL.md"])
	}
	if _, err := db.Exec(t.Context(), `UPDATE plugin_definition SET retired_at = now() WHERE id = 'private-package'`); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerAccess.ReadPackageSkill(t.Context(), "private-package", published.Digest, "docs"); !errors.Is(err, ErrRetiredDefinition) {
		t.Fatalf("retired source error = %v, want ErrRetiredDefinition", err)
	}
}
