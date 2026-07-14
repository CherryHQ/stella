package policy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
)

// ErrInvalidAuthority is returned by Begin when the supplied Authority is not
// well-formed. It denies the use case (fail closed), distinct from the
// infrastructure ErrAuthorizerUnavailable.
var ErrInvalidAuthority = errors.New("authz/policy: invalid authority")

// ErrCatalogVersion is the reload failure raised when an active policy row's
// catalog_version does not match the current authz.CatalogVersion. It is wrapped
// into ErrAuthorizerUnavailable at Begin so the use case fails closed.
var ErrCatalogVersion = errors.New("authz/policy: active policy catalog_version mismatch")

// Authorizer is the concrete, revision-verified authz.Authorizer. It caches one
// compiled snapshot and refreshes it only when the authoritative database
// revision advances, so the steady-state cost of a protected use case is a
// single lightweight revision read.
//
// Correctness does not depend on LISTEN/NOTIFY: every Begin reads the revision
// and reloads on a mismatch, so two independent Authorizer instances observe a
// committed mutation on their next Begin without any notification.
type Authorizer struct {
	store *policyStore

	cache atomic.Pointer[snapshot]
	// publishMu serializes snapshot publication so the never-regress check and
	// the store are atomic with respect to each other. Compilation happens
	// outside this lock; only the short publish decision holds it.
	publishMu sync.Mutex

	// instrumentation, safe in production; read by tests to assert that a use
	// case performs exactly one revision read and reloads only on a mismatch.
	revisionReads atomic.Int64
	reloads       atomic.Int64

	// beforePublish, when non-nil, runs inside reload after the consistent DB
	// read + compile but before publication. Test-only hook for interleaving
	// concurrent reloads; nil in production (no sleeps in production).
	beforePublish func()
}

// New builds an Authorizer over a pgx pool.
func New(pool *pgxpool.Pool) *Authorizer {
	return &Authorizer{store: newPolicyStore(pool)}
}

// Begin binds the use case to one immutable, revision-validated Evaluation. It
// validates the Authority, reads the authoritative revision once, reuses the
// cached snapshot when the revision matches (no policy-row reload), and reloads
// synchronously on a mismatch. Any revision/read/reload/compile failure returns
// ErrAuthorizerUnavailable so the caller fails closed.
func (az *Authorizer) Begin(ctx context.Context, authority authz.Authority) (authz.Evaluation, error) {
	if !authority.Valid() {
		return nil, ErrInvalidAuthority
	}

	az.revisionReads.Add(1)
	rev, err := az.store.revision(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", authz.ErrAuthorizerUnavailable, err)
	}

	if snap := az.cache.Load(); snap != nil && snap.revision == rev {
		return &evaluation{authority: authority, snap: snap}, nil
	}

	snap, err := az.reload(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", authz.ErrAuthorizerUnavailable, err)
	}
	return &evaluation{authority: authority, snap: snap}, nil
}

// reload reads a consistent revision + active-row snapshot, compiles an
// immutable policy set, and publishes it without ever regressing the cache. It
// returns the freshly compiled snapshot (bound to the revision it read), which
// is internally consistent even if a concurrent newer reload won the publish.
func (az *Authorizer) reload(ctx context.Context) (*snapshot, error) {
	az.reloads.Add(1)

	rev, rows, err := az.store.loadForReload(ctx)
	if err != nil {
		return nil, err
	}

	policies := builtinPolicies()
	for _, row := range rows {
		// Inactive resources are never custom-policy decision points. Historical
		// rows remain diagnostic data, but cannot affect an evaluation.
		rt, known := parseResourceType(row.ResourceType)
		if known && !resourceAcceptsCustomPolicy(rt) {
			continue
		}
		// An active row must be pinned to the CURRENT catalog version. A lower
		// (stale) or higher (future) version cannot be interpreted under current
		// semantics without silently widening or narrowing access, so it fails the
		// whole reload/Begin as unavailable rather than being evaluated. Quarantined
		// legacy rows (catalog_version 0) never reach here — they are not active.
		if row.CatalogVersion != int64(authz.CatalogVersion) {
			return nil, fmt.Errorf("authz/policy: active policy %s: %w: catalog_version %d != current %d",
				row.ID, ErrCatalogVersion, row.CatalogVersion, authz.CatalogVersion)
		}
		subjects, err := unmarshalSubjects(row.Subjects)
		if err != nil {
			// A malformed/invalid subject selector on an active row must fail the
			// whole Begin: never treat an uninterpretable subject as "any actor".
			return nil, fmt.Errorf("authz/policy: active policy %s: %w", row.ID, err)
		}
		preds, err := unmarshalAttributes(row.Attributes)
		if err != nil {
			return nil, fmt.Errorf("authz/policy: policy %s: %w", row.ID, err)
		}
		cp, err := compileCustom(row.ID, row.ResourceType, row.Action, row.Effect, subjects, preds)
		if err != nil {
			// An active row that fails to compile must fail closed: never drop a
			// deny silently.
			return nil, fmt.Errorf("authz/policy: active policy %s: %w", row.ID, err)
		}
		policies = append(policies, cp)
	}

	snap := &snapshot{revision: rev, policies: policies}

	if az.beforePublish != nil {
		az.beforePublish()
	}
	az.publish(snap)
	return snap, nil
}

// publish stores snap only if it does not regress the cached revision, so
// out-of-order concurrent reloads can never move the cache backward.
func (az *Authorizer) publish(snap *snapshot) {
	az.publishMu.Lock()
	defer az.publishMu.Unlock()
	cur := az.cache.Load()
	if cur == nil || snap.revision > cur.revision {
		az.cache.Store(snap)
	}
}

// cachedRevision returns the currently published revision, or -1 if none.
// White-box test helper.
func (az *Authorizer) cachedRevision() int64 {
	if snap := az.cache.Load(); snap != nil {
		return snap.revision
	}
	return -1
}
