package channel

import (
	"context"
	"sync"
	"testing"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type groupPublishCompletionProbe struct {
	mu      sync.Mutex
	done    chan struct{}
	checks  int
	acked   int
	outcome pkgchannel.EgressOutcome
}

func newGroupPublishCompletionProbe() *groupPublishCompletionProbe {
	return &groupPublishCompletionProbe{done: make(chan struct{})}
}

func (p *groupPublishCompletionProbe) Check(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checks++
	return nil
}

func (p *groupPublishCompletionProbe) Ack(_ context.Context, outcome pkgchannel.EgressOutcome) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acked++
	p.outcome = outcome
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	return nil
}

func (p *groupPublishCompletionProbe) Done() <-chan struct{} { return p.done }

// The publisher reports through the source owner's capture proxy. A pre-send
// discard survives every downgrade; any other unproven boundary forces unknown
// so a delivered capture can never outlive its durable settlement.
func TestDowngradePublishOutcomePreservesExplicitDiscard(t *testing.T) {
	probe := newGroupPublishCompletionProbe()
	proxy := newDurableCompletionProxy()
	proxy.Bind(probe)
	if err := proxy.Ack(context.Background(), pkgchannel.EgressDiscarded); err != nil {
		t.Fatalf("capture discard: %v", err)
	}
	if err := downgradePublishOutcome(proxy); err != nil {
		t.Fatalf("downgrade with explicit discard: %v", err)
	}
	if got, ok := proxy.outcome(); !ok || got != pkgchannel.EgressDiscarded {
		t.Fatalf("outcome = %q, %v; want discarded, true", got, ok)
	}
	if probe.acked != 0 {
		t.Fatalf("underlying Ack calls = %d, want 0 before forward", probe.acked)
	}
}

func TestDowngradePublishOutcomeForcesUnknownAfterDelivered(t *testing.T) {
	probe := newGroupPublishCompletionProbe()
	proxy := newDurableCompletionProxy()
	proxy.Bind(probe)
	if err := proxy.Ack(context.Background(), pkgchannel.EgressDelivered); err != nil {
		t.Fatalf("capture delivered: %v", err)
	}
	if err := downgradePublishOutcome(proxy); err != nil {
		t.Fatalf("downgrade after delivered: %v", err)
	}
	if got, ok := proxy.outcome(); !ok || got != pkgchannel.EgressUnknown {
		t.Fatalf("outcome = %q, %v; want unknown, true", got, ok)
	}
}
