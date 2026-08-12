package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
)

func testSkillRoot(t *testing.T, identity Skill) home.SkillRootOperations {
	t.Helper()
	database, base := dbtest.New(t), t.TempDir()
	if identity.UserID != "" {
		if _, err := database.Exec(t.Context(), "INSERT INTO auth_user(id,email) VALUES($1,$2)", identity.UserID, identity.UserID+"@test.invalid"); err != nil {
			t.Fatal(err)
		}
	}
	if identity.AgentID != "" {
		if _, err := database.Exec(t.Context(), "INSERT INTO agent(id,name,model,workspace) VALUES($1,$1,'test/model',$2)", identity.AgentID, filepath.Join(base, "agent")); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := home.NewWorkspaceManager(database, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	request, scope, err := skillRootSelection(identity)
	if err != nil {
		t.Fatal(err)
	}
	root, err := manager.OpenRoot(t.Context(), request, scope, home.RootReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	skillRoot, ok := root.(home.SkillRootOperations)
	if !ok {
		t.Fatal("root lacks Skill publication operations")
	}
	return skillRoot
}

func deterministicEntropy(fill byte) func([]byte) error {
	return func(output []byte) error {
		for index := range output {
			output[index] = fill
		}
		return nil
	}
}

func TestPublishRevisionCreatesImmutableCurrentAndPreservesHistory(t *testing.T) {
	skill := testManagedSkill()
	skill.UserID = uuid.NewString()
	root := testSkillRoot(t, skill)
	first, err := publishRevision(t.Context(), root, skill, testRevisionFiles(), "", true, deterministicEntropy(1))
	if err != nil {
		t.Fatal(err)
	}
	updated := first.Skill
	updated.Description = "updated"
	updated.Version++
	updated.UpdatedAt = updated.UpdatedAt.Add(1)
	files := testRevisionFiles()
	files[0].Content = []byte("new current")
	second, err := publishRevision(t.Context(), root, updated, files, first.Skill.ContentDigest, false, deterministicEntropy(2))
	if err != nil {
		t.Fatal(err)
	}
	if second.Skill.ContentDigest == first.Skill.ContentDigest {
		t.Fatal("content digest did not change")
	}
	if current, err := readCurrentSnapshot(t.Context(), root, skill); err != nil || current.Skill.ContentDigest != second.Skill.ContentDigest || string(current.Files[0].Content) != "new current" {
		t.Fatalf("current = %#v, %v", current, err)
	}
	if historical, err := readRevisionSnapshot(t.Context(), root, skill, first.Skill.ContentDigest); err != nil || historical.Skill.ContentDigest != first.Skill.ContentDigest {
		t.Fatalf("historical = %#v, %v", historical, err)
	}
	if _, err := publishRevision(t.Context(), root, updated, files, first.Skill.ContentDigest, false, deterministicEntropy(3)); !errors.Is(err, ErrSkillDigestConflict) {
		t.Fatalf("stale CAS error = %v", err)
	}
}

func TestPublishRevisionRoundTripsBoundedWideDirectoryTree(t *testing.T) {
	skill := testManagedSkill()
	skill.UserID = uuid.NewString()
	root := testSkillRoot(t, skill)
	files := []revisionFile{{Path: MainFile, Mode: 0o644, Content: []byte("main")}}
	for index := range 260 {
		files = append(files, revisionFile{
			Path:    fmt.Sprintf("references-%03d/item", index),
			Mode:    0o644,
			Content: []byte("bounded"),
		})
	}
	snapshot, err := publishRevision(t.Context(), root, skill, files, "", true, deterministicEntropy(7))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Files) != len(files) {
		t.Fatalf("round-tripped files = %d, want %d", len(snapshot.Files), len(files))
	}
}

type syncFailRoot struct {
	home.SkillRootOperations
	directory string
	fail      bool
}

func (root *syncFailRoot) SyncDirectory(ctx context.Context, directory string) error {
	if root.fail && directory == root.directory {
		return home.ErrOutcomeUnknown
	}
	return root.SkillRootOperations.SyncDirectory(ctx, directory)
}

func TestPublishRevisionDoesNotSelectBeforeRevisionAncestryFence(t *testing.T) {
	for _, directory := range []string{managedRevisionRoot, "."} {
		t.Run(directory, func(t *testing.T) {
			skill := testManagedSkill()
			skill.UserID = uuid.NewString()
			underlying := testSkillRoot(t, skill)
			root := &syncFailRoot{SkillRootOperations: underlying, directory: directory, fail: true}
			if _, err := publishRevision(t.Context(), root, skill, testRevisionFiles(), "", true, deterministicEntropy(4)); !home.IsOutcomeUnknown(err) {
				t.Fatalf("publication error = %v", err)
			}
			if _, err := underlying.Lstat(t.Context(), skill.ID); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("selector crossed failed fence: %v", err)
			}
			root.fail = false
			if _, err := publishRevision(t.Context(), root, skill, testRevisionFiles(), "", true, deterministicEntropy(5)); err != nil {
				t.Fatalf("resume publication: %v", err)
			}
		})
	}
}

func TestReadRevisionRejectsSymlinkAndCorruption(t *testing.T) {
	skill := testManagedSkill()
	skill.UserID = uuid.NewString()
	root := testSkillRoot(t, skill)
	snapshot, err := publishRevision(t.Context(), root, skill, testRevisionFiles(), "", true, deterministicEntropy(6))
	if err != nil {
		t.Fatal(err)
	}
	revision, _ := selectedRevisionPath(skill, snapshot.Skill.ContentDigest)
	if err := root.Remove(t.Context(), filepath.ToSlash(filepath.Join(revision, "scripts", "check")), home.RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink(t.Context(), "../../../../SKILL.md", filepath.ToSlash(filepath.Join(revision, "scripts", "check"))); err == nil {
		t.Fatal("escaping symlink unexpectedly accepted")
	}
	if err := root.Symlink(t.Context(), "other", filepath.ToSlash(filepath.Join(revision, "scripts", "check"))); err != nil {
		t.Fatal(err)
	}
	if _, err := readRevisionSnapshot(t.Context(), root, skill, snapshot.Skill.ContentDigest); !errors.Is(err, ErrInvalidSkillRevision) {
		t.Fatalf("symlink revision error = %v", err)
	}
	if err := root.Remove(t.Context(), filepath.ToSlash(filepath.Join(revision, "scripts", "check")), home.RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := root.Write(t.Context(), filepath.ToSlash(filepath.Join(revision, "scripts", "check")), strings.NewReader("tampered"), home.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := readRevisionSnapshot(t.Context(), root, skill, snapshot.Skill.ContentDigest); !errors.Is(err, ErrInvalidSkillRevision) {
		t.Fatalf("digest corruption error = %v", err)
	}
}

func TestSelectedRevisionCannotEscapeRoot(t *testing.T) {
	skill := testManagedSkill()
	for _, target := range []string{"/tmp/revision", "../revision", managedRevisionRoot + "/other/" + strings.Repeat("a", 64), managedRevisionRoot + "/" + skill.ID + "/../" + strings.Repeat("a", 64)} {
		if _, err := parseSelectedRevision(skill, target); !errors.Is(err, ErrInvalidSkillRevision) {
			t.Fatalf("target %q error = %v", target, err)
		}
	}
}
