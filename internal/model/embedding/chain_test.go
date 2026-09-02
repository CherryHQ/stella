package embedding

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeProvider is a scripted provider: each Embed call pops the next outcome.
type fakeProvider struct {
	name     string
	kind     Kind
	model    string
	outcomes []error // one per expected call; nil = success
	calls    int
}

func (f *fakeProvider) Name() string  { return f.name }
func (f *fakeProvider) Kind() Kind    { return f.kind }
func (f *fakeProvider) Model() string { return f.model }

func (f *fakeProvider) Embed(_ context.Context, req Request) (Result, error) {
	i := f.calls
	f.calls++
	if i < len(f.outcomes) && f.outcomes[i] != nil {
		return Result{}, f.outcomes[i]
	}
	vecs := make([][]float32, len(req.Texts))
	for j := range vecs {
		vecs[j] = []float32{1, 0, 0}
	}
	return Result{Vectors: vecs}, nil
}

func req() Request { return Request{Texts: []string{"hi"}, Mode: ModeQuery} }

// manualClock lets a test drive breaker timing deterministically.
type manualClock struct{ t time.Time }

func (m *manualClock) now() time.Time          { return m.t }
func (m *manualClock) advance(d time.Duration) { m.t = m.t.Add(d) }

func TestChain_PrimarySuccessNoFallback(t *testing.T) {
	primary := &fakeProvider{name: "api", kind: KindAPI, model: "space-a"}
	local := &fakeProvider{name: "local", kind: KindLocal, model: "space-b"}
	c := NewChain([]Provider{primary, local}, BreakerConfig{}, nil)

	res, err := c.Embed(context.Background(), req())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProviderName != "api" || res.FallbackUsed {
		t.Fatalf("expected primary api, got %q fallback=%v", res.ProviderName, res.FallbackUsed)
	}
	if res.Model != "space-a" {
		t.Fatalf("result must carry primary space key, got %q", res.Model)
	}
	if local.calls != 0 {
		t.Fatalf("fallback should not have been called, calls=%d", local.calls)
	}
}

func TestChain_TransientFailsOverToLocal(t *testing.T) {
	primary := &fakeProvider{name: "api", kind: KindAPI, model: "space-a", outcomes: []error{errors.New("503")}}
	local := &fakeProvider{name: "local", kind: KindLocal, model: "space-b"}
	c := NewChain([]Provider{primary, local}, BreakerConfig{}, nil)

	res, err := c.Embed(context.Background(), req())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProviderName != "local" || !res.FallbackUsed {
		t.Fatalf("expected fallback to local, got %q fallback=%v", res.ProviderName, res.FallbackUsed)
	}
	// The fallback result must advertise the LOCAL space, not the primary's, so
	// the caller queries the matching index.
	if res.Model != "space-b" {
		t.Fatalf("fallback result must carry local space key, got %q", res.Model)
	}
}

func TestChain_LiveCallerTreatsUpstreamDeadlineAsProviderFailure(t *testing.T) {
	primary := &fakeProvider{name: "api", kind: KindAPI, model: "space-a", outcomes: []error{context.DeadlineExceeded}}
	local := &fakeProvider{name: "local", kind: KindLocal, model: "space-b"}
	c := NewChain([]Provider{primary, local}, BreakerConfig{}, nil)

	res, err := c.Embed(context.Background(), req())
	if err != nil {
		t.Fatalf("upstream deadline with live caller returned error: %v", err)
	}
	if res.ProviderName != "local" || !res.FallbackUsed {
		t.Fatalf("upstream deadline must fall back, got provider=%q fallback=%v", res.ProviderName, res.FallbackUsed)
	}
}

type halfOpenCancelProvider struct {
	cancel context.CancelFunc
	calls  int
}

func (*halfOpenCancelProvider) Name() string  { return "api" }
func (*halfOpenCancelProvider) Kind() Kind    { return KindAPI }
func (*halfOpenCancelProvider) Model() string { return "space-a" }

func (p *halfOpenCancelProvider) Embed(ctx context.Context, req Request) (Result, error) {
	p.calls++
	switch p.calls {
	case 1:
		return Result{}, errors.New("503")
	case 2:
		p.cancel()
		return Result{}, ctx.Err()
	default:
		vecs := make([][]float32, len(req.Texts))
		for i := range vecs {
			vecs[i] = []float32{1, 0, 0}
		}
		return Result{Vectors: vecs}, nil
	}
}

