package embedding

import (
	"context"
	"errors"
	"testing"
)

type fakeSettings struct {
	s     Settings
	calls int
}

func (f *fakeSettings) EmbeddingSettings(_ context.Context) (Settings, error) {
	f.calls++
	return f.s, nil
}

func TestService_DisabledReportsNoSpace(t *testing.T) {
	fs := &fakeSettings{s: Settings{Enabled: false}}
	svc := Boot(BootConfig{Settings: fs})

	_, model, err := svc.EmbedQuery(context.Background(), "hello")
	if err != nil {
		t.Fatalf("disabled EmbedQuery should not error, got %v", err)
	}
	if model != "" {
		t.Errorf("disabled lane must report empty space key, got %q", model)
	}

	// Enabled but no key is also treated as disabled (would silently no-op otherwise).
	fs.s = Settings{Enabled: true, Model: "m", APIKey: ""}
	if _, model, _ := svc.EmbedQuery(context.Background(), "hi"); model != "" {
		t.Errorf("keyless lane must report empty space key, got %q", model)
	}
}

func TestService_ResolveCachesAndRebuildsOnConfigChange(t *testing.T) {
	fs := &fakeSettings{s: Settings{Enabled: true, Model: "text-embedding-3-small", Dim: 1536, APIKey: "k"}}
	svc := Boot(BootConfig{Settings: fs})
	ctx := context.Background()

	r1, err := svc.resolve(ctx)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r1.space != "text-embedding-3-small@1536" {
		t.Fatalf("space key = %q, want model@dim", r1.space)
	}

	// Same config: the cached build is reused (no rebuild).
	r2, _ := svc.resolve(ctx)
	if r1 != r2 {
		t.Error("identical config should reuse the cached build")
	}

	// Changing the dimension is a new vector space and forces a rebuild.
	fs.s.Dim = 512
	r3, err := svc.resolve(ctx)
	if err != nil {
		t.Fatalf("resolve after change: %v", err)
	}
	if r3 == r1 {
		t.Error("config change should rebuild, got cached value")
	}
	if r3.space != "text-embedding-3-small@512" {
		t.Errorf("rebuilt space key = %q, want new dim", r3.space)
	}
}

func TestService_ResolveDisabled(t *testing.T) {
	fs := &fakeSettings{s: Settings{Enabled: false}}
	svc := Boot(BootConfig{Settings: fs})
	if _, err := svc.resolve(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Errorf("disabled resolve should return ErrDisabled, got %v", err)
	}
}
