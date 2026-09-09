package sandbox

import (
	"context"
	"maps"
	"testing"
)

type renderEnvSession struct {
	nopSession
	rendered bool
}

func (s *renderEnvSession) RenderEnv(_ context.Context, env map[string]string) (map[string]string, error) {
	s.rendered = true
	copy := map[string]string{}
	maps.Copy(copy, env)
	copy["RENDERED"] = "yes"
	return copy, nil
}

func TestRenderEnvUsesRawRenderer(t *testing.T) {
	s := &renderEnvSession{}
	got, err := RenderEnv(context.Background(), s, map[string]string{"CURRENT": "yes"})
	if err != nil {
		t.Fatalf("RenderEnv: %v", err)
	}
	if !s.rendered || got["RENDERED"] != "yes" {
		t.Fatalf("renderer was not used: rendered=%v env=%v", s.rendered, got)
	}
}

func TestRenderEnvDefensiveCopiesWithoutRenderer(t *testing.T) {
	s := NopSession()
	input := map[string]string{"CURRENT": "yes"}
	got, err := RenderEnv(context.Background(), s, input)
	if err != nil {
		t.Fatalf("RenderEnv: %v", err)
	}
	got["MUTATED"] = "yes"
	if _, ok := input["MUTATED"]; ok {
		t.Fatal("RenderEnv returned caller-owned map")
	}
}
