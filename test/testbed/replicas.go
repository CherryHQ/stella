package testbed

import (
	"context"
	"fmt"
	"maps"
	"sync"
)

// ReplicaSet manages N stellad processes sharing one PostgreSQL — the
// multi-replica test shape. The first instance owns the embedded cluster;
// siblings attach via DatabaseURL and share the vault key so secrets decrypt
// on every replica.
type ReplicaSet struct {
	mu        sync.Mutex
	instances []*Instance
}

// StartReplicas boots n instances on one database. Instance 0 runs the
// embedded cluster; 1..n-1 point at its DSN. opts apply to all; PerReplicaEnv
// overrides per index.
func StartReplicas(ctx context.Context, n int, opts Options, perReplicaEnv ...map[string]string) (*ReplicaSet, error) {
	if n < 1 {
		return nil, fmt.Errorf("replica count must be >= 1")
	}
	rs := &ReplicaSet{}
	first, err := Start(ctx, opts)
	if err != nil {
		return nil, err
	}
	rs.instances = append(rs.instances, first)
	vaultKey := first.vaultKey
	for i := 1; i < n; i++ {
		o := opts
		o.Port = 0
		o.Bootstrap = false
		o.Managed = false
		o.DatabaseURL = first.DatabaseURL()
		o.VaultKey = vaultKey
		o.OmitVaultKey = false
		o.FakeModel = false
		if i < len(perReplicaEnv) {
			o.ExtraEnv = mergeEnv(opts.ExtraEnv, perReplicaEnv[i])
		}
		inst, err := Start(ctx, o)
		if err != nil {
			_ = rs.Stop()
			return nil, fmt.Errorf("start replica %d: %w", i, err)
		}
		rs.instances = append(rs.instances, inst)
	}
	return rs, nil
}

func mergeEnv(base, extra map[string]string) map[string]string {
	out := map[string]string{}
	maps.Copy(out, base)
	maps.Copy(out, extra)
	return out
}

// Instance returns the i-th replica (0 = the database owner).
func (r *ReplicaSet) Instance(i int) *Instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.instances[i]
}

// Len returns the replica count.
func (r *ReplicaSet) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.instances)
}

// Stop kills every replica and releases the shared database last.
func (r *ReplicaSet) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var firstErr error
	// Stop siblings first, then the DB owner — a killed sibling must not see
	// the database vanish beneath it before its own shutdown completes.
	for i := len(r.instances) - 1; i >= 0; i-- {
		if err := r.instances[i].Stop(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	r.instances = nil
	return firstErr
}
