package channel

import (
	"context"
	"sync"
	"testing"
	"time"

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

func TestGroupPublishCompletionGateDefersAckUntilRelease(t *testing.T) {
	probe := newGroupPublishCompletionProbe()
	gate := newGroupPublishCompletionGate(probe)
	if err := gate.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if err := gate.Ack(context.Background(), pkgchannel.EgressDelivered); err != nil {
		t.Fatalf("capture Ack: %v", err)
	}
	select {
	case <-probe.Done():
		t.Fatal("underlying completion released before durable finalize")
	default:
	}
	if got, ok := gate.capturedOutcome(); !ok || got != pkgchannel.EgressDelivered {
		t.Fatalf("captured outcome = %q, %v; want delivered, true", got, ok)
	}
	if err := ackGroupPublishCompletion(context.Background(), gate, pkgchannel.EgressDelivered); err != nil {
		t.Fatalf("release: %v", err)
	}
	select {
	case <-probe.Done():
	case <-time.After(time.Second):
		t.Fatal("underlying completion was not released")
	}
	if probe.acked != 1 || probe.outcome != pkgchannel.EgressDelivered {
		t.Fatalf("underlying Ack = %d/%q, want 1/delivered", probe.acked, probe.outcome)
	}
}

func TestGroupPublishCompletionGatePreservesCheckAndRejectsConflictingAck(t *testing.T) {
	probe := newGroupPublishCompletionProbe()
	gate := newGroupPublishCompletionGate(probe)
	if err := gate.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if err := gate.Ack(context.Background(), pkgchannel.EgressDiscarded); err != nil {
		t.Fatalf("first Ack: %v", err)
	}
	if err := gate.Ack(context.Background(), pkgchannel.EgressUnknown); err == nil {
		t.Fatal("conflicting Ack unexpectedly succeeded")
	}
	if probe.checks != 1 || probe.acked != 0 {
		t.Fatalf("probe calls = checks %d, acked %d; want 1, 0", probe.checks, probe.acked)
	}
	if got := publishErrorCompletionOutcome(gate); got != pkgchannel.EgressDiscarded {
		t.Fatalf("pre-send error outcome = %q, want discarded", got)
	}
	if got := publishErrorCompletionOutcome(newGroupPublishCompletionGate(nil)); got != pkgchannel.EgressUnknown {
		t.Fatalf("uncaptured error outcome = %q, want unknown", got)
	}
}
