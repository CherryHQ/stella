package embedding

import (
	"context"
	"errors"
	"testing"
)

// fakeLocalEmbedder records the mode it was called with and returns canned vectors
// (or a forced count / error) so the local provider can be tested without a sidecar.
type fakeLocalEmbedder struct {
	gotMode Mode
	vec     []float32
	forceN  int // if > 0, return this many vectors regardless of input count
	err     error
}

func (f *fakeLocalEmbedder) EmbedLocal(_ context.Context, mode Mode, texts []string) ([][]float32, error) {
	f.gotMode = mode
	if f.err != nil {
		return nil, f.err
	}
	n := len(texts)
	if f.forceN > 0 {
		n = f.forceN
	}
	out := make([][]float32, n)
	for i := range out {
		out[i] = append([]float32(nil), f.vec...)
	}
	return out, nil
}

func TestLocalProvider_EmbedMapsModeAndStampsSpace(t *testing.T) {
	fe := &fakeLocalEmbedder{vec: make([]float32, 384)}
	p := NewLocalProvider(fe)

	if p.Kind() != KindLocal {
		t.Errorf("Kind = %q, want local", p.Kind())
	}
	if p.Model() != LocalModelID {
		t.Errorf("Model = %q, want %q", p.Model(), LocalModelID)
	}

	res, err := p.Embed(context.Background(), Request{Texts: []string{"a", "b"}, Mode: ModeDocument})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if fe.gotMode != ModeDocument {
		t.Errorf("mode passed through = %q, want document", fe.gotMode)
	}
	if res.Model != LocalModelID {
		t.Errorf("result space = %q, want %q", res.Model, LocalModelID)
	}
	if len(res.Vectors) != 2 {
		t.Errorf("got %d vectors, want 2", len(res.Vectors))
	}
}

func TestLocalProvider_CountMismatchIsTerminal(t *testing.T) {
	fe := &fakeLocalEmbedder{vec: make([]float32, 384), forceN: 1}
	p := NewLocalProvider(fe)

	_, err := p.Embed(context.Background(), Request{Texts: []string{"a", "b"}})
	if err == nil {
		t.Fatal("expected error on count mismatch")
	}
	if !IsTerminal(err) {
		t.Errorf("count mismatch should be Terminal, got %v", err)
	}
}

func TestService_ResolveLocal(t *testing.T) {
	fe := &fakeLocalEmbedder{vec: make([]float32, 384)}
	fs := &fakeSettings{s: Settings{Enabled: true, Provider: "local"}}
	svc := Boot(BootConfig{Settings: fs, LocalEmbedder: fe})

	r, err := svc.resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve local: %v", err)
	}
	if r.space != LocalModelID {
		t.Errorf("local space = %q, want %q", r.space, LocalModelID)
	}
}

func TestService_ResolveLocalWithoutSidecarDisabled(t *testing.T) {
	// Provider=local but no embedder wired => lane disabled, not an error.
	fs := &fakeSettings{s: Settings{Enabled: true, Provider: "local"}}
	svc := Boot(BootConfig{Settings: fs})
	if _, err := svc.resolve(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Errorf("local without sidecar should be ErrDisabled, got %v", err)
	}
}

func TestService_LocalEmbedQueryUsesLocalSpace(t *testing.T) {
	fe := &fakeLocalEmbedder{vec: make([]float32, 384)}
	fe.vec[0] = 1 // non-zero so normalization has something to work with
	fs := &fakeSettings{s: Settings{Enabled: true, Provider: "local"}}
	svc := Boot(BootConfig{Settings: fs, LocalEmbedder: fe})

	_, space, err := svc.EmbedQuery(context.Background(), "hello")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if space != LocalModelID {
		t.Errorf("query space = %q, want %q", space, LocalModelID)
	}
	if fe.gotMode != ModeQuery {
		t.Errorf("query mode = %q, want query", fe.gotMode)
	}
}

func TestService_LocalServesSensitiveRequest(t *testing.T) {
	// A privacy-sensitive request must be served locally (never via an API
	// provider). With a local-only chain it resolves and returns the local space.
	fe := &fakeLocalEmbedder{vec: make([]float32, 384)}
	fs := &fakeSettings{s: Settings{Enabled: true, Provider: "local"}}
	svc := Boot(BootConfig{Settings: fs, LocalEmbedder: fe})

	r, err := svc.resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	res, err := r.chain.Embed(context.Background(), Request{Texts: []string{"secret"}, Mode: ModeDocument, Privacy: PrivacySensitive})
	if err != nil {
		t.Fatalf("sensitive embed: %v", err)
	}
	if res.ProviderName != "stella-ml/e5-small" || res.Model != LocalModelID {
		t.Errorf("sensitive request served by %q in space %q, want local", res.ProviderName, res.Model)
	}
}
