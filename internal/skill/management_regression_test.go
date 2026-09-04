package skill

import (
	"context"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type managementRegressionStore struct {
	ManagementStore
	identities []Skill
	current    ManagedRevision
	loaded     int
	created    ManagedCreate
	updated    ManagedSkillUpdate
}

func (s *managementRegressionStore) ListIdentityByScope(context.Context, string, string, string) ([]Skill, error) {
	return s.identities, nil
}

func (s *managementRegressionStore) GetIdentity(context.Context, string) (*Skill, error) {
	sk := s.current.Skill
	return &sk, nil
}

func (s *managementRegressionStore) LoadCurrentRevision(context.Context, Skill) (ManagedRevision, error) {
	s.loaded++
	return s.current, nil
}

func (s *managementRegressionStore) UpdateManagedSkill(_ context.Context, in ManagedSkillUpdate) (SkillSnapshot, error) {
	s.updated = in
	return SkillSnapshot{Skill: s.current.Skill}, nil
}

func (s *managementRegressionStore) CreateManagedSkill(_ context.Context, sk Skill, files map[string]string) (SkillSnapshot, error) {
	s.created = ManagedCreate{
		Scope: sk.Scope, Name: sk.Name, Description: sk.Description,
		DisableModelInvocation: sk.DisableModelInvocation, Files: files,
	}
	return SkillSnapshot{Skill: sk, Files: []string{MainFile, "references/o2.md"}}, nil
}

type managementRegressionAccess struct{ ManagementAccess }

func (managementRegressionAccess) ManageScope(context.Context, authz.Authority, string, string) (string, string, error) {
	return "user-1", "", nil
}

func (managementRegressionAccess) ManageByID(context.Context, authz.Authority, string, authz.Action) (Skill, error) {
	return Skill{}, nil
}

func TestManagementListReadsOnlyBoundedIdentityMetadata(t *testing.T) {
	identities := make([]Skill, maxManagementSkillListResults+1)
	for i := range identities {
		identities[i] = Skill{ID: string(rune('a' + i)), Scope: "user", ContentDigest: "digest"}
	}
	store := &managementRegressionStore{identities: identities}
	h := skillManagementHandler{management: NewManagement(store, managementRegressionAccess{})}

	out, err := h.List(context.Background(), SettingsSkillListInput{})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if got := len(result["skills"].([]managementSkillView)); got != maxManagementSkillListResults {
		t.Fatalf("listed %d Skills, want capped %d", got, maxManagementSkillListResults)
	}
	if truncated, _ := result["truncated"].(bool); !truncated {
		t.Fatal("capped list must report truncation")
	}
	if store.loaded != 0 {
		t.Fatalf("list opened %d Home revisions, want metadata-only reads", store.loaded)
	}
}

func TestManagementUpdatePreservesVersionAndEmptyDescription(t *testing.T) {
	empty, version := "", "2.0.0"
	current := Skill{ID: "skill-1", Scope: "user", ContentDigest: "digest", Metadata: json.RawMessage(`{"source":"https://example.test/skill","version":"1.0.0"}`)}
	store := &managementRegressionStore{current: ManagedRevision{
		Skill: current,
		Files: map[string][]byte{MainFile: []byte("old"), "references/stale.md": []byte("stale")},
	}}
	m := NewManagement(store, managementRegressionAccess{})

	_, err := m.Update(context.Background(), authz.Authority{}, ManagedUpdate{
		ID: "skill-1", ExpectedVersion: "digest", Name: "", Version: &version,
		Patch: UpdatePatch{Description: &empty}, Files: map[string]string{MainFile: "updated"}, ReplaceFiles: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.updated.Patch.Description == nil || *store.updated.Patch.Description != "" {
		t.Fatalf("description patch = %#v, want explicit empty string", store.updated.Patch.Description)
	}
	var metadata map[string]any
	if err := json.Unmarshal(store.updated.Patch.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["version"] != version || metadata["source"] != "https://example.test/skill" {
		t.Fatalf("version patch replaced metadata: %#v", metadata)
	}
	if len(store.updated.DeleteFiles) != 1 || store.updated.DeleteFiles[0] != "references/stale.md" {
		t.Fatalf("deleted files = %#v, want stale package resource", store.updated.DeleteFiles)
	}
}

func TestManagementUpdateRejectsPackageNameChange(t *testing.T) {
	current := Skill{ID: "skill-1", Scope: "user", Name: "original", ContentDigest: "digest"}
	store := &managementRegressionStore{current: ManagedRevision{Skill: current, Files: map[string][]byte{MainFile: []byte("old")}}}
	m := NewManagement(store, managementRegressionAccess{})

	_, err := m.Update(t.Context(), authz.Authority{}, ManagedUpdate{
		ID: "skill-1", ExpectedVersion: "digest", Name: "renamed", Files: map[string]string{MainFile: "updated"}, ReplaceFiles: true,
	})
	if err == nil || !strings.Contains(err.Error(), `does not match managed Skill name "original"`) {
		t.Fatalf("name mismatch error = %v", err)
	}
	if store.updated.ID != "" {
		t.Fatalf("name mismatch reached store update: %#v", store.updated)
	}
}

type managementTestFiles struct {
	directories map[string][]pkgsandbox.DirEntry
	files       map[string][]byte
}

func (f managementTestFiles) ReadFile(name string) ([]byte, error) {
	content, ok := f.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), content...), nil
}

func (f managementTestFiles) ReadDir(name string) ([]pkgsandbox.DirEntry, error) {
	entries, ok := f.directories[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]pkgsandbox.DirEntry(nil), entries...), nil
}

func (f managementTestFiles) Stat(name string) (pkgsandbox.FileInfo, error) {
	if _, ok := f.directories[name]; ok {
		return pkgsandbox.FileInfo{IsDir: true}, nil
	}
	content, ok := f.files[name]
	if !ok {
		return pkgsandbox.FileInfo{}, fs.ErrNotExist
	}
	return pkgsandbox.FileInfo{Size: int64(len(content))}, nil
}

func (managementTestFiles) WriteFile(string, []byte, fs.FileMode) error { return fs.ErrPermission }
func (managementTestFiles) ProjectFiles(string, []pkgsandbox.ProjectedFile) error {
	return fs.ErrPermission
}

func (managementTestFiles) ProjectTempFiles(string, []pkgsandbox.ProjectedFile) (string, error) {
	return "", fs.ErrPermission
}

type managementTestSession struct {
	files pkgsandbox.FileAccess
}

func (managementTestSession) Policy() pkgsandbox.Policy { return pkgsandbox.Policy{} }
func (managementTestSession) Close() error              { return nil }
func (managementTestSession) Alive() bool               { return true }
func (managementTestSession) Done() <-chan struct{}     { return nil }
func (managementTestSession) Exec(context.Context, string, pkgsandbox.ExecOptions) (pkgsandbox.ExecResult, error) {
	return pkgsandbox.ExecResult{}, nil
}

func (managementTestSession) StartProcess(context.Context, pkgsandbox.ProcessRequest) (pkgsandbox.ProcessHandle, error) {
	return nil, fs.ErrPermission
}
func (s managementTestSession) Files() pkgsandbox.FileAccess { return s.files }
func (managementTestSession) WorkingDir() string             { return "/work" }

func TestReadSkillPackageReadsTheStandardDirectory(t *testing.T) {
	main := []byte("---\nname: stella-doctor\ndescription: Diagnose Stella deployments.\ndisable-model-invocation: true\n---\n# Doctor\n")
	fileAccess := managementTestFiles{
		directories: map[string][]pkgsandbox.DirEntry{
			"/work/stella-doctor": {
				{Name: "assets", IsDir: true},
				{Name: MainFile, Size: int64(len(main))},
				{Name: "references", IsDir: true},
			},
			"/work/stella-doctor/assets":     {{Name: "icon.png", Size: 3}},
			"/work/stella-doctor/references": {{Name: "o2.md", Size: 12}},
		},
		files: map[string][]byte{
			"/work/stella-doctor/SKILL.md":         main,
			"/work/stella-doctor/assets/icon.png":  {0xff, 0x00, 0x7f},
			"/work/stella-doctor/references/o2.md": []byte("# O2 details"),
		},
	}
	h := skillManagementHandler{runtime: managementTestSession{files: fileAccess}}

	pkg, err := h.readSkillPackage(t.Context(), "stella-doctor")
	if err != nil {
		t.Fatal(err)
	}
	if pkg.frontmatter.Name != "stella-doctor" || pkg.frontmatter.Description != "Diagnose Stella deployments." || !pkg.frontmatter.DisableModelInvocation {
		t.Fatalf("frontmatter = %#v", pkg.frontmatter)
	}
	if len(pkg.files) != 3 || pkg.files["references/o2.md"] != "# O2 details" || []byte(pkg.files["assets/icon.png"])[0] != 0xff {
		t.Fatalf("package files = %#v", pkg.files)
	}
}

func TestManagementCreateUsesPackageFrontmatterAndResources(t *testing.T) {
	main := []byte("---\nname: stella-doctor\ndescription: Diagnose Stella deployments.\ndisable-model-invocation: true\n---\n# Doctor\n")
	fileAccess := managementTestFiles{
		directories: map[string][]pkgsandbox.DirEntry{
			"/work/stella-doctor":            {{Name: MainFile, Size: int64(len(main))}, {Name: "references", IsDir: true}},
			"/work/stella-doctor/references": {{Name: "o2.md", Size: 12}},
		},
		files: map[string][]byte{
			"/work/stella-doctor/SKILL.md":         main,
			"/work/stella-doctor/references/o2.md": []byte("# O2 details"),
		},
	}
	store := &managementRegressionStore{}
	h := skillManagementHandler{
		management: NewManagement(store, managementRegressionAccess{}),
		runtime:    managementTestSession{files: fileAccess},
	}

	if _, err := h.Create(t.Context(), SettingsSkillCreateInput{ContentPath: "stella-doctor", Scope: "system"}); err != nil {
		t.Fatal(err)
	}
	if store.created.Scope != "system" || store.created.Name != "stella-doctor" || store.created.Description != "Diagnose Stella deployments." || !store.created.DisableModelInvocation {
		t.Fatalf("managed create = %#v", store.created)
	}
	if len(store.created.Files) != 2 || store.created.Files["references/o2.md"] != "# O2 details" {
		t.Fatalf("created package files = %#v", store.created.Files)
	}
}

func TestReadSkillPackageRequiresDirectoryAndMatchingName(t *testing.T) {
	main := []byte("---\nname: different\ndescription: mismatch\n---\n")
	fileAccess := managementTestFiles{
		directories: map[string][]pkgsandbox.DirEntry{
			"/work/stella-doctor": {{Name: MainFile, Size: int64(len(main))}},
		},
		files: map[string][]byte{
			"/work/lone.md":                []byte("single file"),
			"/work/stella-doctor/SKILL.md": main,
		},
	}
	h := skillManagementHandler{runtime: managementTestSession{files: fileAccess}}

	if _, err := h.readSkillPackage(t.Context(), "lone.md"); err == nil || !strings.Contains(err.Error(), "must be an Agent Skills directory") {
		t.Fatalf("single-file error = %v", err)
	}
	if _, err := h.readSkillPackage(t.Context(), "stella-doctor"); err == nil || !strings.Contains(err.Error(), "does not match parent directory") {
		t.Fatalf("name mismatch error = %v", err)
	}
}

func TestManagementToolViewsMarshalUsefulMetadata(t *testing.T) {
	now := time.Date(2026, time.September, 4, 8, 0, 0, 0, time.UTC)
	view := managementSkillViewOf(ManagedRevision{
		Skill: Skill{ID: "skill-1", Scope: "user", Name: "doctor", Description: "diagnose", ContentDigest: "digest", CreatedAt: now, UpdatedAt: now},
		Files: map[string][]byte{MainFile: []byte("body")},
	})
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "{}" || !strings.Contains(string(encoded), `"id":"skill-1"`) || !strings.Contains(string(encoded), `"files":["SKILL.md"]`) {
		t.Fatalf("managed Skill view = %s", encoded)
	}
}
