package host

import (
	"encoding/json"
	"fmt"

	"github.com/CherryHQ/stella/internal/plugin"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// BundledRuntimeResource is the trusted declaration for an executable shipped
// with Stella rather than installed by mise. Keeping its definition, binary,
// and skill owner together prevents a bundled runtime from becoming an
// unowned PATH entry or an independently enabled Skill.
type BundledRuntimeResource struct {
	Definition plugin.Definition
	BinaryName string
	SkillName  string
}

// BuiltinBundledRuntimeResources returns release-owned runtime declarations.
// The binary itself is extracted by resources/binaries; this adapter only
// projects its plugin identity into the common catalog and session view.
func BuiltinBundledRuntimeResources() []BundledRuntimeResource {
	return []BundledRuntimeResource{{
		Definition: plugin.Definition{
			ID: "tool/xberg", Namespace: "xberg", DisplayName: "Xberg",
			Backend: plugin.BackendGo, Source: plugin.SourceBuiltin,
			ImplementationKey: "tool/xberg", Spec: json.RawMessage(`{}`),
			DefaultEnabled: true, Revision: 1,
		},
		BinaryName: "xberg",
		SkillName:  "xberg",
	}}
}

// BuiltinBundledRuntimeDefinitions projects bundled runtime declarations for
// the composition root to register in the common catalog.
func BuiltinBundledRuntimeDefinitions() ([]plugin.Definition, error) {
	resources := BuiltinBundledRuntimeResources()
	definitions := make([]plugin.Definition, 0, len(resources))
	for _, resource := range resources {
		if err := resource.Definition.Validate(); err != nil {
			return nil, fmt.Errorf("bundled runtime %q: %w", resource.Definition.ID, err)
		}
		definitions = append(definitions, resource.Definition)
	}
	return definitions, nil
}

func bundledRuntimeResource(pluginID string) (BundledRuntimeResource, bool) {
	for _, resource := range BuiltinBundledRuntimeResources() {
		if resource.Definition.ID == pluginID {
			return resource, true
		}
	}
	return BundledRuntimeResource{}, false
}

func appendBundledRuntimeResources(view *pkgplugins.SessionPluginView, identity pkgplugins.PluginResourceIdentity, resource BundledRuntimeResource) {
	if resource.BinaryName != "" {
		view.BundledBinarySpecs = append(view.BundledBinarySpecs, pkgplugins.PluginBundledBinarySpec{
			PluginResourceIdentity: identity,
			Name:                   resource.BinaryName,
		})
	}
	if resource.SkillName != "" {
		view.SkillSpecs = append(view.SkillSpecs, pkgplugins.PluginSkillSpec{
			PluginResourceIdentity: identity,
			Name:                   resource.SkillName,
		})
	}
}
