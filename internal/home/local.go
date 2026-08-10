package home

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// NewLocalRegistry reconstructs every local compatibility Store needed by
// durable non-purged Homes and an unfinished legacy registration. Phase 1 maps
// each immutable Store ID to the same STELLA_HOME base; only defaultStore is
// used for genuinely new Home identities.
func NewLocalRegistry(ctx context.Context, db *pgxpool.Pool, defaultStore, base string) (*Registry, error) {
	if db == nil {
		return nil, errors.New("home: database is required")
	}
	ids := map[string]struct{}{defaultStore: {}}
	q := sqlc.New(db)
	referenced, err := q.ListRetainedStorageHomeStoreID(ctx)
	if err != nil {
		return nil, fmt.Errorf("home: list retained local Stores: %w", err)
	}
	for _, id := range referenced {
		ids[id] = struct{}{}
	}
	marker, err := q.GetStorageMigration(ctx, TypedHomeLegacyRegistrationMigration)
	if err == nil && marker.State != "completed" {
		var metadata struct {
			StoreID string `json:"store_id"`
		}
		if err := json.Unmarshal(marker.Metadata, &metadata); err != nil {
			return nil, fmt.Errorf("home: decode pending legacy registration Store: %w", err)
		}
		ids[metadata.StoreID] = struct{}{}
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("home: load legacy registration Store: %w", err)
	}
	stores := make([]Store, 0, len(ids))
	for id := range ids {
		store, err := NewLocalStore(id, base)
		if err != nil {
			return nil, fmt.Errorf("home: reconstruct local Store %q: %w", id, err)
		}
		stores = append(stores, store)
	}
	registry, err := NewRegistry(db, defaultStore, stores...)
	if err != nil {
		return nil, err
	}
	if err := registry.ValidateConfiguredStores(ctx); err != nil {
		return nil, err
	}
	return registry, nil
}

// LocalStore preserves the existing local, none, and Docker-bind filesystem
// layout. Locators are Store-relative compatibility coordinates, never host
// paths; all physical operations consume the persisted locator under os.Root.
type LocalStore struct{ id, base string }

