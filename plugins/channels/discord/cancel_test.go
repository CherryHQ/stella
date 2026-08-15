package discord

import "testing"

func TestCancelRegistryUnregisterAlsoRemovesFromOrder(t *testing.T) {
	r := newCancelRegistry()
	token := r.register("requester", func() bool { return true })
	if len(r.order) != 1 {
		t.Fatalf("order = %v, want one entry after register", r.order)
	}
	r.unregister(token)
	if len(r.entries) != 0 {
		t.Fatalf("entries = %v, want empty after unregister", r.entries)
	}
	if len(r.order) != 0 {
		t.Fatalf("order = %v, want empty after unregister, not just entries", r.order)
	}
}

func TestCancelRegistryUnregisterUnknownTokenIsNoop(t *testing.T) {
	r := newCancelRegistry()
	kept := r.register("requester", func() bool { return true })
	r.unregister("does-not-exist")
	if len(r.entries) != 1 || len(r.order) != 1 || r.order[0] != kept {
		t.Fatalf("entries = %v, order = %v; unregistering an unknown token must not disturb the registry", r.entries, r.order)
	}
}

// TestCancelRegistryStaysBoundedUnderRegisterUnregisterChurn simulates the
// common case: most turns resolve (and unregister) long before the registry
// ever reaches cancelRegistryLimit. Before unregister also cleaned order,
// order grew forever across this churn even though entries stayed tiny.
func TestCancelRegistryStaysBoundedUnderRegisterUnregisterChurn(t *testing.T) {
	r := newCancelRegistry()
	const churn = cancelRegistryLimit * 50
	for range churn {
		token := r.register("requester", func() bool { return true })
		r.unregister(token)
	}
	if len(r.entries) != 0 {
		t.Fatalf("entries = %d, want 0 after every registration was unregistered", len(r.entries))
	}
	if len(r.order) != 0 {
		t.Fatalf("order = %d entries after %d register/unregister cycles, want 0 (order must not grow unbounded)", len(r.order), churn)
	}
}

// TestCancelRegistryEvictsOldestWhenNeverUnregistered exercises the FIFO
// eviction backstop directly, independent of the unregister path.
func TestCancelRegistryEvictsOldestWhenNeverUnregistered(t *testing.T) {
	r := newCancelRegistry()
	var first string
	for i := range cancelRegistryLimit + 1 {
		token := r.register("requester", func() bool { return true })
		if i == 0 {
			first = token
		}
	}
	if len(r.entries) != cancelRegistryLimit || len(r.order) != cancelRegistryLimit {
		t.Fatalf("entries = %d, order = %d, want both capped at %d", len(r.entries), len(r.order), cancelRegistryLimit)
	}
	if _, ok := r.get(first); ok {
		t.Fatalf("oldest token should have been evicted once the registry hit its limit")
	}
}
