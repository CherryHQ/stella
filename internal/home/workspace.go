// Package home owns the single-replica POSIX workspace layout beneath STELLA_HOME.
package home

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type PrincipalKind string

const (
	UserPrincipal  PrincipalKind = "user"
	GroupPrincipal PrincipalKind = "group"
)

type WorkspaceRequest struct{ UserID, GroupID, AgentID string }

type WorkspaceView struct {
	PrincipalRoot, DataRoot, AgentRoot string
}

// Workspace is the complete control-plane Home boundary: callers may resolve
// physical roots for provider-private wiring and mint typed rooted capabilities.
type Workspace interface {
	WorkspaceView(context.Context, WorkspaceRequest) (WorkspaceView, error)
	RootOpener
}

// Admission is the process-wide durable-storage gate. Shared storage supplies
// a fail-closed implementation; local single-replica storage leaves it nil.
type Admission interface {
	Check(context.Context) error
}

// WorkspaceManager is the sole production materializer of typed workspace roots.
// PostgreSQL remains owner authority; the filesystem is layout and data authority.
type WorkspaceManager struct {
	db         *pgxpool.Pool
	base       string
	rootFD     int
	admission  Admission
	ownerLocks [257]chan struct{}
}

// Close releases the pinned STELLA_HOME descriptor. The manager must not be
// used after Close.
func (m *WorkspaceManager) Close() error { return closeWorkspaceRoot(m.rootFD) }

func NewWorkspaceManager(db *pgxpool.Pool, base string) (*WorkspaceManager, error) {
	return NewWorkspaceManagerWithAdmission(db, base, nil)
}

