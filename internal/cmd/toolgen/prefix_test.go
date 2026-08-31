package main

import (
	"strings"
	"testing"
)

func TestPrefixedFamiliesCanShareOnePackage(t *testing.T) {
	pkg := domainPackage{Dir: "controlplane", Package: "controlplane", Split: true, PrefixGeneratedSymbols: true}
	decls := []toolDecl{
		{Family: "provider", Action: "get", Name: "provider_get", Description: "Get a Provider.", Package: pkg, SourceLocation: "provider", Schema: objectSchema(nil, nil)},
		{Family: "plugin", Action: "get", Name: "plugin_get", Description: "Get a Plugin.", Package: pkg, SourceLocation: "plugin", Schema: objectSchema(nil, nil)},
	}
	if err := validate(decls); err != nil {
		t.Fatalf("validate prefixed shared package: %v", err)
	}
	for _, decl := range decls {
		got, err := renderTool(decl.Family, pkg, []toolDecl{decl})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{exportName(decl.Family) + "ActionTools", exportName(decl.Family) + "Handler", exportName(decl.Family) + "Dispatch", exportName(decl.Family) + "GetInput"} {
			if !strings.Contains(string(got), want) {
				t.Errorf("%s output missing %s", decl.Family, want)
			}
		}
	}
}

func TestUnprefixedFamiliesCannotShareOnePackage(t *testing.T) {
	pkg := domainPackage{Dir: "shared", Package: "shared", Split: true}
	err := validate([]toolDecl{
		{Family: "one", Action: "get", Name: "one_get", Description: "Get one.", Package: pkg, SourceLocation: "one", Schema: objectSchema(nil, nil)},
		{Family: "two", Action: "get", Name: "two_get", Description: "Get two.", Package: pkg, SourceLocation: "two", Schema: objectSchema(nil, nil)},
	})
	if err == nil || !strings.Contains(err.Error(), "must prefix generated symbols") {
		t.Fatalf("validate unprefixed shared package = %v", err)
	}
}
