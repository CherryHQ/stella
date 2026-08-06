package skills

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// HomeStore is the Home-authoritative current-state surface. Its two
// dependencies deliberately own all filesystem access and mutation mechanics.
// It is not the legacy Store interface: callers without digest CAS remain on
// their existing port until the production cutover.
type HomeStore struct {
	catalog *HomeCatalog
	manager *HomeSkillManager
}

func NewHomeStore(catalog *HomeCatalog, manager *HomeSkillManager) (*HomeStore, error) {
	if catalog == nil || manager == nil {
		return nil, errors.New("skills: Home store requires catalog and manager")
	}
	if manager.catalog != catalog {
		return nil, errors.New("skills: Home store catalog and manager catalog must match")
	}
	return &HomeStore{catalog: catalog, manager: manager}, nil
}

func (s *HomeStore) Get(ctx context.Context, id string) (*Skill, error) {
	item, err := s.catalog.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return skillPointer(item.Skill), nil
}

// List preserves the approved Home cutover rule: an active disabled entry
// shadows lower-precedence entries, then is itself omitted from invocation.
func (s *HomeStore) List(ctx context.Context, vc ViewContext) ([]Skill, error) {
	items, err := s.catalog.List(ctx, vc)
	if err != nil {
		return nil, err
	}
	rows := catalogSkills(items)
	sortSkillsByPrecedence(rows)
	return rows, nil
}

func (s *HomeStore) ListAll(ctx context.Context) ([]Skill, error) {
	items, err := s.catalog.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	rows := catalogSkills(items)
	sortSkillsByScopeCreatedID(rows)
	return rows, nil
}

func (s *HomeStore) Resolve(ctx context.Context, name string, vc ViewContext) (*Skill, error) {
	item, err := s.catalog.Resolve(ctx, name, vc)
	if err != nil || item == nil {
		return nil, err
	}
	return skillPointer(item.Skill), nil
}

func (s *HomeStore) ListByScope(ctx context.Context, scope, userID, agentID string) ([]Skill, error) {
	items, err := s.catalog.ListByScope(ctx, scope, userID, agentID)
	if err != nil {
		return nil, err
	}
	rows := catalogSkills(items)
	sortSkillsByScopeCreatedID(rows)
	return rows, nil
}

// ListForAgentContext returns every active applicable row. Unlike List, it
// intentionally retains disabled rows and duplicate names for downstream
// management and merge logic.
func (s *HomeStore) ListForAgentContext(ctx context.Context, userID, agentID string) ([]Skill, error) {
	roots := []HomeCatalogRoot{
		{Scope: "user_agent", UserID: userID, AgentID: agentID},
		{Scope: "user", UserID: userID},
		{Scope: "system_agent", AgentID: agentID},
		{Scope: "system"},
	}
	rows, err := s.listActiveRoots(ctx, roots)
	if err != nil {
		return nil, err
	}
	sortSkillsByPrecedence(rows)
	return rows, nil
}

func (s *HomeStore) ListForAdmin(ctx context.Context, userID string) ([]Skill, error) {
	items, err := s.catalog.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]Skill, 0, len(items))
	for _, item := range items {
		sk := item.Skill
		if sk.Scope == "system" || sk.Scope == "system_agent" ||
			(userID != "" && (sk.Scope == "user" || sk.Scope == "user_agent") && sk.UserID == userID) {
			rows = append(rows, sk)
		}
	}
	sortSkillsByScopeCreatedID(rows)
	return rows, nil
}

func (s *HomeStore) ListForUser(ctx context.Context, userID string, agentIDs []string) ([]Skill, error) {
	roots := []HomeCatalogRoot{{Scope: "system"}}
	for _, agentID := range uniqueSorted(agentIDs) {
		roots = append(roots, HomeCatalogRoot{Scope: "system_agent", AgentID: agentID})
	}
	if userID != "" {
		roots = append(roots, HomeCatalogRoot{Scope: "user", UserID: userID})
		for _, agentID := range uniqueSorted(agentIDs) {
			roots = append(roots, HomeCatalogRoot{Scope: "user_agent", UserID: userID, AgentID: agentID})
		}
	}
	rows, err := s.listActiveRoots(ctx, roots)
	if err != nil {
		return nil, err
	}
	sortSkillsByScopeCreatedID(rows)
	return rows, nil
}

