package skill

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"

	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// FileStore is the ordinary-file Skill backend. The mutex serializes Stella's
// own writers; external edits remain intentionally observable at the next
// capture and do not receive a cross-process transaction guarantee.
type FileStore struct {
	db    *pgxpool.Pool
	q     *sqlc.Queries
	roots home.RootOpener
	mu    sync.Mutex
}

type SkillCapture struct {
	Revisions      []ManagedRevision
	MaskedNames    []string
	ForbiddenNames []string
}

func isFileSkillID(id string) bool { return strings.HasPrefix(id, "file:") }

func fileSkillID(scope, userID, agentID, name string) string {
	payload, _ := json.Marshal([4]string{scope, userID, agentID, name})
	return "file:" + base64.RawURLEncoding.EncodeToString(payload)
}

func fileSkillIdentity(id string) (scope, userID, agentID, name string, ok bool) {
	if !isFileSkillID(id) {
		return "", "", "", "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "file:"))
	if err != nil {
		return "", "", "", "", false
	}
	var tuple [4]string
	if err := json.Unmarshal(payload, &tuple); err != nil || tuple[0] == "" || tuple[3] == "" || fileSkillID(tuple[0], tuple[1], tuple[2], tuple[3]) != id {
		return "", "", "", "", false
	}
	return tuple[0], tuple[1], tuple[2], tuple[3], true
}

func NewFileStore(db *pgxpool.Pool, roots home.RootOpener) *FileStore {
	store := &FileStore{db: db, roots: roots}
	if db != nil {
		store.q = sqlc.New(db)
	}
	return store
}

func decodeFileSkillID(id string) (Skill, bool) {
	if !isFileSkillID(id) {
		return Skill{}, false
	}
	scope, userID, agentID, name, ok := fileSkillIdentity(id)
	if !ok || fileSkillID(scope, userID, agentID, name) != id {
		return Skill{}, false
	}
	if !validFileSkillOwner(scope, userID, agentID) || skillNameValidationError(name, name) != nil {
		return Skill{}, false
	}
	return Skill{ID: id, Scope: scope, UserID: userID, AgentID: agentID, Name: name}, true
}

func validFileSkillOwner(scope, userID, agentID string) bool {
	switch scope {
	case "system":
		return userID == "" && agentID == ""
	case "system_agent":
		return userID == "" && agentID != ""
	case "user":
		return userID != "" && agentID == ""
	case "user_agent":
		return userID != "" && agentID != ""
	default:
		return false
	}
}

func resourceRootFor(scope string) (plugin.Scope, home.RootScope, bool) {
	switch scope {
	case "system":
		return plugin.ScopeSystem, home.RootSystemResources, true
	case "system_agent":
		return plugin.ScopeSystemAgent, home.RootSystemAgentResources, true
	case "user":
		return plugin.ScopeUser, home.RootUserResources, true
	case "user_agent":
		return plugin.ScopeUserAgent, home.RootUserAgentResources, true
	default:
		return "", 0, false
	}
}

func (s *FileStore) resourceRoots(vc ViewContext) []plugin.ResourceRoot {
	if s == nil || s.roots == nil {
		return nil
	}
	owners := []Skill{{Scope: "system"}, {Scope: "system_agent", AgentID: vc.AgentID}, {Scope: "user", UserID: vc.UserID}, {Scope: "user_agent", UserID: vc.UserID, AgentID: vc.AgentID}}
	result := make([]plugin.ResourceRoot, 0, len(owners))
	for _, owner := range owners {
		pluginScope, rootScope, ok := resourceRootFor(owner.Scope)
		if !ok {
			continue
		}
		req := home.WorkspaceRequest{UserID: owner.UserID, AgentID: owner.AgentID}
		result = append(result, plugin.ResourceRoot{
			Scope: pluginScope, UserID: owner.UserID, AgentID: owner.AgentID,
			Open: func(ctx context.Context) (home.RootOperations, error) {
				return s.roots.OpenRoot(ctx, req, rootScope, home.RootReadOnly)
			},
		})
	}
	return result
}

// CaptureVisible captures the selected independent Skill winners in one
// filesystem pass. A malformed or disabled winner masks the same name below
// it, so callers never silently revive a broader resource.
func (s *FileStore) CaptureVisible(ctx context.Context, vc ViewContext) (SkillCapture, error) {
	if s == nil || s.roots == nil {
		return SkillCapture{}, ErrManagedSkillsUnavailable
	}
	resources, err := plugin.DiscoverResources(ctx, s.resourceRoots(vc))
	if err != nil {
		return SkillCapture{}, err
	}
	revisions := make([]ManagedRevision, 0, len(resources))
	masked := make([]string, 0)
	forbidden := make([]string, 0)
	for _, resource := range resources {
		if resource.Key.Kind != plugin.ResourceSkill {
			continue
		}
		if resource.Forbidden {
			forbidden = append(forbidden, resource.Key.Name)
		}
		if resource.Disabled || resource.Content == nil {
			masked = append(masked, resource.Key.Name)
			continue
		}
		revision, err := revisionFromContent(resource.Key.Scope, resource.Key.UserID, resource.Key.AgentID, resource.Key.Name, resource.Content)
		if err != nil {
			masked = append(masked, resource.Key.Name)
			continue
		}
		if revision.Skill.Status == SkillStatusDeprecated || revision.Skill.DisableModelInvocation || isDisabledIdentity(revision.Skill, vc.DisabledSkillRefs) {
			masked = append(masked, revision.Skill.Name)
			continue
		}
		revisions = append(revisions, revision)
	}
	slices.Sort(masked)
	masked = slices.Compact(masked)
	slices.Sort(forbidden)
	forbidden = slices.Compact(forbidden)
	return SkillCapture{Revisions: revisions, MaskedNames: masked, ForbiddenNames: forbidden}, nil
}

func revisionFromContent(scope plugin.Scope, userID, agentID, name string, content *plugin.ResourceContent) (ManagedRevision, error) {
	main, err := fs.ReadFile(content.FS(), MainFile)
	if err != nil {
		return ManagedRevision{}, err
	}
	fm, err := parseFrontmatter(string(main))
	if err != nil {
		return ManagedRevision{}, err
	}
	if err := skillNameValidationError(name, name); err != nil || fm.Name != name || strings.TrimSpace(fm.Description) == "" {
		return ManagedRevision{}, fmt.Errorf("invalid Skill %q", name)
	}
	metadata, err := skillMetadataJSON(fm.Metadata)
	if err != nil {
		return ManagedRevision{}, err
	}
	files := make(map[string][]byte)
	modes := make(map[string]fs.FileMode)
	err = fs.WalkDir(content.FS(), ".", func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(content.FS(), file)
		if err != nil {
			return err
		}
		files[file] = bytes.Clone(data)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		modes[file] = info.Mode().Perm()
		return nil
	})
	if err != nil {
		return ManagedRevision{}, err
	}
	status := fm.Status
	if status == "" {
		status = SkillStatusActive
	}
	digest := strings.TrimPrefix(content.Digest, "sha256:")
	if _, err := hex.DecodeString(digest); err != nil || len(digest) != sha256HexLength {
		return ManagedRevision{}, ErrInvalidSkillRevision
	}
	skill := Skill{
		ID: fileSkillID(string(scope), userID, agentID, name), Scope: string(scope), UserID: userID, AgentID: agentID,
		Name: name, Description: fm.Description, Status: status, DisableModelInvocation: fm.DisableModelInvocation,
		Metadata: metadata, Version: 0, ContentDigest: digest,
	}
	return ManagedRevision{Skill: skill, Files: files, Modes: modes}, nil
}

