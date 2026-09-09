package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// PreparedLegacyFileExport is the immutable hand-off between the legacy
// managed-Skill reader and the file-backed publisher. SourceIdentityID and
// SourceDigest are evidence for the startup cutover transaction; they are not
// authored file metadata.
type PreparedLegacyFileExport struct {
	Skills     []LegacyFileSkillExport
	Settings   []LegacySkillSettingsExport
	revalidate func(context.Context) error
}

type LegacyFileSkillExport struct {
	Scope                  string
	UserID                 string
	AgentID                string
	Name                   string
	FileSkillID            string
	SourceIdentityID       string
	SourceDigest           string
	Description            string
	DisableModelInvocation bool
	Metadata               json.RawMessage
	Files                  map[string]ManagedSkillFile
	// ContentDigest is the digest returned by the file ResourceStore after
	// publication. It is filled by PublishLegacyFileExport.
	ContentDigest string
}

type LegacySkillSettingsExport struct {
	Scope     string
	UserID    string
	AgentID   string
	Disabled  []string
	Forbidden []string
}

// PrepareLegacyFileExport verifies every legacy identity and its CURRENT
// revision while holding the managed-Skill mutation lock. Callers must run the
// old PostgreSQL-to-POSIX migrator first so the selector and immutable revision
// are present in the legacy roots.
func (s *LegacySkillStore) PrepareLegacyFileExport(ctx context.Context) (prepared PreparedLegacyFileExport, resultErr error) {
	if s == nil {
		return prepared, errors.New("skills: legacy Skill store is required")
	}
	release, err := s.lockManagedMutationsForMigration(ctx)
	if err != nil {
		return prepared, err
	}
	defer finishManagedMutation(release, &resultErr)

	identities, err := s.listAllIdentitiesForMigration(ctx)
	if err != nil {
		return prepared, err
	}
	for _, identity := range identities {
		snapshot, err := s.loadIdentityForMigration(ctx, identity)
		if legacySelectorUnavailable(err) && legacySelectorOverlapsResource(identity.Scope) {
			snapshot, err = s.loadArchivedIdentityForMigration(ctx, identity)
		}
		if err != nil {
			return prepared, fmt.Errorf("prepare legacy Skill %s: %w", identity.ID, err)
		}
		files := make(map[string]ManagedSkillFile, len(snapshot.Files))
		for _, file := range snapshot.Files {
			files[file.Path] = ManagedSkillFile{Content: bytes.Clone(file.Content), Mode: file.Mode}
		}
		main, ok := files[MainFile]
		if !ok {
			return prepared, fmt.Errorf("prepare legacy Skill %s: %w", identity.ID, ErrInvalidSkillRevision)
		}
		// The old store keeps the authored body in SKILL.md. Render the
		// ordinary file frontmatter from the verified identity exactly once.
		rendered, err := renderSkillMarkdown(snapshot.Skill, []byte("---\n---\n"), main.Content)
		if err != nil {
			return prepared, fmt.Errorf("render legacy Skill %s: %w", identity.ID, err)
		}
		main.Content = rendered
		files[MainFile] = main
		newID := fileSkillID(identity.Scope, identity.UserID, identity.AgentID, identity.Name)
		if newID == "" {
			return prepared, fmt.Errorf("prepare legacy Skill %s: invalid file resource identity", identity.ID)
		}
		entry := LegacyFileSkillExport{
			Scope: identity.Scope, UserID: identity.UserID, AgentID: identity.AgentID,
			Name: identity.Name, FileSkillID: newID, SourceIdentityID: identity.ID,
			SourceDigest: snapshot.Skill.ContentDigest, Description: snapshot.Skill.Description,
			DisableModelInvocation: snapshot.Skill.DisableModelInvocation, Metadata: bytes.Clone(snapshot.Skill.Metadata), Files: files,
		}
		prepared.Skills = append(prepared.Skills, entry)
		if snapshot.Skill.Status != SkillStatusActive {
			prepared.addDisabled(snapshot.Skill)
		}
	}
	sort.Slice(prepared.Skills, func(i, j int) bool { return prepared.Skills[i].FileSkillID < prepared.Skills[j].FileSkillID })
	sort.Slice(prepared.Settings, func(i, j int) bool { return settingsKey(prepared.Settings[i]) < settingsKey(prepared.Settings[j]) })
	prepared.revalidate = func(ctx context.Context) error {
		release, err := s.lockManagedMutationsForMigration(ctx)
		if err != nil {
			return err
		}
		defer finishManagedMutation(release, &err)
		for _, entry := range prepared.Skills {
			identity, err := s.getIdentityForMigration(ctx, entry.SourceIdentityID)
			if err != nil || identity == nil {
				return errors.Join(err, fmt.Errorf("legacy Skill %s identity disappeared", entry.SourceIdentityID))
			}
			snapshot, err := s.loadIdentityForMigration(ctx, *identity)
			if legacySelectorUnavailable(err) && legacySelectorOverlapsResource(identity.Scope) {
				snapshot, err = s.loadArchivedIdentityForMigration(ctx, *identity)
			}
			if err != nil || snapshot.Skill.ContentDigest != entry.SourceDigest {
				return errors.Join(err, ErrSkillDigestConflict)
			}
		}
		return nil
	}
	return prepared, nil
}

