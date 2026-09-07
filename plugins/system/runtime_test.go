package system

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin/manifest"
)

func TestRuntimeResourcesMatchEverySystemPlugin(t *testing.T) {
	resources := RuntimeResources()
	catalog, err := manifest.LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	wantCount := 0
	for _, definition := range catalog.Plugins {
		if definition.Kind != "system" {
			continue
		}
		for _, name := range definition.BundledBinaries {
			wantCount++
			if !slices.ContainsFunc(resources, func(r RuntimeResource) bool { return r.Name == name && r.Embedded }) {
				t.Errorf("system plugin %s is missing embedded CLI %s", definition.ID, name)
			}
		}
		for _, binary := range definition.Binaries {
			wantCount++
			if !slices.ContainsFunc(resources, func(r RuntimeResource) bool {
				return r.Name == binary.Name && r.MiseTool == binary.Tool && r.Version == binary.Version && !r.Embedded
			}) {
				t.Errorf("system plugin %s is missing reconciled CLI %s", definition.ID, binary.Name)
			}
		}
	}
	if len(resources) != wantCount || wantCount == 0 {
		t.Fatalf("runtime resource count = %d, want %d from system plugins", len(resources), wantCount)
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