const sha256HexLength = 64

func (s *FileStore) GetIdentity(_ context.Context, id string) (*Skill, error) {
	skill, ok := decodeFileSkillID(id)
	if !ok {
		return nil, nil
	}
	return &skill, nil
}

func (s *FileStore) ListIdentityVisible(ctx context.Context, vc ViewContext) ([]Skill, error) {
	capture, err := s.CaptureVisible(ctx, vc)
	if err != nil {
		return nil, err
	}
	result := make([]Skill, 0, len(capture.Revisions))
	for _, revision := range capture.Revisions {
		result = append(result, revision.Skill)
	}
	return result, nil
}

func (s *FileStore) ListIdentityByScope(ctx context.Context, scope, userID, agentID string) ([]Skill, error) {
	return s.captureScopeSkills(ctx, Skill{Scope: scope, UserID: userID, AgentID: agentID})
}

func (s *FileStore) ListIdentityCandidate(ctx context.Context, name string, vc ViewContext) ([]Skill, error) {
	identities, err := s.ListIdentityVisible(ctx, vc)
	if err != nil {
		return nil, err
	}
	result := make([]Skill, 0, 1)
	for _, identity := range identities {
		if identity.Name == name {
			result = append(result, identity)
		}
	}
	return result, nil
}

func (s *FileStore) LoadCurrentRevision(ctx context.Context, identity Skill) (ManagedRevision, error) {
	if err := validateFileSkill(identity); err != nil {
		return ManagedRevision{}, err
	}
	root, err := s.openResourceRoot(ctx, identity, home.RootReadOnly)
	if err != nil {
		return ManagedRevision{}, err
	}
	defer func() { _ = root.Close() }()
	content, err := plugin.CaptureResource(ctx, root, path.Join("skills", identity.Name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ManagedRevision{}, pgx.ErrNoRows
		}
		return ManagedRevision{}, err
	}
	revision, err := revisionFromContent(plugin.Scope(identity.Scope), identity.UserID, identity.AgentID, identity.Name, content)
	if err != nil {
		return ManagedRevision{}, err
	}
	if revision.Skill.ID != identity.ID {
		return ManagedRevision{}, ErrInvalidSkillRevision
	}
	return revision, nil
}

