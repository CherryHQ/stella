package skill

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/CherryHQ/stella/internal/platform/home"
)

const revisionQuarantinePrefix = ".stella-gc-"

var (
	ErrSkillMaintenanceUnavailable = errors.New("skills: maintenance root walker is unavailable")
	ErrSkillTurnRevisionChanged    = errors.New("skills: turn revision changed during admission")
)

type revisionOwner struct {
	Scope   string
	UserID  string
	AgentID string
	Name    string
}

func (o revisionOwner) skill(id string) Skill {
	return Skill{ID: id, Scope: o.Scope, UserID: o.UserID, AgentID: o.AgentID, Name: o.Name}
}

type revisionReference struct {
	SkillID string
	Digest  string
}

type revisionCleanup struct {
	request home.WorkspaceRequest
	scope   home.RootScope
	path    string
}

type revisionScanBudget struct{ remaining int }

func (b *revisionScanBudget) consume() error {
	if b.remaining == 0 {
		return ErrSkillCatalogLimit
	}
	b.remaining--
	return nil
}

// BindActiveSkillTurnSnapshot supplies the existing runtime owner snapshot to
// maintenance. The callback is called only after the managed advisory lock is
// held and must return detached SkillTurnViews.
func (s *POSIXStore) BindActiveSkillTurnSnapshot(snapshot func(context.Context) ([]SkillTurnView, error)) {
	s.activeSkillTurns = snapshot
}

// BindRevisionCleanupTrigger supplies a non-blocking event notifier. It is
// called only after the mutation lock is released and a mutation succeeded.
func (s *POSIXStore) BindRevisionCleanupTrigger(trigger func()) {
	s.revisionChange = trigger
}

// BindRevisionCleanupGuard supplies the runtime termination attestation used
// by cleanup before it quarantines bytes. A non-nil error keeps all bytes.
func (s *POSIXStore) BindRevisionCleanupGuard(guard func(context.Context) error) {
	s.revisionGuard = guard
}

// RegisterSkillTurnView closes the capture-to-registration race. It verifies
// each captured managed selector while holding the same advisory lock used by
// publication and GC, then registers the whole view before releasing it.
func (s *POSIXStore) RegisterSkillTurnView(ctx context.Context, turnID string, view SkillTurnView, register func(string, SkillTurnView) error) error {
	if register == nil {
		return errors.New("skills: turn registration callback is required")
	}
	if err := ValidateSkillTurnSelection(view); err != nil {
		return err
	}
	release, err := s.lockManagedMutations(ctx)
	if err != nil {
		return err
	}
	var resultErr error
	for _, identity := range view.ManagedIdentities() {
		current, loadErr := s.loadIdentity(ctx, identity)
		if loadErr != nil {
			resultErr = errors.Join(ErrSkillTurnRevisionChanged, loadErr)
			break
		}
		if current.Skill.ContentDigest != identity.ContentDigest || !sameSkillIdentity(current.Skill, identity) {
			resultErr = ErrSkillTurnRevisionChanged
			break
		}
	}
	if resultErr == nil {
		resultErr = register(turnID, view.Clone())
	}
	return errors.Join(resultErr, release())
}

// CleanupUnreachableRevisions reconciles immutable managed Skill revisions on
// an event edge such as deletion, last turn release, or startup recovery. It
// never trusts a missing database row until the row query has returned no rows.
func (s *POSIXStore) CleanupUnreachableRevisions(ctx context.Context) error {
	// Startup reconciliation and a degraded migration are authoritative
	// blockers. A scan during either state could mistake the legacy inventory
	// for unreachable revision bytes and quarantine live data.
	if err := s.checkAvailable(); err != nil {
		return err
	}
	maintenance, ok := s.roots.(home.SkillRootMaintenance)
	if !ok {
		return ErrSkillMaintenanceUnavailable
	}
	release, err := s.lockManagedMutations(ctx)
	if err != nil {
		return err
	}
	owners, refs, err := s.loadRevisionReachability(ctx)
	if err != nil {
		return errors.Join(err, release())
	}
	active := s.activeSkillTurns
	var activeViews []SkillTurnView
	if active != nil {
		activeViews, err = active(ctx)
		if err != nil {
			return errors.Join(err, release())
		}
	}
	if s.revisionGuard != nil {
		if guardErr := s.revisionGuard(ctx); guardErr != nil {
			return errors.Join(guardErr, release())
		}
	}
	protected := protectedRevisionKeys(activeViews)
	for ref := range refs {
		protected[ref] = struct{}{}
	}
	var quarantine []revisionCleanup
	budget := revisionScanBudget{remaining: MaxManagedSkillCatalogEntries}
	scanErr := maintenance.WalkExistingSkillRoots(ctx, func(request home.WorkspaceRequest, scope home.RootScope, root home.SkillRootOperations) error {
		found, err := s.scanRevisionRoot(ctx, root, request, scope, owners, protected, &budget)
		quarantine = append(quarantine, found...)
		return err
	})
	unlockErr := release()
	removeErr := s.removeQuarantinedRevisions(ctx, maintenance, quarantine)
	return errors.Join(scanErr, unlockErr, removeErr)
}

