package channel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type durableCompletionProbe struct {
	mu      sync.Mutex
	ack     pkgchannel.EgressOutcome
	done    chan struct{}
	ackCall chan struct{}
}

type durableCompletionErrorProbe struct {
	done chan struct{}
	err  error
}

func (p *durableCompletionErrorProbe) Check(context.Context) error { return nil }
func (p *durableCompletionErrorProbe) Ack(context.Context, pkgchannel.EgressOutcome) error {
	return p.err
}
func (p *durableCompletionErrorProbe) Done() <-chan struct{} { return p.done }

func newDurableCompletionProbe() *durableCompletionProbe {
	return &durableCompletionProbe{done: make(chan struct{}), ackCall: make(chan struct{}, 1)}
}

func (p *durableCompletionProbe) Check(context.Context) error { return nil }

func (p *durableCompletionProbe) Ack(_ context.Context, outcome pkgchannel.EgressOutcome) error {
	p.mu.Lock()
	p.ack = outcome
	p.mu.Unlock()
	p.ackCall <- struct{}{}
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	return nil
}

func (p *durableCompletionProbe) Done() <-chan struct{} { return p.done }

// The proxy's Ack only captures the reported outcome and returns; the source
// owner terminalizes through ForwardAck after its durable facts settle. The
// underlying completion must not observe anything before that forward.
func TestDurableCompletionProxyAckCapturesWithoutReleasing(t *testing.T) {
	proxy := newDurableCompletionProxy()
	probe := newDurableCompletionProbe()
	proxy.Bind(probe)

	if err := proxy.Ack(context.Background(), pkgchannel.EgressDelivered); err != nil {
		t.Fatalf("Ack capture: %v", err)
	}
	if got, ok := proxy.outcome(); !ok || got != pkgchannel.EgressDelivered {
		t.Fatalf("captured outcome = %q, %v; want delivered, true", got, ok)
	}
	select {
	case <-probe.Done():
		t.Fatal("underlying completion released before ForwardAck")
	default:
	}
	if err := proxy.ForwardAck(context.Background()); err != nil {
		t.Fatalf("ForwardAck: %v", err)
	}
	select {
	case <-probe.ackCall:
	case <-time.After(time.Second):
		t.Fatal("underlying completion was not acknowledged")
	}
	select {
	case <-proxy.Done():
	case <-time.After(time.Second):
		t.Fatal("proxy Done did not close after forward")
	}
}

// Repeating the same outcome is idempotent; a different outcome is a conflict.
func TestDurableCompletionProxyAckRejectsConflictingOutcome(t *testing.T) {
	proxy := newDurableCompletionProxy()
	if err := proxy.Ack(context.Background(), pkgchannel.EgressDelivered); err != nil {
		t.Fatalf("first Ack: %v", err)
	}
	if err := proxy.Ack(context.Background(), pkgchannel.EgressDelivered); err != nil {
		t.Fatalf("repeated identical Ack: %v", err)
	}
	if err := proxy.Ack(context.Background(), pkgchannel.EgressUnknown); err == nil {
		t.Fatal("conflicting Ack unexpectedly succeeded")
	}
	if got, ok := proxy.outcome(); !ok || got != pkgchannel.EgressDelivered {
		t.Fatalf("outcome = %q, %v; want delivered, true", got, ok)
	}
}

func TestDurableCompletionCapturedOutcomeSurvivesCallerCancellation(t *testing.T) {
	for _, outcome := range []pkgchannel.EgressOutcome{
		pkgchannel.EgressDelivered, pkgchannel.EgressFailed, pkgchannel.EgressDiscarded,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			// Both select cases are ready after a normal adapter returns and
			// cancels its context. Either selection must preserve its report.
			for range 100 {
				ctx, cancel := context.WithCancel(t.Context())
				proxy := newDurableCompletionProxy()
				if err := proxy.Ack(ctx, outcome); err != nil {
					cancel()
					t.Fatal(err)
				}
				cancel()
				if got := (&DurableIngress{}).waitProxyAck(ctx, proxy); got != outcome {
					t.Fatalf("outcome after adapter cancellation = %q, want %q", got, outcome)
				}
			}
		})
	}
}

func TestDurableCompletionCancellationWithoutReportStaysUnknown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	proxy := newDurableCompletionProxy()
	if got := (&DurableIngress{}).waitProxyAck(ctx, proxy); got != pkgchannel.EgressUnknown {
		t.Fatalf("outcome without adapter report = %q, want unknown", got)
	}
	if err := proxy.Ack(t.Context(), pkgchannel.EgressDelivered); err == nil {
		t.Fatal("late acknowledgement overwrote cancellation's unknown outcome")
	}
}

func TestDurableCompletionProxyForceAckDowngradesDelivered(t *testing.T) {
	proxy := newDurableCompletionProxy()
	if err := proxy.captureAck(pkgchannel.EgressDelivered); err != nil {
		t.Fatalf("capture delivered: %v", err)
	}
	if err := proxy.forceAck(pkgchannel.EgressUnknown); err != nil {
		t.Fatalf("force unknown: %v", err)
	}
	if got, ok := proxy.outcome(); !ok || got != pkgchannel.EgressUnknown {
		t.Fatalf("outcome = %q, %v; want unknown, true", got, ok)
	}
}

func TestDurableCompletionProxyNilSourceWaitsForForward(t *testing.T) {
	proxy := newDurableCompletionProxy()
	proxy.Bind(nil)
	if err := proxy.Ack(context.Background(), pkgchannel.EgressDelivered); err != nil {
		t.Fatalf("Ack capture: %v", err)
	}
	select {
	case <-proxy.Done():
		t.Fatal("Done closed before ForwardAck")
	default:
	}
	if err := proxy.ForwardAck(context.Background()); err != nil {
		t.Fatalf("ForwardAck: %v", err)
	}
	select {
	case <-proxy.Done():
	case <-time.After(time.Second):
		t.Fatal("Done did not close after ForwardAck with a nil source")
	}
}

func TestDurableCompletionProxyPersistsForwardError(t *testing.T) {
	want := context.DeadlineExceeded
	proxy := newDurableCompletionProxy()
	proxy.Bind(&durableCompletionErrorProbe{done: make(chan struct{}), err: want})
	if err := proxy.captureAck(pkgchannel.EgressDelivered); err != nil {
		t.Fatalf("capture Ack: %v", err)
	}
	if err := proxy.ForwardAck(context.Background()); !errors.Is(err, want) {
		t.Fatalf("ForwardAck error = %v, want %v", err, want)
	}
	if err := proxy.ForwardAck(context.Background()); !errors.Is(err, want) {
		t.Fatalf("repeated ForwardAck error = %v, want %v", err, want)
	}
}

func TestDurableIngressQuiesceWaitsForAcceptedClaims(t *testing.T) {
	ingress := &DurableIngress{}
	if !ingress.beginProcess() {
		t.Fatal("first process should pass the claim boundary")
	}
	ingress.Quiesce()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := ingress.WaitIdle(ctx); err == nil {
		t.Fatal("WaitIdle returned before accepted claim finished")
	}
	ingress.endProcess()
	if err := ingress.WaitIdle(context.Background()); err != nil {
		t.Fatalf("WaitIdle after drain: %v", err)
	}
	if ingress.beginProcess() {
		t.Fatal("quiesced ingress accepted a new claim")
	}
}