func (s *FileStore) captureScopeSkills(ctx context.Context, owner Skill) ([]Skill, error) {
	if !validFileSkillOwner(owner.Scope, owner.UserID, owner.AgentID) {
		return nil, ErrInvalidSkillRevision
	}
	root, err := s.openResourceRoot(ctx, owner, home.RootReadOnly)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	entries, err := root.List(ctx, "skills", home.ListOptions{Limit: plugin.ResourceMaxEntries + 1})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) > plugin.ResourceMaxEntries {
		return nil, errors.Join(ErrSkillLimit, plugin.ErrResourceLimit)
	}
	slices.SortFunc(entries, func(left, right fs.DirEntry) int { return strings.Compare(left.Name(), right.Name()) })
	result := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || skillNameValidationError(entry.Name(), entry.Name()) != nil {
			continue
		}
		content, err := plugin.CaptureResource(ctx, root, path.Join("skills", entry.Name()))
		if err != nil {
			return nil, err
		}
		revision, err := revisionFromContent(plugin.Scope(owner.Scope), owner.UserID, owner.AgentID, entry.Name(), content)
		if err != nil {
			return nil, err
		}
		result = append(result, revision.Skill)
	}
	return result, nil
}

func (s *FileStore) LoadExactRevision(ctx context.Context, identity Skill, digest string) (ManagedRevision, error) {
	if !validSkillDigest(digest) {
		return ManagedRevision{}, ErrSkillDigestRequired
	}
	revision, err := s.LoadCurrentRevision(ctx, identity)
	if err != nil {
		return ManagedRevision{}, err
	}
	if revision.Skill.ContentDigest != digest {
		return ManagedRevision{}, ErrSkillDigestConflict
	}
	return revision, nil
}

func validateFileSkill(skill Skill) error {
	if !validFileSkillOwner(skill.Scope, skill.UserID, skill.AgentID) {
		return ErrInvalidSkillRevision
	}
	if skill.ID != "" && skill.ID != fileSkillID(skill.Scope, skill.UserID, skill.AgentID, skill.Name) {
		return ErrInvalidSkillRevision
	}
	return skillNameValidationError(skill.Name, skill.Name)
}

func (s *FileStore) requireMutation() error {
	if s == nil || s.db == nil || s.roots == nil {
		return ErrManagedSkillsUnavailable
	}
	return nil
}

