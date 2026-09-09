package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/home"
)

type packageCopyReader struct {
	revision PackageSkillRevision
	err      error
	calls    int
}

func (r *packageCopyReader) ReadPackageSkill(_ context.Context, _ authz.Authority, pluginID, digest, name string) (PackageSkillRevision, error) {
	r.calls++
	if r.err != nil {
		return PackageSkillRevision{}, r.err
	}
	if r.revision.Ref.PackageID != pluginID || r.revision.Ref.PackageDigest != digest || r.revision.Ref.Name != name {
		return PackageSkillRevision{}, errors.New("foreign package reference")
	}
	return r.revision, nil
}

type packageCopyStore struct {
	managementRegressionStore
	files map[string]ManagedSkillFile
}

type filePackageCopyAccess struct {
	userID  string
	current Skill
}

type filePackageCopyFixture struct {
	store  *FileStore
	userID string
}

func newFilePackageCopyFixture(t *testing.T) filePackageCopyFixture {
	t.Helper()
	db := dbtest.New(t)
	userID := "00000000-0000-4000-8000-000000000125"
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user(id,email) VALUES($1,$2)`, userID, userID+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO agent(id,name,workspace) VALUES('package-copy-agent','Package copy','')`); err != nil {
		t.Fatal(err)
	}
	manager, err := home.NewWorkspaceManager(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return filePackageCopyFixture{store: NewFileStore(db, manager), userID: userID}
}

func (a filePackageCopyAccess) ManageScope(context.Context, authz.Authority, string, string) (string, string, error) {
	return a.userID, "", nil
}

func (a filePackageCopyAccess) ManageByID(context.Context, authz.Authority, string, authz.Action) (Skill, error) {
	return a.current, nil
}

func (s *packageCopyStore) CreateManagedSkillWithFiles(_ context.Context, skill Skill, files map[string]ManagedSkillFile) (SkillSnapshot, error) {
	s.files = files
	return SkillSnapshot{Skill: skill}, nil
}

