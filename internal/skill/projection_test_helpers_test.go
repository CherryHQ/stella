package skill

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// projectionReader is the small in-memory runtime reader shared by the tool
// and turn-view tests. It intentionally models the old identity/revision read
// boundary without depending on a concrete store implementation.
type projectionReader struct {
	identities []Skill
	revisions  map[string]ManagedRevision
	loads      int
}

func (*projectionReader) TouchReflectSkillRuntimeUseDigest(context.Context, string, string, string, string) error {
	return nil
}

func (r *projectionReader) GetIdentity(_ context.Context, id string) (*Skill, error) {
	for i := range r.identities {
		if r.identities[i].ID == id {
			return &r.identities[i], nil
		}
	}
	return nil, nil
}

func (r *projectionReader) ListIdentityVisible(context.Context, ViewContext) ([]Skill, error) {
	return append([]Skill(nil), r.identities...), nil
}

func (r *projectionReader) ListIdentityByScope(context.Context, string, string, string) ([]Skill, error) {
	return nil, nil
}

func (r *projectionReader) ListIdentityCandidate(context.Context, string, ViewContext) ([]Skill, error) {
	return nil, nil
}

func (r *projectionReader) LoadCurrentRevision(_ context.Context, identity Skill) (ManagedRevision, error) {
	r.loads++
	revision, ok := r.revisions[identity.ID]
	if !ok {
		return ManagedRevision{}, os.ErrNotExist
	}
	return revision, nil
}

func (r *projectionReader) LoadExactRevision(_ context.Context, identity Skill, digest string) (ManagedRevision, error) {
	revision, err := r.LoadCurrentRevision(context.Background(), identity)
	if err != nil || revision.Skill.ContentDigest != digest {
		return ManagedRevision{}, errors.Join(err, ErrSkillDigestConflict)
	}
	return revision, nil
}

type projectionSession struct {
	tempVisible string
	tempHost    string
}

func (s projectionSession) Policy() pkgsandbox.Policy {
	return pkgsandbox.Policy{Env: map[string]string{pkgsandbox.EnvTempDir: s.tempVisible}}
}

func (s projectionSession) Files() pkgsandbox.FileAccess { return projectionAccess{session: s} }

type projectionAccess struct{ session projectionSession }

func (a projectionAccess) hostPath(name string) (string, error) {
	s := a.session
	rel, err := filepath.Rel(s.tempVisible, name)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	return filepath.Join(s.tempHost, rel), nil
}

func (a projectionAccess) ReadFile(string) ([]byte, error) { return nil, os.ErrPermission }
func (a projectionAccess) ReadDir(string) ([]pkgsandbox.DirEntry, error) {
	return nil, os.ErrPermission
}

func (a projectionAccess) Stat(string) (pkgsandbox.FileInfo, error) {
	return pkgsandbox.FileInfo{}, os.ErrPermission
}
func (a projectionAccess) WriteFile(string, []byte, fs.FileMode) error { return os.ErrPermission }
func (a projectionAccess) ProjectFiles(name string, files []pkgsandbox.ProjectedFile) error {
	target, err := a.hostPath(name)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() {
			return pkgsandbox.ErrProjectionConflict
		}
		for _, file := range files {
			content, readErr := os.ReadFile(filepath.Join(target, filepath.FromSlash(file.Path)))
			fileInfo, statErr := os.Lstat(filepath.Join(target, filepath.FromSlash(file.Path)))
			if readErr != nil || statErr != nil || !bytes.Equal(content, file.Content) || fileInfo.Mode().Perm() != file.Mode.Perm() {
				return pkgsandbox.ErrProjectionConflict
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(target), ".project-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage) //nolint:errcheck
	for _, file := range files {
		filePath := filepath.Join(stage, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filePath, file.Content, file.Mode); err != nil {
			return err
		}
	}
	return os.Rename(stage, target)
}

func (a projectionAccess) ProjectTempFiles(name string, files []pkgsandbox.ProjectedFile) (string, error) {
	visible := path.Join(a.session.tempVisible, name)
	if err := a.ProjectFiles(visible, files); err != nil {
		return "", err
	}
	return visible, nil
}

type selectedSkillReads struct{ denied map[string]bool }

func (a selectedSkillReads) BeginRead(context.Context) (SkillReadDecision, error) { return a, nil }

func (a selectedSkillReads) AllowRead(_ context.Context, id, _, _, _ string) (bool, error) {
	return !a.denied[id], nil
}

func newProjectionTool(t *testing.T, reader RuntimeReader, session sandboxSession, authorizer SkillReadAuthorizer) *Tool {
	t.Helper()
	tool, err := NewTool(reader, session, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}
