package plugin

import (
	"sync"
	"testing"
)

func TestRegister(t *testing.T) {
	ResetFactories()

	f := Factory{Name: "test-plugin", New: func(cfg map[string]any) (Plugin, error) { return nil, nil }}
	Register(f)

	got := Factories()
	if len(got) != 1 {
		t.Fatalf("expected 1 factory, got %d", len(got))
	}
	if got[0].Name != "test-plugin" {
		t.Fatalf("expected name %q, got %q", "test-plugin", got[0].Name)
	}
}

func TestRegisterMultiple(t *testing.T) {
	ResetFactories()

	names := []string{"alpha", "beta", "gamma"}
	for _, name := range names {
		Register(Factory{Name: name, New: func(cfg map[string]any) (Plugin, error) { return nil, nil }})
	}

	got := Factories()
	if len(got) != len(names) {
		t.Fatalf("expected %d factories, got %d", len(names), len(got))
	}
	for i, name := range names {
		if got[i].Name != name {
			t.Errorf("factory[%d]: expected %q, got %q", i, name, got[i].Name)
		}
	}
}

func TestFactoriesReturnsCopy(t *testing.T) {
	ResetFactories()

	Register(Factory{Name: "original", New: func(cfg map[string]any) (Plugin, error) { return nil, nil }})

	got := Factories()
	got[0] = Factory{Name: "mutated"}

	original := Factories()
	if original[0].Name != "original" {
		t.Fatalf("modifying returned slice affected registry: got %q", original[0].Name)
	}
}

func TestResetFactories(t *testing.T) {
	ResetFactories()

	Register(Factory{Name: "will-be-cleared", New: func(cfg map[string]any) (Plugin, error) { return nil, nil }})
	ResetFactories()

	got := Factories()
	if len(got) != 0 {
		t.Fatalf("expected 0 factories after reset, got %d", len(got))
	}
}

func TestRegisterConcurrent(t *testing.T) {
	ResetFactories()

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			Register(Factory{
				Name: "concurrent-" + string(rune('A'+idx%26)),
				New:  func(cfg map[string]any) (Plugin, error) { return nil, nil },
			})
		}(i)
	}
	wg.Wait()

	got := Factories()
	if len(got) != n {
		t.Fatalf("expected %d factories, got %d", n, len(got))
	}
}
