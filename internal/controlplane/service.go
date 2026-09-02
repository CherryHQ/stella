// Package controlplane owns Stella's deployment control-plane resources: LLM
// providers, deployment settings (embedding, CLI-tool registry, OAuth provider
// config), plugins (registered + manifest), and channels.
//
// These resources are administered, not user-owned. Authorization is a single
// admin gate at Begin (see access.go): an Access is minted only for an admin
// authority, so a non-admin actor is denied before any durable read or external
// action — identical to the legacy requireAdmin gate this service replaces. There
// is no per-user ownership and no per-method authorization.
//
// The service also owns the persistence and live-reload orchestration each
// control-plane mutation performs (store writes plus pool/plugin-host hot
// reloads), so the HTTP transport keeps only request decoding and response
// shaping. It never reads request-supplied identity: the caller passes a trusted
// authz.Authority derived from verified session claims.
package controlplane

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/connections"
	modelcatalog "github.com/CherryHQ/stella/internal/model/catalog"
	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"
)

// Service owns the control-plane resources: the persistence and runtime handles a
// control-plane mutation needs to apply and hot-reload changes. Authorization is
// the admin gate in Begin (see access.go); a nil receiver fails closed.
type Service struct {
	store         config.Store
	plugins       *pluginhost.Host
	pools         *agent.PoolManager
	conns         *connections.Service
	log           *slog.Logger
	catalogSyncMu sync.Mutex
	catalogMu     sync.RWMutex
	catalog       *modelcatalog.Catalog
}

// NewService builds the control-plane service from its fully-wired dependencies.
// The composition root constructs it once and shares the same instance behind the
// HTTP endpoints. log defaults to slog.Default() when nil.
func NewService(store config.Store, plugins *pluginhost.Host, pools *agent.PoolManager, conns *connections.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, plugins: plugins, pools: pools, conns: conns, log: log}
}

// SetModelCatalog installs the process-wide catalog snapshot after startup or
// synchronization. Callers may replace it atomically between requests.
func (s *Service) SetModelCatalog(catalog *modelcatalog.Catalog) {
	if s == nil {
		return
	}
	s.catalogMu.Lock()
	s.catalog = catalog
	s.catalogMu.Unlock()
}

func (s *Service) effectiveModelCatalog(ctx context.Context) *modelcatalog.Catalog {
	s.catalogMu.RLock()
	catalog := s.catalog
	s.catalogMu.RUnlock()
	if catalog != nil {
		return catalog
	}
	store, _ := s.store.(modelcatalog.SnapshotStore)
	catalog, _, err := modelcatalog.Load(ctx, store, s.log)
	if err != nil {
		s.log.Warn("failed to load model catalog", "error", err)
		return nil
	}
	return catalog
}

// ---- typed errors ---------------------------------------------------------
//
// The service returns typed domain errors; the transport maps them to HTTP.
// Denials reuse the authz sentinels (authz.ErrForbidden / authz.ErrUnauthenticated)
// so the admin-only contract reads identically to every other domain.

// NotFoundError reports a control-plane resource that does not exist. Msg carries
// the historical per-resource client message so the transport preserves the 404
// body byte-for-byte.
type NotFoundError struct{ Msg string }

func (e *NotFoundError) Error() string { return e.Msg }

// ValidationError reports a rejected control-plane input (the legacy 400 path).
// Msg carries the exact historical client message.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// ForbiddenError reports a control-plane precondition that forbids an operation
// for a reason other than the admin gate (e.g. a resource locked by an
// environment override). It maps to 403 with Msg, distinct from the opaque
// authz.ErrForbidden denial, and is only ever returned after authorization
// succeeds so it never leaks to an unauthorized caller.
type ForbiddenError struct{ Msg string }

func (e *ForbiddenError) Error() string { return e.Msg }

// ConflictError reports a create that collides with an existing resource, mapped
// to 409 by the transport. It is returned only after authorization succeeds.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// UpstreamError reports a failed call to an external control-plane dependency
// (e.g. a provider's model-listing endpoint), mapped to 502 by the transport.
type UpstreamError struct{ Err error }

func (e *UpstreamError) Error() string {
	if e.Err == nil {
		return "upstream service error"
	}
	return e.Err.Error()
}

func (e *UpstreamError) Unwrap() error { return e.Err }

func notFound(msg string) error { return &NotFoundError{Msg: msg} }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// ErrUnavailable is returned when the service itself is not configured (a nil
// receiver); the transport maps it to a fail-closed 503.
var ErrUnavailable = errors.New("controlplane: authorization unavailable")