func (s *FileStore) CreateManagedSkill(ctx context.Context, skill Skill, files map[string]string) (SkillSnapshot, error) {
	managed := make(map[string]ManagedSkillFile, len(files))
	for name, content := range files {
		managed[name] = ManagedSkillFile{Content: []byte(content), Mode: 0o644}
	}
	return s.CreateManagedSkillWithFiles(ctx, skill, managed)
}

func (s *FileStore) CreateManagedSkillWithFiles(ctx context.Context, skill Skill, files map[string]ManagedSkillFile) (snapshot SkillSnapshot, resultErr error) {
	if err := s.requireMutation(); err != nil {
		return SkillSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if skill.ID == "" {
		skill.ID = fileSkillID(skill.Scope, skill.UserID, skill.AgentID, skill.Name)
	}
	if skill.ID != fileSkillID(skill.Scope, skill.UserID, skill.AgentID, skill.Name) {
		return SkillSnapshot{}, ErrInvalidSkillRevision
	}
	if err := validateFileSkill(skill); err != nil {
		return SkillSnapshot{}, err
	}
	if skill.Status == "" {
		skill.Status = SkillStatusActive
	}
	if (skill.Status != SkillStatusActive && skill.Status != SkillStatusDeprecated) || strings.TrimSpace(skill.Description) == "" {
		return SkillSnapshot{}, ErrInvalidSkillRevision
	}
	if len(skill.Metadata) == 0 {
		skill.Metadata = json.RawMessage(`{}`)
	}
	skill.Metadata, resultErr = sanitizeMetadata(skill.Metadata)
	if resultErr != nil {
		return SkillSnapshot{}, resultErr
	}
	created, err := s.createFileSkill(ctx, skill, files)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if err := s.recordFileSkillChange(ctx, nil, created.Skill, "create", ManualSkillCreatedBy, created.Skill.Metadata); err != nil {
		return SkillSnapshot{Skill: created.Skill, Files: sortedRevisionPaths(created)}, fmt.Errorf("%w: record Skill evidence: %w", home.ErrOutcomeUnknown, err)
	}
	return SkillSnapshot{Skill: created.Skill, Files: sortedRevisionPaths(created)}, nil
}

func (s *FileStore) UpdateManagedSkill(ctx context.Context, in ManagedSkillUpdate) (snapshot SkillSnapshot, resultErr error) {
	if err := s.requireMutation(); err != nil {
		return SkillSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, ok := decodeFileSkillID(in.ID)
	if !ok || identity.Scope != in.Scope || identity.UserID != in.UserID || identity.AgentID != in.AgentID {
		return SkillSnapshot{}, ErrSkillNotMutable
	}
	if !validSkillDigest(in.ExpectedDigest) {
		return SkillSnapshot{}, ErrSkillDigestRequired
	}
	before, after, err := s.updateFileSkill(ctx, in)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if err := s.recordFileSkillChange(ctx, &before.Skill, after.Skill, "patch", ManualSkillCreatedBy, after.Skill.Metadata); err != nil {
		return SkillSnapshot{Skill: after.Skill, Files: sortedRevisionPaths(after)}, fmt.Errorf("%w: record Skill evidence: %w", home.ErrOutcomeUnknown, err)
	}
	return SkillSnapshot{Skill: after.Skill, Files: sortedRevisionPaths(after)}, nil
}

func (s *FileStore) DeleteManagedSkill(ctx context.Context, in ManagedSkillDelete) (resultErr error) {
	if err := s.requireMutation(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, ok := decodeFileSkillID(in.ID)
	if !ok || identity.Scope != in.Scope || identity.UserID != in.UserID || identity.AgentID != in.AgentID {
		return ErrSkillNotMutable
	}
	if !validSkillDigest(in.ExpectedDigest) {
		return ErrSkillDigestRequired
	}
	before, err := s.deleteFileSkill(ctx, in)
	if err != nil {
		return err
	}
	if err := s.recordFileSkillChange(ctx, &before.Skill, before.Skill, "delete", ManualSkillCreatedBy, before.Skill.Metadata); err != nil {
		return fmt.Errorf("%w: record Skill evidence: %w", home.ErrOutcomeUnknown, err)
	}
	return nil
}

func (s *FileStore) createFileSkill(ctx context.Context, skill Skill, files map[string]ManagedSkillFile) (ManagedRevision, error) {
	if err := validateFileSkill(skill); err != nil {
		return ManagedRevision{}, err
	}
	prepared, err := prepareSkillFiles(skill, files, nil)
	if err != nil {
		return ManagedRevision{}, err
	}
	root, err := s.openResourceRoot(ctx, skill, home.RootReadWrite)
	if err != nil {
		return ManagedRevision{}, err
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir(ctx, "skills", 0o755, home.MkdirOptions{Parents: true}); err != nil {
		return ManagedRevision{}, err
	}
	temp := "skills/.stella-skill-tmp-" + randomSuffix()
	if err := root.Mkdir(ctx, temp, 0o755, home.MkdirOptions{}); err != nil {
		return ManagedRevision{}, err
	}
	defer func() { _ = root.Remove(ctx, temp, home.RemoveOptions{Recursive: true}) }()
	if err := writeSkillTree(ctx, root, temp, prepared); err != nil {
		return ManagedRevision{}, err
	}
	destination := path.Join("skills", skill.Name)
	if err := root.Rename(ctx, temp, destination, home.RenameOptions{NoReplace: true, SyncParent: true}); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ManagedRevision{}, ErrSkillNameConflict
		}
		return ManagedRevision{}, err
	}
	content, err := plugin.CaptureResource(ctx, root, destination)
	if err != nil {
		return ManagedRevision{}, err
	}
	revision, err := revisionFromContent(plugin.Scope(skill.Scope), skill.UserID, skill.AgentID, skill.Name, content)
	if err != nil {
		return ManagedRevision{}, err
	}
	if !revisionMatchesFiles(revision, prepared) {
		return ManagedRevision{}, ErrSkillDigestConflict
	}
	return revision, nil
}

func (s *FileStore) updateFileSkill(ctx context.Context, in ManagedSkillUpdate) (ManagedRevision, ManagedRevision, error) {
	identity, ok := decodeFileSkillID(in.ID)
	if !ok {
		return ManagedRevision{}, ManagedRevision{}, ErrSkillNotMutable
	}
	root, err := s.openResourceRoot(ctx, identity, home.RootReadWrite)
	if err != nil {
		return ManagedRevision{}, ManagedRevision{}, err
	}
	defer func() { _ = root.Close() }()
	currentContent, err := plugin.CaptureResource(ctx, root, path.Join("skills", identity.Name))
	if err != nil {
		return ManagedRevision{}, ManagedRevision{}, err
	}
	before, err := revisionFromContent(plugin.Scope(identity.Scope), identity.UserID, identity.AgentID, identity.Name, currentContent)
	if err != nil {
		return ManagedRevision{}, ManagedRevision{}, err
	}
	if err := checkExpectedDigest(in.ExpectedDigest, before.Skill.ContentDigest); err != nil {
		return ManagedRevision{}, ManagedRevision{}, err
	}
	if before.Skill.Status == SkillStatusDeprecated {
		return ManagedRevision{}, ManagedRevision{}, ErrSkillNotMutable
	}
	afterSkill := before.Skill
	if main, ok := in.Files[MainFile]; ok && isFullSkillDocument(main) {
		if err := applyIncomingSkillFrontmatter(&afterSkill, []byte(main), identity.Name); err != nil {
			return ManagedRevision{}, ManagedRevision{}, err
		}
	}
	if in.Patch.Description != nil {
		afterSkill.Description = *in.Patch.Description
	}
	if in.Patch.Status != nil {
		afterSkill.Status = *in.Patch.Status
	}
	if in.Patch.DisableModelInvocation != nil {
		afterSkill.DisableModelInvocation = *in.Patch.DisableModelInvocation
	}
	if len(in.Patch.Metadata) != 0 {
		afterSkill.Metadata, err = sanitizeMetadata(in.Patch.Metadata)
		if err != nil {
			return ManagedRevision{}, ManagedRevision{}, err
		}
	}
	if (afterSkill.Status != SkillStatusActive && afterSkill.Status != SkillStatusDeprecated) || strings.TrimSpace(afterSkill.Description) == "" {
		return ManagedRevision{}, ManagedRevision{}, ErrInvalidSkillRevision
	}
	files := make(map[string]ManagedSkillFile, len(before.Files)+len(in.Files))
	for filename, content := range before.Files {
		files[filename] = ManagedSkillFile{Content: bytes.Clone(content), Mode: before.Modes[filename]}
	}
	for filename, content := range in.Files {
		mode := fs.FileMode(0o644)
		if existing, exists := files[filename]; exists {
			mode = existing.Mode
		}
		files[filename] = ManagedSkillFile{Content: []byte(content), Mode: mode}
	}
	for _, filename := range in.DeleteFiles {
		if err := validateSkillPath(filename); err != nil {
			return ManagedRevision{}, ManagedRevision{}, err
		}
		if filename == MainFile {
			return ManagedRevision{}, ManagedRevision{}, errors.New("skills: cannot delete SKILL.md")
		}
		delete(files, filename)
	}
	prepared, err := prepareSkillFiles(afterSkill, files, before.Files[MainFile])
	if err != nil {
		return ManagedRevision{}, ManagedRevision{}, err
	}
	for filename, file := range prepared {
		if filename == MainFile || !bytes.Equal(file.Content, before.Files[filename]) || file.Mode.Perm() != before.Modes[filename].Perm() {
			if err := root.Mkdir(ctx, path.Dir(path.Join("skills", identity.Name, filename)), 0o755, home.MkdirOptions{Parents: true}); err != nil {
				return ManagedRevision{}, ManagedRevision{}, err
			}
			if err := root.Upload(ctx, path.Join("skills", identity.Name, filename), bytes.NewReader(file.Content), home.WriteOptions{Mode: file.Mode, Sync: true, MaxBytes: MaxManagedSkillFileBytes}); err != nil {
				return ManagedRevision{}, ManagedRevision{}, err
			}
		}
	}
	for filename := range before.Files {
		if _, exists := prepared[filename]; !exists {
			if err := root.Remove(ctx, path.Join("skills", identity.Name, filename), home.RemoveOptions{}); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return ManagedRevision{}, ManagedRevision{}, err
			}
		}
	}
	afterContent, err := plugin.CaptureResource(ctx, root, path.Join("skills", identity.Name))
	if err != nil {
		return ManagedRevision{}, ManagedRevision{}, err
	}
	after, err := revisionFromContent(plugin.Scope(identity.Scope), identity.UserID, identity.AgentID, identity.Name, afterContent)
	if err == nil && !revisionMatchesFiles(after, prepared) {
		err = ErrSkillDigestConflict
	}
	return before, after, err
}

func revisionMatchesFiles(revision ManagedRevision, files map[string]ManagedSkillFile) bool {
	if len(revision.Files) != len(files) {
		return false
	}
	for filename, expected := range files {
		actual, ok := revision.Files[filename]
		if !ok || !bytes.Equal(actual, expected.Content) {
			return false
		}
		if revision.Modes[filename] != expected.Mode.Perm()&0o555 {
			return false
		}
	}
	return true
}

func (s *FileStore) deleteFileSkill(ctx context.Context, in ManagedSkillDelete) (ManagedRevision, error) {
	identity, ok := decodeFileSkillID(in.ID)
	if !ok {
		return ManagedRevision{}, ErrSkillNotMutable
	}
	root, err := s.openResourceRoot(ctx, identity, home.RootReadWrite)
	if err != nil {
		return ManagedRevision{}, err
	}
	defer func() { _ = root.Close() }()
	content, err := plugin.CaptureResource(ctx, root, path.Join("skills", identity.Name))
	if err != nil {
		return ManagedRevision{}, err
	}
	before, err := revisionFromContent(plugin.Scope(identity.Scope), identity.UserID, identity.AgentID, identity.Name, content)
	if err != nil {
		return ManagedRevision{}, err
	}
	if err := checkExpectedDigest(in.ExpectedDigest, before.Skill.ContentDigest); err != nil {
		return ManagedRevision{}, err
	}
	if err := root.Remove(ctx, path.Join("skills", identity.Name), home.RemoveOptions{Recursive: true}); err != nil {
		return ManagedRevision{}, err
	}
	return before, nil
}

func (s *FileStore) openResourceRoot(ctx context.Context, skill Skill, access home.RootAccess) (home.RootOperations, error) {
	_, scope, ok := resourceRootFor(skill.Scope)
	if !ok {
		return nil, ErrInvalidSkillRevision
	}
	return s.roots.OpenRoot(ctx, home.WorkspaceRequest{UserID: skill.UserID, AgentID: skill.AgentID}, scope, access)
}

func prepareSkillFiles(skill Skill, files map[string]ManagedSkillFile, existingMain []byte) (map[string]ManagedSkillFile, error) {
	if err := validateFileSkill(skill); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("skills: SKILL.md is required")
	}
	result := make(map[string]ManagedSkillFile, len(files))
	for filename, file := range files {
		if err := validateSkillPath(filename); err != nil {
			return nil, err
		}
		file.Content = bytes.Clone(file.Content)
		file.Mode = writableSkillMode(file.Mode)
		result[filename] = file
	}
	main, ok := result[MainFile]
	if !ok {
		return nil, errors.New("skills: SKILL.md is required")
	}
	base := existingMain
	if base == nil {
		base = main.Content
	}
	var err error
	main.Content, err = renderSkillMarkdown(skill, base, main.Content)
	if err != nil {
		return nil, err
	}
	fm, err := parseFrontmatter(string(main.Content))
	if err != nil || fm.Name != skill.Name || strings.TrimSpace(fm.Description) == "" {
		return nil, ErrInvalidSkillRevision
	}
	main.Mode = writableSkillMode(main.Mode)
	result[MainFile] = main
	if err := validateFileStoreFiles(result); err != nil {
		return nil, err
	}
	return result, nil
}