// listIdentityByScopeForMigration is deliberately separate from the runtime
// method: startup has fenced managed writes and must still read the authority
// while the runtime availability gate is closed.
func (s *LegacySkillStore) listAllIdentitiesForMigration(ctx context.Context) ([]Skill, error) {
	rows, err := s.db.Query(ctx, `SELECT id, scope, user_id, agent_id, name, description, status, disable_model_invocation, metadata, created_at, updated_at, version FROM skill ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inventory := make([]sqlc.Skill, 0)
	for rows.Next() {
		var row sqlc.Skill
		if err := rows.Scan(&row.ID, &row.Scope, &row.UserID, &row.AgentID, &row.Name, &row.Description, &row.Status, &row.DisableModelInvocation, &row.Metadata, &row.CreatedAt, &row.UpdatedAt, &row.Version); err != nil {
			return nil, err
		}
		inventory = append(inventory, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return identitiesFromRows(inventory)
}

func legacySelectorOverlapsResource(scope string) bool {
	return scope == "system_agent" || scope == "user"
}

func legacySelectorUnavailable(err error) bool {
	return errors.Is(err, errCurrentSkillSelectorMissing) || errors.Is(err, syscall.EINVAL)
}

func (s *LegacySkillStore) loadArchivedIdentityForMigration(ctx context.Context, identity Skill) (managedSnapshot, error) {
	root, err := s.openExistingSkillRoot(ctx, identity)
	if err != nil {
		return managedSnapshot{}, err
	}
	defer func() { _ = root.Close() }()
	target, err := root.Readlink(ctx, path.Join(".stella-legacy-selectors", identity.ID))
	if err != nil {
		return managedSnapshot{}, err
	}
	digest, err := parseSelectedRevision(identity, target)
	if err != nil {
		return managedSnapshot{}, err
	}
	return readRevisionSnapshot(ctx, root, identity, digest)
}

func (p *PreparedLegacyFileExport) addDisabled(identity Skill) {
	for i := range p.Settings {
		if p.Settings[i].Scope == identity.Scope && p.Settings[i].UserID == identity.UserID && p.Settings[i].AgentID == identity.AgentID {
			p.Settings[i].Disabled = append(p.Settings[i].Disabled, "skill:"+identity.Name)
			return
		}
	}
	p.Settings = append(p.Settings, LegacySkillSettingsExport{Scope: identity.Scope, UserID: identity.UserID, AgentID: identity.AgentID, Disabled: []string{"skill:" + identity.Name}})
}

func settingsKey(s LegacySkillSettingsExport) string {
	return s.Scope + "\x00" + s.UserID + "\x00" + s.AgentID
}

func legacyOwnerKey(scope, userID, agentID string) string {
	return scope + "\x00" + userID + "\x00" + agentID
}

// PublishLegacyFileExport publishes each scope into its ordinary resource
// root. A complete existing tree is accepted as an idempotent retry; any
// differing tree is a hard conflict. No managed-Skill changelog is created.
func PublishLegacyFileExport(ctx context.Context, roots home.RootOpener, export PreparedLegacyFileExport) (resultErr error) {
	if roots == nil {
		return errors.New("skills: resource root opener is required")
	}
	if export.revalidate != nil {
		if err := export.revalidate(ctx); err != nil {
			return fmt.Errorf("validate legacy Skill selectors: %w", err)
		}
	}
	groups := make(map[string][]*LegacyFileSkillExport)
	for i := range export.Skills {
		entry := &export.Skills[i]
		if err := validateLegacyEntry(*entry); err != nil {
			return err
		}
		key := legacyOwnerKey(entry.Scope, entry.UserID, entry.AgentID)
		groups[key] = append(groups[key], entry)
	}
	settings := make(map[string]LegacySkillSettingsExport, len(export.Settings))
	for _, value := range export.Settings {
		if err := validateLegacyOwner(value.Scope, value.UserID, value.AgentID); err != nil {
			return err
		}
		canonicalizeLegacySettings(&value)
		settings[settingsKey(value)] = value
	}
	owners := make([]string, 0, len(groups))
	for owner := range groups {
		owners = append(owners, owner)
	}
	slices.Sort(owners)
	for _, ownerKey := range owners {
		parts := strings.Split(ownerKey, "\x00")
		if len(parts) != 3 {
			return errors.New("skills: invalid migration owner key")
		}
		scope, userID, agentID := parts[0], parts[1], parts[2]
		root, err := roots.OpenRoot(ctx, home.WorkspaceRequest{UserID: userID, AgentID: agentID}, legacyResourceRoot(scope), home.RootReadWrite)
		if err != nil {
			return err
		}
		publishErr := publishLegacyScope(ctx, root, groups[ownerKey])
		closeErr := root.Close()
		if publishErr != nil || closeErr != nil {
			return errors.Join(publishErr, closeErr)
		}
	}
	return nil
}

func legacyResourceRoot(scope string) home.RootScope {
	switch scope {
	case "system":
		return home.RootSystemResources
	case "system_agent":
		return home.RootSystemAgentResources
	case "user":
		return home.RootUserResources
	case "user_agent":
		return home.RootUserAgentResources
	default:
		return 0
	}
}

func validateLegacyOwner(scope, userID, agentID string) error {
	switch scope {
	case "system":
		if userID != "" || agentID != "" {
			return errors.New("skills: system owner must be empty")
		}
	case "system_agent":
		if userID != "" || agentID == "" {
			return errors.New("skills: invalid system_agent owner")
		}
	case "user":
		if userID == "" || agentID != "" {
			return errors.New("skills: invalid user owner")
		}
	case "user_agent":
		if userID == "" || agentID == "" {
			return errors.New("skills: invalid user_agent owner")
		}
	default:
		return fmt.Errorf("skills: unknown scope %q", scope)
	}
	return nil
}

func validateLegacyEntry(entry LegacyFileSkillExport) error {
	if err := validateLegacyOwner(entry.Scope, entry.UserID, entry.AgentID); err != nil {
		return err
	}
	if err := skillNameValidationError(entry.Name, entry.Name); err != nil {
		return fmt.Errorf("skills: invalid legacy Skill name %q: %w", entry.Name, err)
	}
	if fileSkillID(entry.Scope, entry.UserID, entry.AgentID, entry.Name) != entry.FileSkillID {
		return fmt.Errorf("skills: invalid file Skill ID for %q", entry.Name)
	}
	if len(entry.Files) == 0 || entry.Files[MainFile].Content == nil {
		return fmt.Errorf("skills: Skill %q has no SKILL.md", entry.Name)
	}
	return nil
}

func canonicalizeLegacySettings(settings *LegacySkillSettingsExport) {
	slices.Sort(settings.Disabled)
	settings.Disabled = slices.Compact(settings.Disabled)
	slices.Sort(settings.Forbidden)
	settings.Forbidden = slices.Compact(settings.Forbidden)
}

func publishLegacyScope(ctx context.Context, root home.RootOperations, entries []*LegacyFileSkillExport) error {
	if len(entries) > 0 {
		_ = root.Mkdir(ctx, "skills", 0o755, home.MkdirOptions{Parents: true})
	}
	for _, entry := range entries {
		if err := archiveLegacySelector(ctx, root, entry); err != nil {
			return err
		}
		if err := publishLegacySkill(ctx, root, entry); err != nil {
			return err
		}
	}
	return nil
}

// archiveLegacySelector removes the old UUID-named selector from the ordinary
// resource tree. The selector is retained under a hidden directory so an
// interrupted migration can be verified and resumed without leaving a broken
// Skill candidate for runtime discovery.
func archiveLegacySelector(ctx context.Context, root home.RootOperations, entry *LegacyFileSkillExport) error {
	if !legacySelectorOverlapsResource(entry.Scope) {
		return nil
	}
	skillRoot, ok := root.(home.SkillRootOperations)
	if !ok {
		return errors.New("skills: resource root cannot validate legacy selector")
	}
	prefixed := prefixedSkillRoot{SkillRootOperations: skillRoot, prefix: "skills"}
	identity := Skill{ID: entry.SourceIdentityID, Scope: entry.Scope, UserID: entry.UserID, AgentID: entry.AgentID, Name: entry.Name}
	current, err := readCurrentSnapshot(ctx, &prefixed, identity)
	if legacySelectorUnavailable(err) {
		archived, archivedErr := readArchivedSnapshot(ctx, &prefixed, identity)
		if archivedErr != nil {
			return errors.Join(err, archivedErr, ErrSkillDigestConflict)
		}
		if archived.Skill.ContentDigest != entry.SourceDigest {
			return ErrSkillDigestConflict
		}
		return nil
	}
	if err != nil || current.Skill.ContentDigest != entry.SourceDigest {
		return errors.Join(err, ErrSkillDigestConflict)
	}
	if err := root.Mkdir(ctx, "skills/.stella-legacy-selectors", 0o700, home.MkdirOptions{Parents: true}); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	oldPath := path.Join("skills", entry.SourceIdentityID)
	archivedPath := path.Join("skills", ".stella-legacy-selectors", entry.SourceIdentityID)
	if err := root.Rename(ctx, oldPath, archivedPath, home.RenameOptions{NoReplace: true, SyncParent: true}); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		archived, archivedErr := readArchivedSnapshot(ctx, &prefixed, identity)
		if archivedErr != nil || archived.Skill.ContentDigest != entry.SourceDigest {
			return errors.Join(archivedErr, ErrSkillDigestConflict)
		}
	}
	return nil
}

func readArchivedSnapshot(ctx context.Context, root home.SkillRootOperations, identity Skill) (managedSnapshot, error) {
	target, err := root.Readlink(ctx, path.Join(".stella-legacy-selectors", identity.ID))
	if err != nil {
		return managedSnapshot{}, err
	}
	digest, err := parseSelectedRevision(identity, target)
	if err != nil {
		return managedSnapshot{}, err
	}
	return readRevisionSnapshot(ctx, root, identity, digest)
}

// prefixedSkillRoot adapts the shared resource root to the old Skill-root
// coordinate system. Its Close is intentionally a no-op; the caller owns the
// underlying capability.
type prefixedSkillRoot struct {
	home.SkillRootOperations
	prefix string
}

func (r *prefixedSkillRoot) prefixed(name string) string {
	if name == "." || name == "" {
		return r.prefix
	}
	return path.Join(r.prefix, name)
}
func (r *prefixedSkillRoot) Close() error { return nil }
func (r *prefixedSkillRoot) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	return r.SkillRootOperations.Stat(ctx, r.prefixed(name))
}

func (r *prefixedSkillRoot) Lstat(ctx context.Context, name string) (fs.FileInfo, error) {
	return r.SkillRootOperations.Lstat(ctx, r.prefixed(name))
}

func (r *prefixedSkillRoot) List(ctx context.Context, name string, opts home.ListOptions) ([]fs.DirEntry, error) {
	return r.SkillRootOperations.List(ctx, r.prefixed(name), opts)
}

func (r *prefixedSkillRoot) Read(ctx context.Context, name string, dst io.Writer, opts home.ReadOptions) error {
	return r.SkillRootOperations.Read(ctx, r.prefixed(name), dst, opts)
}

func (r *prefixedSkillRoot) Write(ctx context.Context, name string, src io.Reader, opts home.WriteOptions) error {
	return r.SkillRootOperations.Write(ctx, r.prefixed(name), src, opts)
}

func (r *prefixedSkillRoot) Upload(ctx context.Context, name string, src io.Reader, opts home.WriteOptions) error {
	return r.SkillRootOperations.Upload(ctx, r.prefixed(name), src, opts)
}

func (r *prefixedSkillRoot) Mkdir(ctx context.Context, name string, mode fs.FileMode, opts home.MkdirOptions) error {
	return r.SkillRootOperations.Mkdir(ctx, r.prefixed(name), mode, opts)
}

func (r *prefixedSkillRoot) Remove(ctx context.Context, name string, opts home.RemoveOptions) error {
	return r.SkillRootOperations.Remove(ctx, r.prefixed(name), opts)
}

func (r *prefixedSkillRoot) Rename(ctx context.Context, old, new string, opts home.RenameOptions) error {
	return r.SkillRootOperations.Rename(ctx, r.prefixed(old), r.prefixed(new), opts)
}

func (r *prefixedSkillRoot) Symlink(ctx context.Context, target, name string) error {
	return r.SkillRootOperations.Symlink(ctx, target, r.prefixed(name))
}

func (r *prefixedSkillRoot) Readlink(ctx context.Context, name string) (string, error) {
	return r.SkillRootOperations.Readlink(ctx, r.prefixed(name))
}

func (r *prefixedSkillRoot) SyncDirectory(ctx context.Context, name string) error {
	return r.SkillRootOperations.SyncDirectory(ctx, r.prefixed(name))
}

func publishLegacySkill(ctx context.Context, root home.RootOperations, entry *LegacyFileSkillExport) error {
	stage := path.Join("skills", ".stella-migrate-"+entry.FileSkillID[len("file:"):])
	target := path.Join("skills", entry.Name)
	if err := root.Mkdir(ctx, stage, 0o700, home.MkdirOptions{Parents: true}); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	for filename, file := range entry.Files {
		parent := path.Dir(filename)
		if parent != "." {
			if err := root.Mkdir(ctx, path.Join(stage, parent), 0o755, home.MkdirOptions{Parents: true}); err != nil {
				return err
			}
		}
		if err := root.Write(ctx, path.Join(stage, filename), bytes.NewReader(file.Content), home.WriteOptions{Mode: file.Mode.Perm(), Exclusive: true, Sync: true}); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	if err := compareLegacyTree(ctx, root, stage, entry.Files); err != nil {
		return err
	}
	if err := root.Rename(ctx, stage, target, home.RenameOptions{NoReplace: true, SyncParent: true}); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := compareLegacyTree(ctx, root, target, entry.Files); err != nil {
			return err
		}
		_ = root.Remove(ctx, stage, home.RemoveOptions{Recursive: true})
	}
	content, err := plugin.CaptureResource(ctx, root, target)
	if err != nil {
		return err
	}
	entry.ContentDigest = content.Digest
	return nil
}

func compareLegacyTree(ctx context.Context, root home.RootOperations, base string, expected map[string]ManagedSkillFile) error {
	actual, err := captureLegacyTree(ctx, root, base)
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("skills: target %s conflicts with legacy export", base)
	}
	for name, want := range expected {
		got, ok := actual[name]
		if !ok || got.Mode.Perm() != want.Mode.Perm() || !bytes.Equal(got.Content, want.Content) {
			return fmt.Errorf("skills: target %s conflicts with legacy export", base)
		}
	}
	return nil
}

type capturedLegacyFile struct {
	Content []byte
	Mode    fs.FileMode
}

func captureLegacyTree(ctx context.Context, root home.RootOperations, base string) (map[string]capturedLegacyFile, error) {
	out := map[string]capturedLegacyFile{}
	var walk func(string, string) error
	walk = func(dir, relative string) error {
		entries, err := root.List(ctx, dir, home.ListOptions{Limit: 1024})
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			child := path.Join(dir, name)
			rel := path.Join(relative, name)
			if entry.IsDir() {
				if err := walk(child, rel); err != nil {
					return err
				}
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return errors.Join(err, fmt.Errorf("skills: target contains non-regular file"))
			}
			var data bytes.Buffer
			if err := root.Read(ctx, child, &data, home.ReadOptions{MaxBytes: MaxManagedSkillFileBytes}); err != nil {
				return err
			}
			out[rel] = capturedLegacyFile{Content: data.Bytes(), Mode: info.Mode()}
		}
		return nil
	}
	return out, walk(base, "")
}
