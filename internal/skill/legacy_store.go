package skill

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	skillReconcileTimeout      = 5 * time.Second
	managedSkillCleanupTimeout = 5 * time.Second
	managedSkillLockRetryDelay = 25 * time.Millisecond
	managedSkillAdvisoryLock   = int64(0x5354454c4c41534b)
)

var ErrManagedSkillsUnavailable = errors.New("managed Skills are unavailable")

type managedSkillLockSession struct {
	lock    func(context.Context) error
	unlock  func(context.Context) (bool, error)
	release func()
	discard func(context.Context) error
}

// LegacySkillStore is the bounded migration adapter for the pre-filesystem
// Skill authority. It is constructed only while the one-way migration may
// still need PostgreSQL rows and the old typed Home revision tree.
type LegacySkillStore struct {
	db                 *pgxpool.Pool
	q                  *sqlc.Queries
	roots              home.SkillRootOpener
	now                func() time.Time
	random             func([]byte) error
	acquireManagedLock func(context.Context) (managedSkillLockSession, error)
	cleanupContext     func() (context.Context, context.CancelFunc)
}

func NewLegacySkillStore(db *pgxpool.Pool, roots home.SkillRootOpener) (*LegacySkillStore, error) {
	if db == nil || roots == nil {
		return nil, errors.New("skills: database and legacy Home roots are required")
	}
	store := &LegacySkillStore{
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

func (s *LegacySkillStore) acquireManagedSkillLockSession(ctx context.Context) (managedSkillLockSession, error) {
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
					conn = candidate
					return fmt.Errorf("%w: try managed Skill lock: %w", home.ErrOutcomeUnknown, err)
				}
				if locked {
					conn = candidate
					return nil
				}
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

// lockManagedMutationsForMigration holds the migration-wide advisory lock
// while the legacy PostgreSQL/Home authority is read or published.
func (s *LegacySkillStore) lockManagedMutationsForMigration(ctx context.Context) (func() error, error) {
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

func (s *LegacySkillStore) openSkillRoot(ctx context.Context, identity Skill, access home.RootAccess) (home.SkillRootOperations, error) {
	request, scope, err := skillRootSelection(identity)
	if err != nil {
		return nil, err
	}
	return s.roots.OpenSkillRoot(ctx, request, scope, access)
}

func (s *LegacySkillStore) openExistingSkillRoot(ctx context.Context, identity Skill) (home.SkillRootOperations, error) {
	request, scope, err := skillRootSelection(identity)
	if err != nil {
		return nil, err
	}
	return s.roots.OpenExistingSkillRoot(ctx, request, scope)
}

func freshSkillContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), skillReconcileTimeout)
}

func (s *LegacySkillStore) loadIdentityForMigration(ctx context.Context, identity Skill) (snapshot managedSnapshot, err error) {
	root, err := s.openExistingSkillRoot(ctx, identity)
	if err != nil {
		return managedSnapshot{}, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return readCurrentSnapshot(ctx, root, identity)
}

func (s *LegacySkillStore) loadIdentityRevisionForMigration(ctx context.Context, identity Skill, digest string) (snapshot managedSnapshot, err error) {
	root, err := s.openExistingSkillRoot(ctx, identity)
	if err != nil {
		return managedSnapshot{}, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return readRevisionSnapshot(ctx, root, identity, digest)
}

func (s *LegacySkillStore) getIdentityForMigration(ctx context.Context, id string) (*Skill, error) {
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

func desiredRevisionDigest(skill Skill, files []revisionFile) (string, error) {
	manifest, err := canonicalManifest(skill)
	if err != nil {
		return "", err
	}
	return digestRevision(manifest, files)
}

func (s *LegacySkillStore) reconcilePublished(identity Skill, digest string) (managedSnapshot, error) {
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

func (s *LegacySkillStore) publish(ctx context.Context, desired Skill, files []revisionFile, expected string, create bool) (managedSnapshot, error) {
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
	if publishErr == nil || home.IsOutcomeUnknown(publishErr) {
		reconciled, reconcileErr := s.reconcilePublished(desired, digest)
		if reconcileErr == nil {
			return reconciled, nil
		}
		return managedSnapshot{}, errors.Join(home.ErrOutcomeUnknown, combined, reconcileErr)
	}
	return managedSnapshot{}, combined
}
