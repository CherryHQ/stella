package skill

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

type managementRegressionStore struct {
	ManagementStore
	identities []Skill
	current    ManagedRevision
	loaded     int
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
	store := &managementRegressionStore{current: ManagedRevision{Skill: current}}
	m := NewManagement(store, managementRegressionAccess{})

	_, err := m.Update(context.Background(), authz.Authority{}, ManagedUpdate{
		ID: "skill-1", ExpectedVersion: "digest", Version: &version,
		Patch: UpdatePatch{Description: &empty}, Files: map[string]string{MainFile: "updated"},
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
}
