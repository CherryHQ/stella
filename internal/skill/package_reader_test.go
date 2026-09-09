package skill

import (
	"context"
	"errors"
	"io/fs"
	"testing"
)

func TestStorePackageSkillReaderLoadsCopiedFiles(t *testing.T) {
	reader, err := NewStorePackageSkillReader(func(_ context.Context, digest, name string) (map[string][]byte, map[string]fs.FileMode, error) {
		if digest != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || name != "docs" {
			t.Fatalf("loader args = %q/%q", digest, name)
		}
		return map[string][]byte{"SKILL.md": []byte("hello")}, map[string]fs.FileMode{"SKILL.md": 0o644}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := reader.LoadPackageSkill(t.Context(), PackageSkillRef{PackageID: "pkg", PackageDigest: "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if string(revision.Files["SKILL.md"]) != "hello" {
		t.Fatalf("files = %#v", revision.Files)
	}
}

func TestStorePackageSkillReaderRejectsInvalidReference(t *testing.T) {
	reader, err := NewStorePackageSkillReader(func(context.Context, string, string) (map[string][]byte, map[string]fs.FileMode, error) {
		return nil, nil, errors.New("must not be called")
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.LoadPackageSkill(t.Context(), PackageSkillRef{PackageID: "pkg", PackageDigest: "bad", Name: "docs"}); !errors.Is(err, ErrInvalidSkillRevision) {
		t.Fatalf("error = %v, want invalid revision", err)
	}
}
