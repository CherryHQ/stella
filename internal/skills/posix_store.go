package skills

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	skillReconcileTimeout      = 5 * time.Second
	managedSkillCleanupTimeout = 5 * time.Second
	managedSkillLockRetryDelay = 25 * time.Millisecond
	managedSkillAdvisoryLock   = int64(0x5354454c4c41534b)
)

type managedSkillLockSession struct {
	lock    func(context.Context) error
	unlock  func(context.Context) (bool, error)
	release func()
	discard func(context.Context) error
}

// POSIXStore keeps logical identity, policy evidence, and usage in PostgreSQL;
// mutable metadata and bytes come only from verified revisions in typed Home.
type POSIXStore struct {
	db                 *pgxpool.Pool
	q                  *sqlc.Queries
	roots              home.SkillRootOpener
	now                func() time.Time
	random             func([]byte) error
	acquireManagedLock func(context.Context) (managedSkillLockSession, error)
	cleanupContext     func() (context.Context, context.CancelFunc)
}

func NewPOSIXStore(db *pgxpool.Pool, roots home.SkillRootOpener) (*POSIXStore, error) {
	if db == nil || roots == nil {
		return nil, errors.New("skills: database and Home roots are required")
	}
	store := &POSIXStore{
		db: db, q: sqlc.New(db), roots: roots,
		now: func() time.Time { return time.Now().UTC() },
		random: func(dst []byte) error {
			_, err := rand.Read(dst)
			return err
		},
		cleanupContext: freshManagedSkillCleanupContext,
	}
	store.acquireManagedLock = store.acquireManagedSkillLockSession
	return store, nil
}

func freshManagedSkillCleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), managedSkillCleanupTimeout)
}

func (s *POSIXStore) acquireManagedSkillLockSession(ctx context.Context) (managedSkillLockSession, error) {
	var conn *pgxpool.Conn
	return managedSkillLockSession{
		lock: func(ctx context.Context) error {
			for {
				candidate, err := s.db.Acquire(ctx)
				if err != nil {
					return err
				}
				var locked bool
				err = candidate.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", managedSkillAdvisoryLock).Scan(&locked)
				if err != nil {
					// The server may have acquired the session lock before the
					// acknowledgement failed. Keep the connection so the caller can
					// discard it and release every session-level lock.
					conn = candidate
					return fmt.Errorf("%w: try managed Skill lock: %w", home.ErrOutcomeUnknown, err)
				}
				if locked {
					conn = candidate
					return nil
				}
				// A waiter never occupies a pool connection. The one lock owner
				// retains a connection while leaving the rest of the pool available
				// to Home authorization and evidence transactions inside the body.
				candidate.Release()
				timer := time.NewTimer(managedSkillLockRetryDelay)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return ctx.Err()
				case <-timer.C:
				}
			}
		},
		unlock: func(ctx context.Context) (bool, error) {
			if conn == nil {
				return false, errors.New("skills: managed Skill lock connection is unavailable")
			}
			var unlocked bool
			err := conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", managedSkillAdvisoryLock).Scan(&unlocked)
			return unlocked, err
		},
		release: func() {
			if conn != nil {
				conn.Release()
				conn = nil
			}
		},
		discard: func(ctx context.Context) error {
			if conn == nil {
				return nil
			}
			owned := conn.Hijack()
			conn = nil
			return owned.Close(ctx)
		},
	}, nil
}

