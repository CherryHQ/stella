package sandbox

import (
	"context"
	"testing"
)

type mockFactory struct {
	name      string
	available bool
}

func (m *mockFactory) Name() string                                               { return m.name }
func (m *mockFactory) Available() bool                                            { return m.available }
func (m *mockFactory) Supported(_ Policy) error                                   { return nil }
func (m *mockFactory) CreateSession(_ context.Context, _ Policy) (Session, error) { return nil, nil }

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	f := &mockFactory{name: "local", available: true}

	if err := r.Register(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := r.Get("local")
	if got == nil {
		t.Fatal("expected factory, got nil")
	}
	if got.Name() != "local" {
		t.Fatalf("unexpected name: %s", got.Name())
	}
}

func TestRegistryRegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	f := &mockFactory{name: "local"}
	if err := r.Register(f); err != nil {
		t.Fatalf("first register: unexpected error: %v", err)
	}
	if err := r.Register(f); err == nil {
		t.Fatal("expected error on duplicate register")
	}
}

func TestRegistryUnregister(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockFactory{name: "local"}) //nolint
	r.Unregister("local")
	if got := r.Get("local"); got != nil {
		t.Fatal("expected nil after unregister")
	}
	if names := r.List(); len(names) != 0 {
		t.Fatalf("expected empty list, got %v", names)
	}
}

func TestRegistryList(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockFactory{name: "a"}) //nolint
	r.Register(&mockFactory{name: "b"}) //nolint
	names := r.List()
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("unexpected list: %v", names)
	}
}

func TestRegistryAvailableBackends(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockFactory{name: "unavail", available: false}) //nolint
	r.Register(&mockFactory{name: "avail", available: true})    //nolint
	avail := r.AvailableBackends()
	if len(avail) != 1 || avail[0] != "avail" {
		t.Fatalf("unexpected available backends: %v", avail)
	}
}

func TestRegistryGetMissing(t *testing.T) {
	r := NewRegistry()
	if got := r.Get("nonexistent"); got != nil {
		t.Fatal("expected nil for missing factory")
	}
}