func NewLocalStore(id, base string) (*LocalStore, error) {
	if err := validateStoreID(id); err != nil {
		return nil, fmt.Errorf("home: invalid local store ID: %w", err)
	}
	if base == "" {
		return nil, errors.New("home: local store base is required")
	}
	absolute, err := filepath.Abs(base)
	if err != nil {
		return nil, fmt.Errorf("home: resolve local store base: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("home: canonicalize local store base: %w", err)
	}
	return &LocalStore{id: id, base: canonical}, nil
}

func (s *LocalStore) ID() string { return s.id }

func (s *LocalStore) Allocate(key Key) (string, error) {
	locator, err := s.allocateUnchecked(key)
	if err != nil {
		return "", err
	}
	return locator, s.ValidateLocator(key, locator)
}

func (s *LocalStore) ValidateLocator(key Key, locator string) error {
	if locator == "" || path.IsAbs(locator) || filepath.IsAbs(locator) || filepath.Clean(locator) != filepath.FromSlash(locator) || strings.Contains(locator, `\`) {
		return errors.New("locator must be a clean relative path")
	}
	for part := range strings.SplitSeq(locator, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("locator escapes store root")
		}
	}
	expected, err := s.allocateUnchecked(key)
	if err != nil {
		return err
	}
	if locator != expected {
		return errors.New("locator does not match Home identity")
	}
	return nil
}

func (s *LocalStore) allocateUnchecked(key Key) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	switch key.Kind {
	case PrincipalHome:
		if key.PrincipalKind == GroupPrincipal {
			return path.Join("users", "group-"+key.PrincipalID), nil
		}
		return path.Join("users", key.PrincipalID), nil
	case AgentHome:
		principal, err := s.allocateUnchecked(Principal(key.PrincipalKind, key.PrincipalID))
		if err != nil {
			return "", err
		}
		return path.Join(principal, "agents", key.AgentID), nil
	case SystemSkillRoot:
		return path.Join(".agents", "db-skills"), nil
	case SystemAgentSkillRoot:
		return path.Join("agents", key.AgentID, ".agents", "skills"), nil
	}
	return "", fmt.Errorf("home: unsupported kind %q", key.Kind)
}

func (s *LocalStore) Ensure(_ context.Context, home Record) error {
	root, err := os.OpenRoot(s.base)
	if err != nil {
		return fmt.Errorf("open store root: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(home.Locator, 0o755); err != nil {
		return fmt.Errorf("create home: %w", err)
	}
	return nil
}

// Purge removes only home.Locator. Callers that coordinate durable purge state
// must hold AcquirePurgeLock from immediately before their final DB
// revalidation until Purge returns. Purge is idempotent and checks ctx between
// individual directory operations; cancellation does not imply rollback.
func (s *LocalStore) Purge(ctx context.Context, home Record) error {
	if _, err := s.pathFor(home); err != nil {
		return err
	}
	if err := purgeLocalRelative(ctx, s.base, home.Locator); err != nil {
		return fmt.Errorf("remove home: %w", err)
	}
	return nil
}

// AcquirePurgeLock obtains an OS-backed, process-wide lock whose file is
// outside every Home tree. The returned release function must be called.
func (s *LocalStore) AcquirePurgeLock(ctx context.Context, home Record) (func() error, error) {
	if _, err := s.pathFor(home); err != nil {
		return nil, err
	}
	return acquireLocalPurgeLock(ctx, s.base, home.ID)
}

func splitLocalPath(name string) []string {
	var out []string
	for name != "." && name != "" {
		dir, file := path.Split(name)
		out = append([]string{file}, out...)
		name = path.Clean(dir)
	}
	return out
}

func (s *LocalStore) Attachment(home Record, readOnly bool) sandbox.HomeAttachment {
	return sandbox.HomeAttachment{HomeID: home.ID, StoreID: home.StoreID, Locator: home.Locator, ReadOnly: readOnly}
}

// pathFor is the sole Phase-1 local compatibility projection. It validates the
// persisted relative locator before resolving it beneath this configured root.
func (s *LocalStore) pathFor(home Record) (string, error) {
	if home.StoreID != s.id {
		return "", errors.New("home belongs to another Store")
	}
	if err := s.ValidateLocator(home.Key, home.Locator); err != nil {
		return "", fmt.Errorf("invalid persisted locator: %w", err)
	}
	return filepath.Join(s.base, filepath.FromSlash(home.Locator)), nil
}

// InspectReadyRoot verifies that an exact persisted locator is still the real,
// no-follow directory provisioned for this Home. It never creates directories.
func (s *LocalStore) InspectReadyRoot(home Record) error {
	pin, err := s.PinReadyRoot(home)
	if err != nil {
		return err
	}
	return pin.Close()
}

type localReadyRootPin struct {
	store *LocalStore
	home  Record
	file  *os.File
	info  os.FileInfo
}

func (p *localReadyRootPin) Revalidate() error {
	current, err := openLocalDirNoFollow(p.store.base, p.home.Locator)
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	info, err := current.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(p.info, info) {
		return errors.New("ready home root was replaced")
	}
	return nil
}

func (p *localReadyRootPin) Close() error { return p.file.Close() }

func (s *LocalStore) PinReadyRoot(home Record) (readyRootPin, error) {
	if _, err := s.pathFor(home); err != nil {
		return nil, err
	}
	f, err := openLocalDirNoFollow(s.base, home.Locator)
	if err != nil {
		return nil, fmt.Errorf("inspect ready home root: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat ready home root: %w", err)
	}
	return &localReadyRootPin{store: s, home: home, file: f, info: info}, nil
}

// PrepareWorkspace validates and pins both already-ready roots before adding
// only their nested legacy scaffold. It deliberately cannot recreate a missing
// ready root.
func (s *LocalStore) PrepareWorkspace(principal, agent Record) error {
	if _, _, _, err := s.WorkspacePaths(principal, agent); err != nil {
		return err
	}
	agentRoot, err := openLocalDirNoFollow(s.base, agent.Locator)
	if err != nil {
		return fmt.Errorf("open agent home root: %w", err)
	}
	defer func() { _ = agentRoot.Close() }()
	dirs := []string{
		"data", path.Join("data", ".cache"),
		path.Join("data", ".agents", "skills"),
		path.Join("data", ".agents", "delegates"),
		path.Join("data", "assets"),
	}
	if err := prepareLocalWorkspaceScaffold(s.base, principal.Locator, dirs); err != nil {
		return fmt.Errorf("materialize workspace: %w", err)
	}
	return nil
}

// WorkspacePaths validates and projects an already-prepared workspace without
// filesystem I/O, so it is safe inside the final DB revalidation phase.
func (s *LocalStore) WorkspacePaths(principal, agent Record) (string, string, string, error) {
	principalRoot, err := s.pathFor(principal)
	if err != nil {
		return "", "", "", err
	}
	agentRoot, err := s.pathFor(agent)
	if err != nil {
		return "", "", "", err
	}
	return principalRoot, filepath.Join(principalRoot, "data"), agentRoot, nil
}

// LegacyAgentIDs inspects only the agents child of an already-authoritative
// principal Home. It never derives principals from directory names.
func (s *LocalStore) LegacyAgentIDs(key Key) ([]string, error) {
	if key.Kind != PrincipalHome {
		return nil, errors.New("home: legacy agent parent must be a principal")
	}
	locator, err := s.Allocate(key)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.base)
	if err != nil {
		return nil, fmt.Errorf("open store root: %w", err)
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(path.Join(locator, "agents"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open legacy agent directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("read legacy agent directory: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		ids = append(ids, entry.Name())
	}
	return ids, nil
}