func (s *POSIXStore) loadRevisionReachability(ctx context.Context) (map[string]revisionOwner, map[revisionReference]struct{}, error) {
	owners := make(map[string]revisionOwner)
	rows, err := s.db.Query(ctx, `
		SELECT id, scope, COALESCE(user_id::text, ''), COALESCE(agent_id, ''), name
		FROM skill
	`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, scope, userID, agentID, name string
		if err := rows.Scan(&id, &scope, &userID, &agentID, &name); err != nil {
			rows.Close()
			return nil, nil, err
		}
		owners[id] = revisionOwner{Scope: scope, UserID: userID, AgentID: agentID, Name: name}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()

	refs := make(map[revisionReference]struct{})
	rows, err = s.db.Query(ctx, `
		SELECT skill_id, content_digest
		FROM skill_usage
		WHERE content_digest IS NOT NULL
		UNION
		SELECT skill_id, content_digest
		FROM skill_changelog
		WHERE content_digest IS NOT NULL
	`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var ref revisionReference
		if err := rows.Scan(&ref.SkillID, &ref.Digest); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if validSkillDigest(ref.Digest) {
			refs[ref] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	return owners, refs, nil
}

func protectedRevisionKeys(views []SkillTurnView) map[revisionReference]struct{} {
	protected := make(map[revisionReference]struct{})
	for _, view := range views {
		for _, identity := range view.ManagedIdentities() {
			if validSkillDigest(identity.ContentDigest) {
				protected[revisionReference{SkillID: identity.ID, Digest: identity.ContentDigest}] = struct{}{}
			}
		}
	}
	return protected
}

func (s *POSIXStore) scanRevisionRoot(ctx context.Context, root home.SkillRootOperations, request home.WorkspaceRequest, scope home.RootScope, owners map[string]revisionOwner, protected map[revisionReference]struct{}, budget *revisionScanBudget) ([]revisionCleanup, error) {
	entries, err := root.List(ctx, managedRevisionRoot, home.ListOptions{Limit: MaxManagedSkillCatalogEntries})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var quarantine []revisionCleanup
	for _, entry := range entries {
		if err := budget.consume(); err != nil {
			return quarantine, err
		}
		if entry.Name() == "" || !entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 || !validInventoryComponent(entry.Name()) {
			continue
		}
		skillID := entry.Name()
		owner, exists := owners[skillID]
		preserve, selected, err := s.reconcileSelector(ctx, root, request, scope, skillID, owner, exists, protected)
		if err != nil {
			return quarantine, err
		}
		revisionsPath := path.Join(managedRevisionRoot, skillID)
		revisions, err := root.List(ctx, revisionsPath, home.ListOptions{Limit: MaxManagedSkillCatalogEntries})
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return quarantine, err
		}
		for _, revision := range revisions {
			if err := budget.consume(); err != nil {
				return quarantine, err
			}
			digest := revision.Name()
			if strings.HasPrefix(digest, revisionQuarantinePrefix) && validSkillDigest(strings.TrimPrefix(digest, revisionQuarantinePrefix)) {
				quarantine = append(quarantine, revisionCleanup{request: request, scope: scope, path: path.Join(revisionsPath, digest)})
				continue
			}
			if !revision.IsDir() || revision.Type()&fs.ModeSymlink != 0 || !validSkillDigest(digest) || preserve || selected == digest {
				continue
			}
			identity, err := readRevisionIdentity(ctx, root, skillID, digest)
			if err != nil || !identityMatchesRoot(identity, request, scope) {
				continue
			}
			if _, ok := protected[revisionReference{SkillID: skillID, Digest: digest}]; ok {
				continue
			}
			quarantineName := revisionQuarantinePrefix + digest
			if err := root.Rename(ctx, path.Join(revisionsPath, digest), path.Join(revisionsPath, quarantineName), home.RenameOptions{NoReplace: true, SyncParent: true}); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if !errors.Is(err, fs.ErrExist) {
					return quarantine, err
				}
				// Leave the source revision for a later event. The existing
				// quarantine name is removed in the second pass first.
				continue
			}
			quarantine = append(quarantine, revisionCleanup{request: request, scope: scope, path: path.Join(revisionsPath, quarantineName)})
		}
	}
	return quarantine, nil
}

func (s *POSIXStore) reconcileSelector(ctx context.Context, root home.SkillRootOperations, request home.WorkspaceRequest, scope home.RootScope, skillID string, owner revisionOwner, exists bool, protected map[revisionReference]struct{}) (preserve bool, selected string, err error) {
	target, err := root.Readlink(ctx, skillID)
	if errors.Is(err, fs.ErrNotExist) {
		return exists, "", nil
	}
	if err != nil {
		return true, "", err
	}
	selected, err = parseSelectedRevision(Skill{ID: skillID}, target)
	if err != nil {
		return true, "", err
	}
	if exists {
		identity := owner.skill(skillID)
		if !identityMatchesRoot(identity, request, scope) {
			return true, selected, nil
		}
		if _, err := readRevisionSnapshot(ctx, root, identity, selected); err != nil {
			return true, selected, err
		}
		protected[revisionReference{SkillID: skillID, Digest: selected}] = struct{}{}
		return false, selected, nil
	}
	identity, err := readRevisionIdentity(ctx, root, skillID, selected)
	if err != nil {
		return true, selected, err
	}
	if !identityMatchesRoot(identity, request, scope) {
		return true, selected, nil
	}
	if err := root.Remove(ctx, skillID, home.RemoveOptions{}); err != nil {
		return true, selected, fmt.Errorf("%w: remove deleted Skill selector: %w", home.ErrOutcomeUnknown, err)
	}
	if err := root.SyncDirectory(ctx, "."); err != nil {
		return true, selected, fmt.Errorf("%w: sync deleted Skill selector: %w", home.ErrOutcomeUnknown, err)
	}
	return false, "", nil
}

func readRevisionIdentity(ctx context.Context, root home.SkillRootOperations, skillID, digest string) (Skill, error) {
	pathName, err := selectedRevisionPath(Skill{ID: skillID}, digest)
	if err != nil {
		return Skill{}, err
	}
	manifest, err := readRootBytes(ctx, root, path.Join(pathName, SkillManifestFile), MaxManagedSkillManifestBytes)
	if err != nil {
		return Skill{}, err
	}
	identity, err := decodeCanonicalManifest(manifest)
	if err != nil || identity.ID != skillID {
		return Skill{}, errors.Join(err, ErrInvalidSkillRevision)
	}
	snapshot, err := readRevisionSnapshot(ctx, root, identity, digest)
	if err != nil {
		return Skill{}, err
	}
	return snapshot.Skill, nil
}

func identityMatchesRoot(identity Skill, request home.WorkspaceRequest, scope home.RootScope) bool {
	switch scope {
	case home.RootSystemSkills:
		return identity.Scope == "system" && request.UserID == "" && request.AgentID == ""
	case home.RootSystemAgentSkills:
		return identity.Scope == "system_agent" && identity.AgentID == request.AgentID
	case home.RootUserSkills:
		return identity.Scope == "user" && identity.UserID == request.UserID
	case home.RootUserAgentSkills:
		return identity.Scope == "user_agent" && identity.UserID == request.UserID && identity.AgentID == request.AgentID
	default:
		return false
	}
}

func (s *POSIXStore) removeQuarantinedRevisions(ctx context.Context, maintenance home.SkillRootMaintenance, quarantine []revisionCleanup) error {
	if len(quarantine) == 0 {
		return nil
	}
	remaining := make(map[string]revisionCleanup, len(quarantine))
	for _, item := range quarantine {
		key := fmt.Sprintf("%d\x00%s\x00%s\x00%s", item.scope, item.request.UserID, item.request.AgentID, item.path)
		remaining[key] = item
	}
	return maintenance.WalkExistingSkillRoots(ctx, func(request home.WorkspaceRequest, scope home.RootScope, root home.SkillRootOperations) error {
		for key, item := range remaining {
			if item.scope != scope || item.request != request {
				continue
			}
			if _, err := root.Lstat(ctx, item.path); errors.Is(err, fs.ErrNotExist) {
				delete(remaining, key)
				continue
			} else if err != nil {
				return err
			}
			if err := root.Remove(ctx, item.path, home.RemoveOptions{Recursive: true}); err != nil {
				return err
			}
			delete(remaining, key)
		}
		return nil
	})
}