func (s *POSIXStore) lockManagedMutations(ctx context.Context) (func() error, error) {
	session, err := s.acquireManagedLock(ctx)
	if err != nil {
		return nil, err
	}
	if err := session.lock(ctx); err != nil {
		if !home.IsOutcomeUnknown(err) {
			session.release()
			return nil, err
		}
		closeCtx, cancel := s.cleanupContext()
		closeErr := session.discard(closeCtx)
		cancel()
		return nil, fmt.Errorf("%w: acquire managed Skill lock: %w", home.ErrOutcomeUnknown, errors.Join(err, closeErr))
	}
	return func() error {
		unlockCtx, cancel := s.cleanupContext()
		unlocked, unlockErr := session.unlock(unlockCtx)
		cancel()
		if unlockErr == nil && unlocked {
			session.release()
			return nil
		}
		closeCtx, closeCancel := s.cleanupContext()
		closeErr := session.discard(closeCtx)
		closeCancel()
		return fmt.Errorf("%w: release managed Skill lock: %w", home.ErrOutcomeUnknown, errors.Join(unlockErr, closeErr))
	}, nil
}

func finishManagedMutation(release func() error, resultErr *error) {
	if err := release(); err != nil {
		*resultErr = errors.Join(*resultErr, err)
	}
}

func identityFromRow(row sqlc.Skill) Skill {
	return Skill{ID: row.ID, Scope: row.Scope, UserID: row.UserID.String, AgentID: row.AgentID.String, Name: row.Name}
}

func (s *POSIXStore) openSkillRoot(ctx context.Context, identity Skill, access home.RootAccess) (home.SkillRootOperations, error) {
	request, scope, err := skillRootSelection(identity)
	if err != nil {
		return nil, err
	}
	return s.roots.OpenSkillRoot(ctx, request, scope, access)
}

func (s *POSIXStore) openExistingSkillRoot(ctx context.Context, identity Skill) (home.SkillRootOperations, error) {
	request, scope, err := skillRootSelection(identity)
	if err != nil {
		return nil, err
	}
	return s.roots.OpenExistingSkillRoot(ctx, request, scope)
}

func freshSkillContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), skillReconcileTimeout)
}