func validateFileStoreFiles(files map[string]ManagedSkillFile) error {
	if len(files) == 0 || len(files) > plugin.ResourceMaxFiles {
		return errors.Join(ErrSkillLimit, plugin.ErrResourceLimit)
	}
	total := 0
	directories := make(map[string]struct{})
	for filename, file := range files {
		if len(file.Content) > plugin.ResourceMaxFileBytes {
			return errors.Join(ErrSkillLimit, plugin.ErrResourceLimit)
		}
		total += len(file.Content)
		if total > plugin.ResourceMaxBytes {
			return errors.Join(ErrSkillLimit, plugin.ErrResourceLimit)
		}
		parts := strings.Split(filename, "/")
		for index := 1; index < len(parts); index++ {
			directories[path.Join(parts[:index]...)] = struct{}{}
		}
	}
	if len(files)+len(directories) > plugin.ResourceMaxEntries {
		return errors.Join(ErrSkillLimit, plugin.ErrResourceLimit)
	}
	return nil
}

func writableSkillMode(mode fs.FileMode) fs.FileMode {
	mode = mode.Perm()
	if mode == 0 {
		mode = 0o644
	}
	return mode | 0o600
}

func renderSkillMarkdown(skill Skill, existing, incoming []byte) ([]byte, error) {
	fields, body, err := parseSkillDocument(existing)
	if err != nil {
		return nil, err
	}
	if incoming != nil {
		if isFullSkillDocument(string(incoming)) {
			fields, body, err = parseSkillDocument(incoming)
			if err != nil {
				return nil, err
			}
		} else {
			body = normalizeSkillBody(incoming)
		}
	}
	metadata := map[string]any{}
	if len(skill.Metadata) > 0 {
		if err := json.Unmarshal(skill.Metadata, &metadata); err != nil || metadata == nil {
			return nil, fmt.Errorf("skills: metadata must be a JSON object")
		}
	}
	delete(metadata, "created_by")
	delete(fields, "created_by")
	fields["name"] = skill.Name
	fields["description"] = skill.Description
	fields["status"] = skill.Status
	fields["disable-model-invocation"] = skill.DisableModelInvocation
	fields["metadata"] = metadata
	frontmatter, err := yaml.Marshal(fields)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(frontmatter)+len(body)+12)
	out = append(out, "---\n"...)
	out = append(out, frontmatter...)
	out = append(out, "---\n"...)
	out = append(out, body...)
	return out, nil
}

