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

func TestDurableCompletionProxyAckWaitsForFIFOSettlement(t *testing.T) {
	proxy := newDurableCompletionProxy()
	probe := newDurableCompletionProbe()
	proxy.Bind(probe)

	ackDone := make(chan error, 1)
	go func() { ackDone <- proxy.Ack(context.Background(), pkgchannel.EgressDelivered) }()
	select {
	case err := <-ackDone:
		t.Fatalf("Ack returned before FIFO settlement: %v", err)
	case <-proxy.AckReceived():
	}
	select {
	case err := <-ackDone:
		t.Fatalf("Ack returned before ForwardAck: %v", err)
	default:
	}
	if err := proxy.ForwardAck(context.Background()); err != nil {
		t.Fatalf("ForwardAck: %v", err)
	}
	select {
	case err := <-ackDone:
		if err != nil {
			t.Fatalf("Ack after settlement: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Ack did not wait for settlement")
	}
	select {
	case <-probe.ackCall:
	case <-time.After(time.Second):
		t.Fatal("underlying completion was not acknowledged")
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
	ackDone := make(chan error, 1)
	go func() { ackDone <- proxy.Ack(context.Background(), pkgchannel.EgressDelivered) }()
	select {
	case err := <-ackDone:
		t.Fatalf("Ack returned before FIFO settlement: %v", err)
	case <-proxy.AckReceived():
	}
	if err := proxy.ForwardAck(context.Background()); err != nil {
		t.Fatalf("ForwardAck: %v", err)
	}
	select {
	case err := <-ackDone:
		if err != nil {
			t.Fatalf("Ack after nil-source settlement: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Ack did not return after ForwardAck")
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
	if err := proxy.waitDone(context.Background()); !errors.Is(err, want) {
		t.Fatalf("waitDone error = %v, want %v", err, want)
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
