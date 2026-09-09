package system

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/CherryHQ/stella/resources/binaries"
)

func TestRuntimeResourcesAreIndependentDeclarations(t *testing.T) {
	resources := RuntimeResources()
	if len(resources) != len(binaries.KnownRuntimeNames()) {
		t.Fatal("runtime declaration differs from immutable release command count")
	}
	seen := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		if !slices.Contains(binaries.KnownRuntimeNames(), resource.Name) {
			t.Fatalf("non-release command %q entered core", resource.Name)
		}
		if !resource.Embedded {
			t.Fatalf("non-embedded runtime %q leaked into the immutable core map", resource.Name)
		}
		if _, exists := seen[resource.Name]; exists {
			t.Fatalf("release runtime declaration duplicates %q", resource.Name)
		}
		seen[resource.Name] = struct{}{}
	}
	for _, embedded := range EmbeddedRuntimeResources() {
		if !embedded.Embedded {
			t.Fatalf("non-embedded resource %q returned by EmbeddedRuntimeResources", embedded.Name)
		}
	}
	first := resources[0].Name
	resources[0].Name = "mutated"
	if RuntimeResources()[0].Name != first {
		t.Fatal("runtime declaration must not be mutable through returned slices")
	}
}

func TestVerifyRejectsIncompletePlan(t *testing.T) {
	identity, err := RuntimeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	plan := RuntimePlan{
		Identity:     identity,
		PublicDir:    "/tmp/" + identity,
		PublicBinDir: "/tmp/" + identity,
	}
	if err := Verify(plan); err == nil {
		t.Fatal("Verify accepted a plan without every declared runtime")
	}
}

func TestPrepareCanonicalizesCachedHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires Unix")
	}
	home := t.TempDir()
	physical, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := RuntimeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(physical, ".mise-tools", "public", identity)
	if err := os.MkdirAll(public, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, resource := range RuntimeResources() {
		if err := os.WriteFile(filepath.Join(public, resource.Name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(public, ".stella-shell-env"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	plan, err := Prepare(t.Context(), alias)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PublicDir != public {
		t.Fatalf("public path = %q, want physical path %q", plan.PublicDir, public)
	}
}