func TestManagementCopyPackageSkillPreservesProvenanceBytesAndMode(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	reader := &packageCopyReader{revision: PackageSkillRevision{
		Ref:   PackageSkillRef{PackageID: "demo", PackageDigest: digest, Name: "docs", Description: "package docs"},
		Files: map[string][]byte{MainFile: []byte("---\nname: docs\ndescription: package docs\n---\n"), "scripts/run": {0, 1, 2, 255}},
		Modes: map[string]fs.FileMode{MainFile: 0o644, "scripts/run": 0o755},
	}}
	store := &packageCopyStore{}
	m := NewManagement(store, managementRegressionAccess{}, WithPackageSkillReader(reader))
	snapshot, err := m.CopyPackageSkill(t.Context(), authz.Authority{}, ManagedPackageCopy{
		SourcePluginID: "demo", ExpectedPackageDigest: digest, SkillName: "docs", Scope: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Skill.Name != "docs" || snapshot.Skill.Description != "package docs" {
		t.Fatalf("copied identity = %#v", snapshot.Skill)
	}
	if got := string(store.files[MainFile].Content); got != string(reader.revision.Files[MainFile]) || store.files["scripts/run"].Mode != 0o755 || string(store.files["scripts/run"].Content) != string([]byte{0, 1, 2, 255}) {
		t.Fatalf("copied files = %#v, want bytes and executable mode", store.files)
	}
	if !strings.Contains(string(snapshot.Skill.Metadata), `"source_plugin_id":"demo"`) || !strings.Contains(string(snapshot.Skill.Metadata), digest) {
		t.Fatalf("copy provenance = %s", snapshot.Skill.Metadata)
	}
}

func TestManagementCopyPackageSkillRejectsWrongDigestAndForeignReader(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	reader := &packageCopyReader{revision: PackageSkillRevision{Ref: PackageSkillRef{PackageID: "demo", PackageDigest: digest, Name: "docs"}, Files: map[string][]byte{MainFile: []byte("body")}, Modes: map[string]fs.FileMode{MainFile: 0o644}}}
	store := &packageCopyStore{}
	m := NewManagement(store, managementRegressionAccess{}, WithPackageSkillReader(reader))
	if _, err := m.CopyPackageSkill(t.Context(), authz.Authority{}, ManagedPackageCopy{SourcePluginID: "demo", ExpectedPackageDigest: "bad", SkillName: "docs", Scope: "user"}); !errors.Is(err, ErrInvalidSkillRevision) {
		t.Fatalf("wrong digest error = %v, want invalid revision", err)
	}
	if reader.calls != 0 {
		t.Fatalf("wrong digest reached source reader %d times", reader.calls)
	}
	reader.revision.Ref.PackageID = "other-user-visible-package"
	if _, err := m.CopyPackageSkill(t.Context(), authz.Authority{}, ManagedPackageCopy{SourcePluginID: "demo", ExpectedPackageDigest: digest, SkillName: "docs", Scope: "user"}); err == nil {
		t.Fatal("foreign package reader result was accepted")
	}
	if store.files != nil {
		t.Fatal("foreign source reached managed store")
	}
}

func TestManagementCopyPackageSkillUsesIndependentFileRevision(t *testing.T) {
	f := newFilePackageCopyFixture(t)
	digest := "sha256:" + strings.Repeat("c", 64)
	sourceMainV1 := "---\nname: docs\ndescription: source v1\nstatus: active\nmetadata: {}\n---\nsource v1\n"
	reader := &packageCopyReader{revision: PackageSkillRevision{
		Ref:   PackageSkillRef{PackageID: "demo", PackageDigest: digest, Name: "docs", Description: "source v1"},
		Files: map[string][]byte{MainFile: []byte(sourceMainV1), "bin/run": {0, 1, 2, 255}},
		Modes: map[string]fs.FileMode{MainFile: 0o644, "bin/run": 0o755},
	}}
	authority := authz.Authority{}
	access := &filePackageCopyAccess{userID: f.userID}
	m := NewManagement(f.store, access, WithPackageSkillReader(reader))
	snapshot, err := m.CopyPackageSkill(t.Context(), authority, ManagedPackageCopy{
		SourcePluginID: "demo", ExpectedPackageDigest: digest, SkillName: "docs", Scope: "user",
	})
	if err != nil {
		t.Fatalf("copy into file store: %v", err)
	}
	var wantMetadata map[string]any
	if err := json.Unmarshal(snapshot.Skill.Metadata, &wantMetadata); err != nil {
		t.Fatalf("copy metadata = %s: %v", snapshot.Skill.Metadata, err)
	}
	assertRevision := func(label string, revision ManagedRevision, wantBody, wantDescription string) {
		t.Helper()
		if len(revision.Files) != 2 || len(revision.Modes) != 2 {
			t.Fatalf("%s files = %#v/%#v, want complete two-file revision", label, revision.Files, revision.Modes)
		}
		fields, body, err := parseSkillDocument(revision.Files[MainFile])
		if err != nil {
			t.Fatalf("%s SKILL.md frontmatter: %v", label, err)
		}
		frontmatter, err := parseFrontmatter(string(revision.Files[MainFile]))
		if err != nil {
			t.Fatalf("%s SKILL.md fields: %v", label, err)
		}
		if frontmatter.Name != "docs" || frontmatter.Description != wantDescription || frontmatter.Status != SkillStatusActive || frontmatter.DisableModelInvocation {
			t.Fatalf("%s frontmatter = %#v, want docs/%q/active/enabled", label, frontmatter, wantDescription)
		}
		if !bytes.Equal(body, []byte(wantBody)) || !reflect.DeepEqual(frontmatter.Metadata, wantMetadata) {
			t.Fatalf("%s body/metadata = %q/%#v, want %q/%#v (fields=%#v)", label, body, frontmatter.Metadata, wantBody, wantMetadata, fields)
		}
		if revision.Modes[MainFile] != 0o444 || revision.Modes["bin/run"] != 0o555 {
			t.Fatalf("%s modes = %#v, want SKILL.md 0444 and executable 0555", label, revision.Modes)
		}
		if !bytes.Equal(revision.Files["bin/run"], []byte{0, 1, 2, 255}) {
			t.Fatalf("%s binary = %v, want source bytes", label, revision.Files["bin/run"])
		}
	}
	access.current = snapshot.Skill
	current, err := f.store.LoadCurrentRevision(t.Context(), snapshot.Skill)
	if err != nil {
		t.Fatal(err)
	}
	assertRevision("copied revision", current, "source v1\n", "source v1")

	edited := "managed edit\n"
	updated, err := m.Update(t.Context(), authority, ManagedUpdate{
		ID: snapshot.Skill.ID, ExpectedVersion: snapshot.Skill.ContentDigest,
		Files: map[string]string{MainFile: edited},
	})
	if err != nil {
		t.Fatalf("edit copied Skill: %v", err)
	}
	access.current = updated.Skill
	reader.revision.Ref.Description = "source v2"
	reader.revision.Files[MainFile] = []byte("---\nname: docs\ndescription: source v2\nstatus: active\nmetadata: {}\n---\nsource v2\n")
	reader.revision.Files["bin/run"] = []byte("source v2 binary")
	loaded, err := f.store.LoadCurrentRevision(t.Context(), updated.Skill)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Skill.Description != "source v1" {
		t.Fatalf("managed copy description changed after source update = %q", loaded.Skill.Description)
	}
	assertRevision("managed copy after source update", loaded, edited, "source v1")
}