func (s *HomeStore) ListActiveReflectOwnedUserAgentSkills(ctx context.Context, userID, agentID string) ([]Skill, error) {
	items, err := s.catalog.ListByScope(ctx, "user_agent", userID, agentID)
	if err != nil {
		return nil, err
	}
	rows := make([]Skill, 0, len(items))
	for _, item := range items {
		sk := item.Skill
		// Reflect can mutate only managed revisions. An ordinary directory may
		// carry copied metadata, but it has no immutable catalog digest and must
		// never enter a plan that could fall back to lifecycle versions.
		if sk.Status == SkillStatusActive && CreatedBy(sk) == ReflectSkillCreatedBy && validHomeSkillDigest(sk.ContentDigest) {
			rows = append(rows, sk)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) {
			return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
		}
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.After(rows[j].CreatedAt)
		}
		return rows[i].ID < rows[j].ID
	})
	return rows, nil
}

func (s *HomeStore) LoadFile(ctx context.Context, id, filename string) (string, error) {
	return s.catalog.LoadFile(ctx, id, filename)
}

func (s *HomeStore) ListFiles(ctx context.Context, id string) ([]string, error) {
	return s.catalog.ListFiles(ctx, id)
}

func (s *HomeStore) ListFilesWithContent(ctx context.Context, id string) (map[string]string, error) {
	return s.catalog.ListFilesWithContent(ctx, id)
}

func (s *HomeStore) CreateManagedSkill(ctx context.Context, sk Skill, files map[string]string) (SkillSnapshot, error) {
	if sk.Status != "" && sk.Status != SkillStatusActive {
		return SkillSnapshot{}, errors.New("skills: Home-managed Skills must be active")
	}
	metadata, err := MarkManualOwnedMetadata(sk.Metadata)
	if err != nil {
		return SkillSnapshot{}, err
	}
	metadataMap, err := homeStoreMetadataMap(metadata)
	if err != nil {
		return SkillSnapshot{}, err
	}
	return s.manager.Create(ctx, HomeSkillCreateRequest{
		Scope:                  sk.Scope,
		UserID:                 sk.UserID,
		AgentID:                sk.AgentID,
		Name:                   sk.Name,
		Description:            sk.Description,
		DisableModelInvocation: sk.DisableModelInvocation,
		Metadata:               metadataMap,
		Files:                  homeStoreFileInputs(files),
	})
}

func (s *HomeStore) UpdateManagedSkill(ctx context.Context, in ManagedSkillUpdate) (SkillSnapshot, error) {
	request, err := homeStoreUpdateRequest(in)
	if err != nil {
		return SkillSnapshot{}, err
	}
	return s.manager.Update(ctx, request)
}

func (s *HomeStore) DeleteManagedSkill(ctx context.Context, in ManagedSkillDelete) error {
	if err := validateHomeManagedDelete(in); err != nil {
		return err
	}
	return s.manager.Delete(ctx, in.ID, in.ExpectedDigest)
}

func (s *HomeStore) DeleteManagedSkillFile(ctx context.Context, in ManagedSkillFileDelete) (SkillSnapshot, error) {
	if err := validateHomeManagedDelete(in.ManagedSkillDelete); err != nil {
		return SkillSnapshot{}, err
	}
	if err := validateHomeMutationPath(in.Path); err != nil {
		return SkillSnapshot{}, err
	}
	if in.Path == MainFile {
		return SkillSnapshot{}, errors.New("skills: SKILL.md cannot be deleted")
	}
	return s.manager.DeleteFile(ctx, in.ID, in.ExpectedDigest, in.Path)
}

func (s *HomeStore) listActiveRoots(ctx context.Context, roots []HomeCatalogRoot) ([]Skill, error) {
	rows := make([]Skill, 0)
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if !applicableHomeCatalogRoot(root) {
			continue
		}
		key, err := encodeFilesystemSkillID(root.Scope, root.UserID, root.AgentID, "root")
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		items, err := s.catalog.ListByScope(ctx, root.Scope, root.UserID, root.AgentID)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.Skill.Status == SkillStatusActive {
				rows = append(rows, item.Skill)
			}
		}
	}
	return rows, nil
}

