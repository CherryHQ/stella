package main

import (
	"context"
	"fmt"
	"sync"
)

// limits holds the request caps enforced before any model runs. They bound the
// blast radius of a single request and keep a shared sidecar fair.
type limits struct {
	maxEmbedBatch     int   // max texts per /v1/embed call
	maxTextBytes      int   // max UTF-8 bytes per single text
	maxExtractBody    int64 // max bytes for a /v1/extract body
	perTenantInflight int   // max concurrent in-flight requests per tenant per endpoint
}

func defaultLimits() limits {
	return limits{
		maxEmbedBatch:     128,
		maxTextBytes:      32 << 10, // 32 KiB
		maxExtractBody:    32 << 20, // 32 MiB
		perTenantInflight: 4,
	}
}

// lane is a per-endpoint admission controller. Separate lanes for embed and
// extract guarantee one endpoint's load cannot occupy the other's slots (the
// "Tenant-A extract does not stall Tenant-B embed" invariant). Within a lane, a
// per-tenant in-flight cap stops one tenant from holding every global slot.
type lane struct {
	global   chan struct{}
	perCap   int
	mu       sync.Mutex
	inflight map[string]int
}

func newLane(globalSlots, perTenant int) *lane {
	if globalSlots < 1 {
		globalSlots = 1
	}
	if perTenant < 1 {
		perTenant = 1
	}
	return &lane{
		global:   make(chan struct{}, globalSlots),
		perCap:   perTenant,
		inflight: make(map[string]int),
	}
}

// errBusy signals the per-tenant cap is full; the server maps it to 429.
var errBusy = fmt.Errorf("tenant request limit reached")

// acquire admits a request for tenant, or returns errBusy if the tenant is at its
// cap, or the context error if the global lane fills before ctx is done. The
// returned release must be called exactly once.
func (l *lane) acquire(ctx context.Context, tenant string) (release func(), err error) {
	l.mu.Lock()
	if l.inflight[tenant] >= l.perCap {
		l.mu.Unlock()
		return nil, errBusy
	}
	l.inflight[tenant]++
	l.mu.Unlock()

	releaseTenant := func() {
		l.mu.Lock()
		if l.inflight[tenant]--; l.inflight[tenant] <= 0 {
			delete(l.inflight, tenant)
		}
		l.mu.Unlock()
	}

	select {
	case l.global <- struct{}{}:
		return func() {
			<-l.global
			releaseTenant()
		}, nil
	case <-ctx.Done():
		releaseTenant()
		return nil, ctx.Err()
	}
}