func NewWorkspaceManagerWithAdmission(db *pgxpool.Pool, base string, admission Admission) (*WorkspaceManager, error) {
	if db == nil || base == "" {
		return nil, errors.New("home: database and STELLA_HOME are required")
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("home: inspect STELLA_HOME: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("home: STELLA_HOME must be a real directory")
	}
	rootFD, err := openWorkspaceRoot(abs)
	if err != nil {
		return nil, err
	}
	m := &WorkspaceManager{db: db, base: abs, rootFD: rootFD, admission: admission}
	for i := range m.ownerLocks {
		m.ownerLocks[i] = make(chan struct{}, 1)
		m.ownerLocks[i] <- struct{}{}
	}
	return m, nil
}

func (m *WorkspaceManager) admit(ctx context.Context) error {
	if m.admission == nil {
		return nil
	}
	if err := m.admission.Check(ctx); err != nil {
		return fmt.Errorf("home: storage admission closed: %w", err)
	}
	return nil
}

func validID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\\`) || filepath.Base(id) != id {
		return errors.New("home: unsafe ID")
	}
	return nil
}

func (m *WorkspaceManager) principalPath(kind PrincipalKind, id string) (string, error) {
	if err := validID(id); err != nil {
		return "", err
	}
	if kind == GroupPrincipal {
		id = "group-" + id
	} else if kind != UserPrincipal {
		return "", errors.New("home: invalid principal kind")
	}
	return filepath.Join(m.base, "users", id), nil
}

func (m *WorkspaceManager) ownerExists(ctx context.Context, kind PrincipalKind, id string) error {
	q := sqlc.New(m.db)
	var err error
	if kind == GroupPrincipal {
		_, err = q.GetGroupStateByID(ctx, id)
	} else {
		_, err = q.GetAuthUser(ctx, id)
	}
	if err != nil {
		return fmt.Errorf("home: live owner required: %w", err)
	}
	return nil
}

func (m *WorkspaceManager) agentExists(ctx context.Context, id string) error {
	_, err := sqlc.New(m.db).GetAgent(ctx, id)
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("home: live Agent required: %w", pgx.ErrNoRows)
	}
	return fmt.Errorf("home: validate durable Agent: %w", err)
}

func (m *WorkspaceManager) WorkspaceView(ctx context.Context, req WorkspaceRequest) (WorkspaceView, error) {
	if err := m.admit(ctx); err != nil {
		return WorkspaceView{}, err
	}
	if err := validID(req.AgentID); err != nil {
		return WorkspaceView{}, err
	}
	kind, id := UserPrincipal, req.UserID
	if req.GroupID != "" {
		kind, id = GroupPrincipal, req.GroupID
	}
	keys := []string{"agent:" + req.AgentID}
	if id != "" {
		keys = append(keys, string(kind)+":"+id)
	}
	unlock, err := m.lock(ctx, keys)
	if err != nil {
		return WorkspaceView{}, err
	}
	defer unlock()
	// Durable owners are authority. Validate all rows while their gates are held,
	// before creating even a shared scaffold.
	if err := m.agentExists(ctx, req.AgentID); err != nil {
		return WorkspaceView{}, err
	}
	if id != "" {
		if err := m.ownerExists(ctx, kind, id); err != nil {
			return WorkspaceView{}, err
		}
	}
	if err := m.ensureChain(".agents", "db-skills"); err != nil {
		return WorkspaceView{}, err
	}
	if err := m.ensureChain("agents", req.AgentID, ".agents", "skills"); err != nil {
		return WorkspaceView{}, err
	}
	v := WorkspaceView{AgentRoot: filepath.Join(m.base, "agents", req.AgentID)}
	if id == "" {
		return v, nil
	}
	principal, err := m.principalPath(kind, id)
	if err != nil {
		return WorkspaceView{}, err
	}
	rel, _ := filepath.Rel(m.base, principal)
	if err := m.ensureChain(strings.Split(rel, string(filepath.Separator))...); err != nil {
		return WorkspaceView{}, err
	}
	if err := m.ensureChain(append(strings.Split(rel, string(filepath.Separator)), "data")...); err != nil {
		return WorkspaceView{}, err
	}
	if err := m.ensureChain(append(strings.Split(rel, string(filepath.Separator)), "agents", req.AgentID, ".agents", "skills")...); err != nil {
		return WorkspaceView{}, err
	}
	v.PrincipalRoot, v.DataRoot, v.AgentRoot = principal, filepath.Join(principal, "data"), filepath.Join(principal, "agents", req.AgentID)
	return v, nil
}

func (m *WorkspaceManager) lock(ctx context.Context, keys []string) (func(), error) {
	indices := make([]int, 0, len(keys))
	seen := make(map[int]struct{}, len(keys))
	for _, key := range keys {
		h := uint32(2166136261)
		for i := range len(key) {
			h = (h ^ uint32(key[i])) * 16777619
		}
		idx := int(h) % len(m.ownerLocks)
		if _, ok := seen[idx]; !ok {
			seen[idx] = struct{}{}
			indices = append(indices, idx)
		}
	}
	sort.Ints(indices)
	locked := make([]chan struct{}, 0, len(indices))
	for _, idx := range indices {
		ch := m.ownerLocks[idx]
		select {
		case <-ch:
			locked = append(locked, ch)
		case <-ctx.Done():
			for i := len(locked) - 1; i >= 0; i-- {
				locked[i] <- struct{}{}
			}
			return nil, ctx.Err()
		}
	}
	return func() {
		for i := len(locked) - 1; i >= 0; i-- {
			locked[i] <- struct{}{}
		}
	}, nil
}

// AgentIDOccupied reserves deterministic global Agent roots. Any entry type is
// occupied and inspection errors fail closed.
func (m *WorkspaceManager) AgentIDOccupied(ctx context.Context, id string) (bool, error) {
	if err := m.admit(ctx); err != nil {
		return true, err
	}
	if err := validID(id); err != nil {
		return true, err
	}
	return m.agentIDOccupied(id)
}

func (m *WorkspaceManager) ownerGate(ctx context.Context, kind OwnerKind, id string) (func(), error) {
	if err := m.admit(ctx); err != nil {
		return nil, err
	}
	return m.lock(ctx, []string{string(kind) + ":" + id})
}