func homeStoreUpdateRequest(in ManagedSkillUpdate) (HomeSkillUpdateRequest, error) {
	if err := validateManagedSkillFileChanges(in.Files, in.DeleteFiles); err != nil {
		return HomeSkillUpdateRequest{}, err
	}
	scope, userID, agentID, _, err := decodeFilesystemSkillID(in.ID)
	if err != nil {
		return HomeSkillUpdateRequest{}, err
	}
	if !isMutableSkillScope(scope) || in.Scope != scope || in.UserID != userID || in.AgentID != agentID {
		return HomeSkillUpdateRequest{}, ErrSkillNotMutable
	}
	if in.Patch.Status != nil && *in.Patch.Status != SkillStatusActive {
		return HomeSkillUpdateRequest{}, errors.New("skills: Home-managed Skills cannot change status")
	}
	var metadata *map[string]any
	if len(in.Patch.Metadata) != 0 {
		value, err := homeStoreMetadataMap(in.Patch.Metadata)
		if err != nil {
			return HomeSkillUpdateRequest{}, err
		}
		metadata = &value
	}
	return HomeSkillUpdateRequest{
		ID:                     in.ID,
		ExpectedDigest:         in.ExpectedDigest,
		Description:            in.Patch.Description,
		DisableModelInvocation: in.Patch.DisableModelInvocation,
		Metadata:               metadata,
		FileUpserts:            homeStoreFileInputs(in.Files),
		DeleteFiles:            append([]string(nil), in.DeleteFiles...),
		ConvertToManual:        in.ConvertToManual,
	}, nil
}

func validateHomeManagedDelete(in ManagedSkillDelete) error {
	scope, userID, agentID, _, err := decodeFilesystemSkillID(in.ID)
	if err != nil {
		return err
	}
	if !isMutableSkillScope(scope) || in.Scope != scope || in.UserID != userID || in.AgentID != agentID {
		return ErrSkillNotMutable
	}
	return validateHomeSkillDelete(in.ID, in.ExpectedDigest)
}

func homeStoreMetadataMap(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	value, err := decodeStrictJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("skills: decode Home Skill metadata: %w", err)
	}
	metadata, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("skills: Home Skill metadata must be an object")
	}
	return metadata, nil
}

func homeStoreFileInputs(files map[string]string) []HomeSkillFileInput {
	paths := make([]string, 0, len(files))
	for filename := range files {
		paths = append(paths, filename)
	}
	sort.Strings(paths)
	inputs := make([]HomeSkillFileInput, 0, len(paths))
	for _, filename := range paths {
		inputs = append(inputs, HomeSkillFileInput{Path: filename, Content: []byte(files[filename])})
	}
	return inputs
}

func catalogSkills(items []HomeCatalogSkill) []Skill {
	rows := make([]Skill, len(items))
	for i, item := range items {
		rows[i] = item.Skill
	}
	return rows
}

func skillPointer(sk Skill) *Skill { return &sk }

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sortSkillsByPrecedence(rows []Skill) {
	sort.Slice(rows, func(i, j int) bool {
		left, right := homeStoreScopePrecedence(rows[i].Scope), homeStoreScopePrecedence(rows[j].Scope)
		if left != right {
			return left < right
		}
		return skillCreatedIDLess(rows[i], rows[j])
	})
}

func sortSkillsByScopeCreatedID(rows []Skill) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Scope != rows[j].Scope {
			return rows[i].Scope < rows[j].Scope
		}
		return skillCreatedIDLess(rows[i], rows[j])
	})
}

func homeStoreScopePrecedence(scope string) int {
	switch scope {
	case "user_agent":
		return 1
	case "user":
		return 2
	case "system_agent":
		return 3
	case "system":
		return 4
	default:
		return 5
	}
}

func skillCreatedIDLess(left, right Skill) bool {
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	return left.ID < right.ID
}