func isFullSkillDocument(content string) bool {
	return bytes.HasPrefix(bytes.TrimSpace([]byte(content)), []byte("---"))
}

// applyIncomingSkillFrontmatter applies typed fields from a full SKILL.md
// edit before the explicit API patch. Unknown fields remain in the rendered
// document, while Patch fields win when both are present.
func applyIncomingSkillFrontmatter(skill *Skill, incoming []byte, expectedName string) error {
	fields, _, err := parseSkillDocument(incoming)
	if err != nil {
		return err
	}
	if raw, ok := fields["name"]; ok {
		name, ok := raw.(string)
		if !ok || name != expectedName {
			return fmt.Errorf("skills: SKILL.md name must be %q", expectedName)
		}
	}
	if raw, ok := fields["description"]; ok {
		description, ok := raw.(string)
		if !ok {
			return errors.New("skills: SKILL.md description must be a string")
		}
		skill.Description = description
	}
	if raw, ok := fields["status"]; ok {
		status, ok := raw.(string)
		if !ok || (status != SkillStatusActive && status != SkillStatusDeprecated) {
			return fmt.Errorf("skills: invalid status %v", raw)
		}
		skill.Status = status
	}
	if raw, ok := fields["disable-model-invocation"]; ok {
		disabled, ok := raw.(bool)
		if !ok {
			return errors.New("skills: disable-model-invocation must be a boolean")
		}
		skill.DisableModelInvocation = disabled
	}
	if raw, ok := fields["metadata"]; ok {
		if raw == nil {
			skill.Metadata = json.RawMessage(`{}`)
		} else {
			metadata, ok := raw.(map[string]any)
			if !ok {
				return errors.New("skills: metadata must be an object")
			}
			encoded, err := json.Marshal(metadata)
			if err != nil {
				return err
			}
			skill.Metadata, err = sanitizeMetadata(encoded)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func parseSkillDocument(content []byte) (map[string]any, []byte, error) {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	fields := map[string]any{}
	if len(lines) >= 2 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) != "---" {
				continue
			}
			if err := yaml.Unmarshal([]byte(strings.Join(lines[1:i], "\n")), &fields); err != nil {
				return nil, nil, fmt.Errorf("skills: invalid frontmatter: %w", err)
			}
			if fields == nil {
				fields = map[string]any{}
			}
			return fields, []byte(strings.Join(lines[i+1:], "\n")), nil
		}
		return nil, nil, errors.New("skills: frontmatter is not closed")
	}
	return fields, []byte(text), nil
}