func (s *POSIXStore) loadIdentity(ctx context.Context, identity Skill) (snapshot managedSnapshot, err error) {
	root, err := s.openExistingSkillRoot(ctx, identity)
	if err != nil {
		return managedSnapshot{}, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return readCurrentSnapshot(ctx, root, identity)
}

func (s *POSIXStore) loadIdentityRevision(ctx context.Context, identity Skill, digest string) (snapshot managedSnapshot, err error) {
	root, err := s.openExistingSkillRoot(ctx, identity)
	if err != nil {
		return managedSnapshot{}, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return readRevisionSnapshot(ctx, root, identity, digest)
}

func managedRevisionFromSnapshot(snapshot managedSnapshot) ManagedRevision {
	files := make(map[string][]byte, len(snapshot.Files))
	modes := make(map[string]fs.FileMode, len(snapshot.Files))
	for _, file := range snapshot.Files {
		files[file.Path] = append([]byte(nil), file.Content...)
		modes[file.Path] = file.Mode
	}
	return ManagedRevision{Skill: snapshot.Skill, Files: files, Modes: modes}
}

func (s *POSIXStore) LoadCurrentRevision(ctx context.Context, identity Skill) (ManagedRevision, error) {
	snapshot, err := s.loadIdentity(ctx, identity)
	return managedRevisionFromSnapshot(snapshot), err
}

func (s *POSIXStore) LoadExactRevision(ctx context.Context, identity Skill, digest string) (ManagedRevision, error) {
	if !validSkillDigest(digest) {
		return ManagedRevision{}, ErrSkillDigestRequired
	}
	snapshot, err := s.loadIdentityRevision(ctx, identity, digest)
	return managedRevisionFromSnapshot(snapshot), err
}

func (s *POSIXStore) GetIdentity(ctx context.Context, id string) (*Skill, error) {
	row, err := s.q.GetSkillByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("skills: get identity %q: %w", id, err)
	}
	identity := identityFromRow(row)
	return &identity, nil
}

func identitiesFromRows(rows []sqlc.Skill) ([]Skill, error) {
	if len(rows) > MaxManagedSkillCatalogEntries {
		return nil, ErrSkillCatalogLimit
	}
	out := make([]Skill, len(rows))
	for i := range rows {
		out[i] = identityFromRow(rows[i])
	}
	return out, nil
}

func (s *POSIXStore) ListIdentityVisible(ctx context.Context, vc ViewContext) ([]Skill, error) {
	rows, err := s.q.ListSkillIdentityVisible(ctx, sqlc.ListSkillIdentityVisibleParams{
		AgentID: pgtype.Text{String: vc.AgentID, Valid: vc.AgentID != ""},
		UserID:  pgtype.Text{String: vc.UserID, Valid: vc.UserID != ""},
	})
	if err != nil {
		return nil, err
	}
	return identitiesFromRows(rows)
}

func (s *POSIXStore) ListIdentityCandidate(ctx context.Context, name string, vc ViewContext) ([]Skill, error) {
	rows, err := s.ListIdentityVisible(ctx, vc)
	if err != nil {
		return nil, err
	}
	out := make([]Skill, 0, 4)
	for _, row := range rows {
		if row.Name == name {
			out = append(out, row)
		}
	}
	if len(out) > 4 {
		return nil, ErrSkillCatalogLimit
	}
	return out, nil
}

func (s *POSIXStore) ListIdentityByScope(ctx context.Context, scope, userID, agentID string) ([]Skill, error) {
	rows, err := s.q.ListSkillsByScope(ctx, sqlc.ListSkillsByScopeParams{
		Scope: scope, UserID: pgtype.Text{String: userID, Valid: userID != ""}, AgentID: pgtype.Text{String: agentID, Valid: agentID != ""},
	})
	if err != nil {
		return nil, err
	}
	return identitiesFromRows(rows)
}

func invocationVisible(sk Skill) bool {
	return sk.Status != SkillStatusDeprecated && !sk.DisableModelInvocation
}

func (s *POSIXStore) loadRows(ctx context.Context, rows []Skill, visible func(Skill) bool) ([]Skill, error) {
	if len(rows) > MaxManagedSkillCatalogEntries {
		return nil, ErrSkillCatalogLimit
	}
	out := make([]Skill, 0, len(rows))
	for _, row := range rows {
		snapshot, err := s.loadIdentity(ctx, row)
		if errors.Is(err, errCurrentSkillSelectorMissing) {
			// A missing selector is fail-closed for this identity but must not
			// make one interrupted cleanup take down the whole catalog.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("skills: load Home identity %q: %w", row.ID, err)
		}
		if visible == nil || visible(snapshot.Skill) {
			out = append(out, snapshot.Skill)
		}
	}
	return out, nil
}

func (s *POSIXStore) ListActiveReflectOwnedUserAgentSkills(ctx context.Context, userID, agentID string) ([]Skill, error) {
	rows, err := s.q.ListSkillsByScope(ctx, sqlc.ListSkillsByScopeParams{
		Scope: "user_agent", UserID: pgtype.Text{String: userID, Valid: userID != ""}, AgentID: pgtype.Text{String: agentID, Valid: agentID != ""},
	})
	if err != nil {
		return nil, err
	}
	identities, err := identitiesFromRows(rows)
	if err != nil {
		return nil, err
	}
	loaded, err := s.loadRows(ctx, identities, nil)
	if err != nil {
		return nil, err
	}
	out := loaded[:0]
	for _, skill := range loaded {
		if skill.Status == SkillStatusActive && IsReflectOwned(skill) {
			out = append(out, skill)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func snapshotPaths(snapshot managedSnapshot) []string {
	paths := make([]string, 0, len(snapshot.Files))
	for _, file := range snapshot.Files {
		paths = append(paths, file.Path)
	}
	return paths
}

func mergeRevisionFiles(before []revisionFile, upserts map[string]string, deletions []string) ([]revisionFile, error) {
	files := make(map[string]revisionFile, len(before)+len(upserts))
	for _, file := range before {
		files[file.Path] = revisionFile{Path: file.Path, Mode: file.Mode, Content: append([]byte(nil), file.Content...)}
	}
	for _, filename := range deletions {
		if err := validateSkillPath(filename); err != nil {
			return nil, err
		}
		if filename == MainFile {
			return nil, errors.New("skills: cannot delete SKILL.md")
		}
		delete(files, filename)
	}
	for filename, content := range upserts {
		mode := fs.FileMode(0o644)
		if existing, ok := files[filename]; ok {
			mode = existing.Mode
		}
		files[filename] = revisionFile{Path: filename, Mode: mode, Content: []byte(content)}
	}
	out := make([]revisionFile, 0, len(files))
	for _, file := range files {
		out = append(out, file)
	}
	return validateRevisionFiles(out)
}

func revisionFilesFromStrings(source map[string]string) ([]revisionFile, error) {
	files := make([]revisionFile, 0, len(source))
	for path, content := range source {
		files = append(files, revisionFile{Path: path, Mode: 0o644, Content: []byte(content)})
	}
	return validateRevisionFiles(files)
}

func desiredRevisionDigest(skill Skill, files []revisionFile) (string, error) {
	manifest, err := canonicalManifest(skill)
	if err != nil {
		return "", err
	}
	return digestRevision(manifest, files)
}

func (s *POSIXStore) reconcilePublished(identity Skill, digest string) (managedSnapshot, error) {
	ctx, cancel := freshSkillContext()
	defer cancel()
	root, err := s.openSkillRoot(ctx, identity, home.RootReadWrite)
	if err != nil {
		return managedSnapshot{}, errors.Join(err, home.ErrOutcomeUnknown)
	}
	parent := path.Join(managedRevisionRoot, identity.ID)
	for _, directory := range []string{parent, managedRevisionRoot, "."} {
		if err := root.SyncDirectory(ctx, directory); err != nil {
			_ = root.Close()
			return managedSnapshot{}, errors.Join(err, home.ErrOutcomeUnknown)
		}
	}
	snapshot, readErr := readCurrentSnapshot(ctx, root, identity)
	closeErr := root.Close()
	if readErr != nil || closeErr != nil || snapshot.Skill.ContentDigest != digest {
		return managedSnapshot{}, errors.Join(readErr, closeErr, home.ErrOutcomeUnknown)
	}
	return snapshot, nil
}

func (s *POSIXStore) publish(ctx context.Context, desired Skill, files []revisionFile, expected string, create bool) (managedSnapshot, error) {
	digest, err := desiredRevisionDigest(desired, files)
	if err != nil {
		return managedSnapshot{}, err
	}
	root, err := s.openSkillRoot(ctx, desired, home.RootReadWrite)
	if err != nil {
		return managedSnapshot{}, err
	}
	snapshot, publishErr := publishRevision(ctx, root, desired, files, expected, create, s.random)
	closeErr := root.Close()
	if publishErr == nil && closeErr == nil {
		return snapshot, nil
	}
	combined := errors.Join(publishErr, closeErr)
	// A failed close after successful publication is just as uncertain as an
	// explicit rename acknowledgement failure. Reconcile only the exact desired
	// digest through a fresh capability; never retry the mutation itself.
	if publishErr == nil || home.IsOutcomeUnknown(publishErr) {
		reconciled, reconcileErr := s.reconcilePublished(desired, digest)
		if reconcileErr == nil {
			return reconciled, nil
		}
		return managedSnapshot{}, errors.Join(home.ErrOutcomeUnknown, combined, reconcileErr)
	}
	return managedSnapshot{}, combined
}

func (s *POSIXStore) removeSelection(ctx context.Context, identity Skill, expected string) error {
	if !validSkillDigest(expected) {
		return ErrSkillDigestRequired
	}
	root, err := s.openSkillRoot(ctx, identity, home.RootReadWrite)
	if err != nil {
		return err
	}
	current, err := readCurrentSnapshot(ctx, root, identity)
	if errors.Is(err, errCurrentSkillSelectorMissing) {
		syncErr := root.SyncDirectory(ctx, ".")
		_, verifyErr := readRevisionSnapshot(ctx, root, identity, expected)
		return errors.Join(syncErr, verifyErr, root.Close())
	}
	if err != nil || current.Skill.ContentDigest != expected {
		return errors.Join(err, ErrSkillDigestConflict, root.Close())
	}
	removeErr := root.Remove(ctx, identity.ID, home.RemoveOptions{})
	if removeErr == nil {
		removeErr = root.SyncDirectory(ctx, ".")
	}
	closeErr := root.Close()
	if removeErr == nil && closeErr == nil {
		return nil
	}
	checkCtx, cancel := freshSkillContext()
	defer cancel()
	checkRoot, openErr := s.openSkillRoot(checkCtx, identity, home.RootReadWrite)
	if openErr == nil {
		syncErr := checkRoot.SyncDirectory(checkCtx, ".")
		_, readErr := checkRoot.Readlink(checkCtx, identity.ID)
		checkClose := checkRoot.Close()
		if syncErr == nil && errors.Is(readErr, fs.ErrNotExist) && checkClose == nil {
			return nil
		}
		openErr = errors.Join(syncErr, readErr, checkClose)
	}
	return errors.Join(home.ErrOutcomeUnknown, removeErr, closeErr, openErr)
}

func createIdentityParams(skill Skill) sqlc.CreateSkillParams {
	params := sqlc.CreateSkillParams{ID: skill.ID, Scope: skill.Scope, Name: skill.Name, Description: "", Status: SkillStatusActive, Metadata: json.RawMessage(`{}`)}
	switch skill.Scope {
	case "user":
		params.UserID = pgtype.Text{String: skill.UserID, Valid: true}
	case "user_agent":
		params.UserID = pgtype.Text{String: skill.UserID, Valid: true}
		params.AgentID = pgtype.Text{String: skill.AgentID, Valid: true}
	case "system_agent":
		params.AgentID = pgtype.Text{String: skill.AgentID, Valid: true}
	}
	return params
}

func (s *POSIXStore) CreateManagedSkill(ctx context.Context, skill Skill, source map[string]string) (snapshot SkillSnapshot, resultErr error) {
	release, err := s.lockManagedMutations(ctx)
	if err != nil {
		return SkillSnapshot{}, err
	}
	defer finishManagedMutation(release, &resultErr)
	if skill.ID == "" {
		skill.ID = uuid.NewString()[:8]
	}
	if !validInventoryComponent(skill.ID) || !validInventoryComponent(skill.Name) {
		return SkillSnapshot{}, fmt.Errorf("%w: invalid identity", ErrInvalidSkillRevision)
	}
	files, err := revisionFilesFromStrings(source)
	if err != nil {
		return SkillSnapshot{}, err
	}
	metadata, err := MarkManualOwnedMetadata(skill.Metadata)
	if err != nil {
		return SkillSnapshot{}, err
	}
	now := s.now().UTC()
	skill.Metadata, skill.Status, skill.Version = metadata, SkillStatusActive, 1
	skill.CreatedAt, skill.UpdatedAt = now, now
	published, err := s.publish(ctx, skill, files, "", true)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if _, err := s.q.CreateSkill(ctx, createIdentityParams(skill)); err != nil {
		checkCtx, cancel := freshSkillContext()
		defer cancel()
		row, readErr := s.q.GetSkillByID(checkCtx, skill.ID)
		if readErr == nil && sameSkillIdentity(identityFromRow(row), skill) {
			return SkillSnapshot{Skill: published.Skill, Files: snapshotPaths(published)}, nil
		}
		if readErr != nil && !errors.Is(readErr, pgx.ErrNoRows) {
			return SkillSnapshot{}, fmt.Errorf("%w: reconcile Skill identity registration: %w", home.ErrOutcomeUnknown, errors.Join(err, readErr))
		}
		removeErr := s.removeSelection(checkCtx, skill, published.Skill.ContentDigest)
		return SkillSnapshot{}, fmt.Errorf("register Skill identity: %w", errors.Join(err, readErr, removeErr))
	}
	return SkillSnapshot{Skill: published.Skill, Files: snapshotPaths(published)}, nil
}

func managedUpdateMatches(expected, current managedSnapshot, in ManagedSkillUpdate) (bool, error) {
	want := expected.Skill
	if in.Patch.Description != nil {
		want.Description = *in.Patch.Description
	}
	if in.Patch.Status != nil {
		want.Status = *in.Patch.Status
	}
	if in.Patch.DisableModelInvocation != nil {
		want.DisableModelInvocation = *in.Patch.DisableModelInvocation
	}
	if len(in.Patch.Metadata) > 0 {
		want.Metadata = append(json.RawMessage(nil), in.Patch.Metadata...)
	}
	metadata, err := managedUpdateMetadata(want.Metadata, CreatedBy(expected.Skill), in.ConvertToManual)
	if err != nil {
		return false, err
	}
	want.Metadata, want.Version = metadata, want.Version+1
	if !sameSkillIdentity(want, current.Skill) || want.Description != current.Skill.Description || want.Status != current.Skill.Status || want.DisableModelInvocation != current.Skill.DisableModelInvocation || want.Version != current.Skill.Version {
		return false, nil
	}
	equal, err := semanticJSONEqual(want.Metadata, current.Skill.Metadata)
	if err != nil || !equal {
		return false, err
	}
	files, err := mergeRevisionFiles(expected.Files, in.Files, in.DeleteFiles)
	if err != nil || len(files) != len(current.Files) {
		return false, err
	}
	for i := range files {
		if files[i].Path != current.Files[i].Path || files[i].Mode != current.Files[i].Mode || !bytes.Equal(files[i].Content, current.Files[i].Content) {
			return false, nil
		}
	}
	return true, nil
}

func digestText(digest string) pgtype.Text {
	return pgtype.Text{String: digest, Valid: validSkillDigest(digest)}
}

func (s *POSIXStore) recordManagedEvidence(ctx context.Context, before, after Skill, convert bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	if convert || !IsReflectOwned(after) {
		err = q.DeleteSkillUsage(ctx, after.ID)
	} else {
		err = q.RefreshSkillUsageOnReflectPatch(ctx, sqlc.RefreshSkillUsageOnReflectPatchParams{
			SkillID: after.ID, UserID: after.UserID, AgentID: after.AgentID, ContentDigest: digestText(after.ContentDigest),
		})
	}
	if err == nil {
		_, err = q.InsertSkillChangelog(ctx, sqlc.InsertSkillChangelogParams{
			SkillID: after.ID, UserID: pgtype.Text{String: after.UserID, Valid: after.UserID != ""}, AgentID: pgtype.Text{String: after.AgentID, Valid: after.AgentID != ""},
			Scope: after.Scope, Action: "patch", VersionBefore: pgtype.Int8{Int64: before.Version, Valid: true}, VersionAfter: after.Version,
			ContentDigest: digestText(after.ContentDigest), Metadata: json.RawMessage(`{}`),
		})
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *POSIXStore) managedEvidenceExists(ctx context.Context, before, after Skill) bool {
	var exists bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM skill_changelog WHERE skill_id=$1 AND action='patch' AND version_before=$2 AND version_after=$3 AND content_digest=$4)`, after.ID, before.Version, after.Version, after.ContentDigest).Scan(&exists)
	return err == nil && exists
}

func (s *POSIXStore) UpdateManagedSkill(ctx context.Context, in ManagedSkillUpdate) (snapshot SkillSnapshot, resultErr error) {
	release, err := s.lockManagedMutations(ctx)
	if err != nil {
		return SkillSnapshot{}, err
	}
	defer finishManagedMutation(release, &resultErr)
	identity, err := s.GetIdentity(ctx, in.ID)
	if err != nil || identity == nil {
		return SkillSnapshot{}, errors.Join(err, pgx.ErrNoRows)
	}
	if identity.Scope != in.Scope || identity.UserID != in.UserID || identity.AgentID != in.AgentID {
		return SkillSnapshot{}, ErrSkillNotMutable
	}
	if !validSkillDigest(in.ExpectedDigest) {
		return SkillSnapshot{}, ErrSkillDigestRequired
	}
	if err := validateSkillFilePaths(in.Files); err != nil {
		return SkillSnapshot{}, err
	}
	before, err := s.loadIdentity(ctx, *identity)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if before.Skill.ContentDigest != in.ExpectedDigest {
		expected, loadErr := s.loadIdentityRevision(ctx, *identity, in.ExpectedDigest)
		applied, matchErr := managedUpdateMatches(expected, before, in)
		if loadErr != nil || matchErr != nil || !applied {
			return SkillSnapshot{}, errors.Join(ErrSkillDigestConflict, loadErr, matchErr)
		}
		if !s.managedEvidenceExists(ctx, expected.Skill, before.Skill) {
			if err := s.recordManagedEvidence(ctx, expected.Skill, before.Skill, in.ConvertToManual); err != nil {
				return SkillSnapshot{}, fmt.Errorf("%w: reconcile Skill evidence: %w", home.ErrOutcomeUnknown, err)
			}
		}
		return SkillSnapshot{Skill: before.Skill, Files: snapshotPaths(before)}, nil
	}
	if before.Skill.Status == SkillStatusDeprecated {
		return SkillSnapshot{}, ErrSkillNotMutable
	}
	if in.ConvertToManual && !IsReflectOwned(before.Skill) {
		return SkillSnapshot{}, ErrSkillNotReflectOwned
	}
	after := before.Skill
	if in.Patch.Description != nil {
		after.Description = *in.Patch.Description
	}
	if in.Patch.Status != nil {
		after.Status = *in.Patch.Status
	}
	if in.Patch.DisableModelInvocation != nil {
		after.DisableModelInvocation = *in.Patch.DisableModelInvocation
	}
	if len(in.Patch.Metadata) > 0 {
		after.Metadata = append(json.RawMessage(nil), in.Patch.Metadata...)
	}
	after.Metadata, err = managedUpdateMetadata(after.Metadata, CreatedBy(before.Skill), in.ConvertToManual)
	if err != nil {
		return SkillSnapshot{}, err
	}
	after.Version++
	after.UpdatedAt = s.now().UTC()
	files, err := mergeRevisionFiles(before.Files, in.Files, in.DeleteFiles)
	if err != nil {
		return SkillSnapshot{}, err
	}
	published, err := s.publish(ctx, after, files, in.ExpectedDigest, false)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if err := s.recordManagedEvidence(ctx, before.Skill, published.Skill, in.ConvertToManual); err != nil {
		return SkillSnapshot{Skill: published.Skill, Files: snapshotPaths(published)}, fmt.Errorf("%w: record Skill evidence: %w", home.ErrOutcomeUnknown, err)
	}
	return SkillSnapshot{Skill: published.Skill, Files: snapshotPaths(published)}, nil
}

func (s *POSIXStore) deleteIdentity(ctx context.Context, identity Skill) error {
	agentID, userID := viewSQLParams(ViewContext{UserID: identity.UserID, AgentID: identity.AgentID})
	deleteErr := s.q.DeleteSkill(ctx, sqlc.DeleteSkillParams{ID: identity.ID, AgentID: agentID, UserID: userID})
	checkCtx, cancel := freshSkillContext()
	defer cancel()
	_, readErr := s.q.GetSkillByID(checkCtx, identity.ID)
	if errors.Is(readErr, pgx.ErrNoRows) {
		return nil
	}
	return errors.Join(home.ErrOutcomeUnknown, deleteErr, readErr)
}

func (s *POSIXStore) loadManagedDeleteSnapshot(ctx context.Context, identity Skill, expected string) (managedSnapshot, error) {
	if !validSkillDigest(expected) {
		return managedSnapshot{}, ErrSkillDigestRequired
	}
	before, err := s.loadIdentity(ctx, identity)
	if err != nil {
		return managedSnapshot{}, err
	}
	if before.Skill.ContentDigest != expected {
		return managedSnapshot{}, ErrSkillDigestConflict
	}
	return before, nil
}

func (s *POSIXStore) cleanupDeletedSelection(identity Skill, expected string) error {
	cleanupCtx, cancel := freshSkillContext()
	defer cancel()
	if err := s.removeSelection(cleanupCtx, identity, expected); err != nil {
		return fmt.Errorf("%w: clean deleted Skill selector: %w", home.ErrOutcomeUnknown, err)
	}
	return nil
}

func (s *POSIXStore) DeleteManagedSkill(ctx context.Context, in ManagedSkillDelete) (resultErr error) {
	release, err := s.lockManagedMutations(ctx)
	if err != nil {
		return err
	}
	defer finishManagedMutation(release, &resultErr)
	identity, err := s.GetIdentity(ctx, in.ID)
	if err != nil || identity == nil {
		return errors.Join(err, pgx.ErrNoRows)
	}
	if identity.Scope != in.Scope || identity.UserID != in.UserID || identity.AgentID != in.AgentID {
		return ErrSkillNotMutable
	}
	before, err := s.loadManagedDeleteSnapshot(ctx, *identity, in.ExpectedDigest)
	if err != nil {
		return err
	}
	// PostgreSQL identity is the catalog authority. Remove it before the POSIX
	// selector so a crash can leave only unreachable cleanup, never a live row
	// whose missing selector fails every catalog read.
	if err := s.deleteIdentity(ctx, before.Skill); err != nil {
		return fmt.Errorf("%w: delete Skill identity: %w", home.ErrOutcomeUnknown, err)
	}
	return s.cleanupDeletedSelection(before.Skill, in.ExpectedDigest)
}

func (s *POSIXStore) DeleteManagedSkillFile(ctx context.Context, in ManagedSkillFileDelete) (SkillSnapshot, error) {
	return s.UpdateManagedSkill(ctx, ManagedSkillUpdate{
		ID: in.ID, UserID: in.UserID, AgentID: in.AgentID, Scope: in.Scope,
		ExpectedDigest: in.ExpectedDigest, DeleteFiles: []string{in.Path},
	})
}

func (s *POSIXStore) ListSkillChangelogBySkill(ctx context.Context, skillID string, limit int) ([]SkillChangelog, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.q.ListSkillChangelogBySkill(ctx, sqlc.ListSkillChangelogBySkillParams{SkillID: skillID, LimitCount: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]SkillChangelog, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapChangelogRow(row))
	}
	return out, nil
}

func viewSQLParams(vc ViewContext) (pgtype.Text, pgtype.Text) {
	return pgtype.Text{String: vc.AgentID, Valid: vc.AgentID != ""}, pgtype.Text{String: vc.UserID, Valid: vc.UserID != ""}
}

func mapChangelogRow(row sqlc.SkillChangelog) SkillChangelog {
	return SkillChangelog{
		ID:            row.ID,
		SkillID:       row.SkillID,
		UserID:        row.UserID.String,
		AgentID:       row.AgentID.String,
		Scope:         row.Scope,
		Action:        row.Action,
		VersionBefore: row.VersionBefore.Int64,
		VersionAfter:  row.VersionAfter,
		ContentDigest: row.ContentDigest.String,
		Metadata:      row.Metadata,
		CreatedAt:     row.CreatedAt.UTC(),
	}
}

var (
	_ IdentityReader = (*POSIXStore)(nil)
	_ ManagedDeleter = (*POSIXStore)(nil)
)
