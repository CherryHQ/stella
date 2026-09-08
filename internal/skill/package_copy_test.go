package skill

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
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

type posixPackageCopyAccess struct {
	userID  string
	current Skill
}

func (a posixPackageCopyAccess) ManageScope(context.Context, authz.Authority, string, string) (string, string, error) {
	return a.userID, "", nil
}

func (a posixPackageCopyAccess) ManageByID(context.Context, authz.Authority, string, authz.Action) (Skill, error) {
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

func TestManagementCopyPackageSkillUsesIndependentPOSIXRevision(t *testing.T) {
	f := newPOSIXStoreFixture(t)
	digest := "sha256:" + strings.Repeat("c", 64)
	reader := &packageCopyReader{revision: PackageSkillRevision{
		Ref:   PackageSkillRef{PackageID: "demo", PackageDigest: digest, Name: "docs", Description: "source v1"},
		Files: map[string][]byte{MainFile: []byte("source v1\n"), "bin/run": {0, 1, 2, 255}},
		Modes: map[string]fs.FileMode{MainFile: 0o644, "bin/run": 0o755},
	}}
	authority := authz.Authority{}
	access := &posixPackageCopyAccess{userID: f.userID}
	m := NewManagement(f.store, access, WithPackageSkillReader(reader))
	snapshot, err := m.CopyPackageSkill(t.Context(), authority, ManagedPackageCopy{
		SourcePluginID: "demo", ExpectedPackageDigest: digest, SkillName: "docs", Scope: "user",
	})
	if err != nil {
		t.Fatalf("copy into POSIX store: %v", err)
	}
	access.current = snapshot.Skill
	current, err := f.store.LoadCurrentRevision(t.Context(), snapshot.Skill)
	if err != nil {
		t.Fatal(err)
	}
	if string(current.Files[MainFile]) != "source v1\n" || current.Modes["bin/run"] != 0o755 || string(current.Files["bin/run"]) != string([]byte{0, 1, 2, 255}) {
		t.Fatalf("copied revision = %#v/%#v, want source bytes and executable mode", current.Files, current.Modes)
	}

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
	reader.revision.Files[MainFile] = []byte("source v2\n")
	reader.revision.Files["bin/run"] = []byte("source v2 binary")
	loaded, err := f.store.LoadCurrentRevision(t.Context(), updated.Skill)
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded.Files[MainFile]) != edited || string(loaded.Files["bin/run"]) != string([]byte{0, 1, 2, 255}) || loaded.Skill.Description != "source v1" {
		t.Fatalf("managed copy changed after source update = %#v/%#v/%q", loaded.Files, loaded.Modes, loaded.Skill.Description)
	}
}