func TestChain_CallerCancellationReleasesHalfOpenProbe(t *testing.T) {
	clock := &manualClock{t: time.Unix(1000, 0)}
	primary := &halfOpenCancelProvider{}
	local := &fakeProvider{name: "local", kind: KindLocal, model: "space-b"}
	chain := NewChain([]Provider{primary, local}, BreakerConfig{FailureThreshold: 1, OpenDuration: time.Minute}, clock.now)

	if result, err := chain.Embed(context.Background(), req()); err != nil || result.ProviderName != "local" {
		t.Fatalf("trip breaker result = provider %q, error %v; want local fallback", result.ProviderName, err)
	}
	clock.advance(time.Minute + time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	primary.cancel = cancel
	if _, err := chain.Embed(ctx, req()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled half-open probe error = %v, want context.Canceled", err)
	}
	if local.calls != 1 {
		t.Fatalf("cancelled half-open probe called fallback; local calls = %d, want 1", local.calls)
	}

	if result, err := chain.Embed(context.Background(), req()); err != nil || result.ProviderName != "api" {
		t.Fatalf("next half-open probe result = provider %q, error %v; want API success", result.ProviderName, err)
	}
}

func TestChain_TerminalErrorDoesNotFallback(t *testing.T) {
	primary := &fakeProvider{name: "api", kind: KindAPI, model: "space-a", outcomes: []error{Terminal(errors.New("401 unauthorized"))}}
	local := &fakeProvider{name: "local", kind: KindLocal, model: "space-b"}
	c := NewChain([]Provider{primary, local}, BreakerConfig{}, nil)

	_, err := c.Embed(context.Background(), req())
	if err == nil {
		t.Fatal("expected terminal error to propagate")
	}
	if !IsTerminal(err) {
		t.Fatalf("expected terminal error, got %v", err)
	}
	if local.calls != 0 {
		t.Fatalf("terminal error must not fall back, local calls=%d", local.calls)
	}
}

func TestChain_SensitiveSkipsAPI(t *testing.T) {
	primary := &fakeProvider{name: "api", kind: KindAPI, model: "space-a"}
	local := &fakeProvider{name: "local", kind: KindLocal, model: "space-b"}
	c := NewChain([]Provider{primary, local}, BreakerConfig{}, nil)

	r := req()
	r.Privacy = PrivacySensitive
	res, err := c.Embed(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProviderName != "local" {
		t.Fatalf("sensitive request must skip API, got %q", res.ProviderName)
	}
	if primary.calls != 0 {
		t.Fatalf("API must not be called for sensitive request, calls=%d", primary.calls)
	}
}

func TestChain_SensitiveWithNoLocalFails(t *testing.T) {
	primary := &fakeProvider{name: "api", kind: KindAPI, model: "space-a"}
	c := NewChain([]Provider{primary}, BreakerConfig{}, nil)

	r := req()
	r.Privacy = PrivacySensitive
	_, err := c.Embed(context.Background(), r)
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("expected ErrNoProvider, got %v", err)
	}
}

func TestChain_BreakerOpensThenSkipsAPI(t *testing.T) {
	clk := &manualClock{t: time.Unix(1000, 0)}
	// Primary fails the first 3 calls (threshold), then would succeed — but the
	// breaker should open after the 3rd and route to local without calling it.
	primary := &fakeProvider{name: "api", kind: KindAPI, model: "space-a", outcomes: []error{
		errors.New("503"), errors.New("503"), errors.New("503"),
	}}
	local := &fakeProvider{name: "local", kind: KindLocal, model: "space-b"}
	c := NewChain([]Provider{primary, local}, BreakerConfig{FailureThreshold: 3, OpenDuration: 60 * time.Second}, clk.now)

	for i := range 3 {
		if _, err := c.Embed(context.Background(), req()); err != nil {
			t.Fatalf("call %d: unexpected error %v", i, err)
		}
	}
	callsAfterTrip := primary.calls
	if callsAfterTrip != 3 {
		t.Fatalf("expected 3 primary calls before open, got %d", callsAfterTrip)
	}
	// Breaker now open: next request must skip the API entirely.
	res, err := c.Embed(context.Background(), req())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProviderName != "local" {
		t.Fatalf("open breaker must route to local, got %q", res.ProviderName)
	}
	if primary.calls != callsAfterTrip {
		t.Fatalf("open breaker must not call API, calls went %d -> %d", callsAfterTrip, primary.calls)
	}

	// After the open window a single half-open probe is admitted; the scripted
	// primary now returns success, closing the breaker.
	clk.advance(61 * time.Second)
	res, err = c.Embed(context.Background(), req())
	if err != nil {
		t.Fatalf("unexpected error after window: %v", err)
	}
	if res.ProviderName != "api" {
		t.Fatalf("half-open probe should hit api and succeed, got %q", res.ProviderName)
	}
}