func normalizeSkillBody(content []byte) []byte {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return []byte(text)
}

func sanitizeMetadata(metadata json.RawMessage) (json.RawMessage, error) {
	fields := map[string]any{}
	if len(metadata) != 0 && string(metadata) != "null" {
		if err := json.Unmarshal(metadata, &fields); err != nil || fields == nil {
			return nil, errors.New("skills: metadata must be a JSON object")
		}
	}
	delete(fields, "created_by")
	return json.Marshal(fields)
}

func writeSkillTree(ctx context.Context, root home.RootOperations, base string, files map[string]ManagedSkillFile) error {
	for filename, file := range files {
		destination := path.Join(base, filename)
		if dir := path.Dir(destination); dir != "." {
			if err := root.Mkdir(ctx, path.Dir(destination), 0o755, home.MkdirOptions{Parents: true}); err != nil {
				return err
			}
		}
		if err := root.Upload(ctx, destination, bytes.NewReader(file.Content), home.WriteOptions{Mode: file.Mode, Sync: true, MaxBytes: MaxManagedSkillFileBytes}); err != nil {
			return err
		}
	}
	return nil
}

func randomSuffix() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

func sortedRevisionPaths(revision ManagedRevision) []string {
	return slices.Sorted(maps.Keys(revision.Files))
}
