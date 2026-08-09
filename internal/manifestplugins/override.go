package manifestplugins

import (
	"encoding/json"
	"fmt"
	"maps"
)

// This file owns the shape of a stored customization of a builtin plugin.
//
// An override is *sparse*: it carries only the fields an admin actually
// changed, and the rest keep tracking the definition compiled into the server.
// Storing the resolved plugin instead would freeze the whole definition at the
// version that happened to be running when someone edited one field — the next
// release's better command, args, or description would arrive in the binary and
// be silently discarded by the row.
//
// Granularity is the top-level field. Lists (binaries, skills, session_env) are
// replaced whole, because there is no stable identity to merge their elements
// by; edit one binary and you own that list.
//
// A JSON null means "cleared" rather than "unset", which is what makes emptying
// an optional field expressible at all in a format whose whole point is that
// absent means inherit.

// definition is the customizable half of a ManifestPlugin. ID is the key, Enabled
// is its own column, and Builtin is computed at resolve time — none of them are
// part of what an admin edits, so none of them belong in the stored override.
type definition struct {
	Kind          string               `json:"kind"`
	Name          string               `json:"name"`
	DisplayName   string               `json:"display_name"`
	Description   string               `json:"description"`
	Category      string               `json:"category,omitempty"`
	Essential     bool                 `json:"essential,omitempty"`
	Prompt        string               `json:"prompt,omitempty"`
	Binaries      []ManifestBinary     `json:"binaries,omitempty"`
	Skills        []ManifestSkill      `json:"skills,omitempty"`
	SessionEnvs   []ManifestSessionEnv `json:"session_env,omitempty"`
	OAuthProvider string               `json:"oauth_provider,omitempty"`
}

func definitionOf(p ManifestPlugin) definition {
	// Normalize empty slices to nil so omitempty produces stable JSON whether the
	// source said null or [].
	binaries, skills, envs := p.Binaries, p.Skills, p.SessionEnvs
	if len(binaries) == 0 {
		binaries = nil
	}
	if len(skills) == 0 {
		skills = nil
	}
	if len(envs) == 0 {
		envs = nil
	}
	return definition{
		Kind:          p.Kind,
		Name:          p.Name,
		DisplayName:   p.DisplayName,
		Description:   p.Description,
		Category:      p.Category,
		Essential:     p.Essential,
		Prompt:        p.Prompt,
		Binaries:      binaries,
		Skills:        skills,
		SessionEnvs:   envs,
		OAuthProvider: p.OAuthProvider,
	}
}

// DefinitionJSON serializes a plugin's whole definition. It is what an
// admin-added plugin stores: there is no builtin underneath it to inherit from,
// so the row is the plugin.
func DefinitionJSON(p ManifestPlugin) (string, error) {
	data, err := json.Marshal(definitionOf(p))
	if err != nil {
		return "", fmt.Errorf("marshal plugin definition %q: %w", p.ID, err)
	}
	return string(data), nil
}

func definitionFields(p ManifestPlugin) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(definitionOf(p))
	if err != nil {
		return nil, fmt.Errorf("marshal plugin definition %q: %w", p.ID, err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("read plugin definition %q: %w", p.ID, err)
	}
	return out, nil
}

// OverrideJSON returns the sparse override that turns base into edited, or the
// empty string when the two agree and no row is needed. A field base carries and
// edited does not was cleared, and is recorded as null.
func OverrideJSON(base, edited ManifestPlugin) (string, error) {
	from, err := definitionFields(base)
	if err != nil {
		return "", err
	}
	to, err := definitionFields(edited)
	if err != nil {
		return "", err
	}
	diff := make(map[string]any, len(to))
	for key, want := range to {
		if got, ok := from[key]; !ok || !json.Valid(got) || string(got) != string(want) {
			diff[key] = want
		}
	}
	for key := range from {
		if _, ok := to[key]; !ok {
			diff[key] = nil
		}
	}
	if len(diff) == 0 {
		return "", nil
	}
	data, err := json.Marshal(diff)
	if err != nil {
		return "", fmt.Errorf("marshal plugin override %q: %w", edited.ID, err)
	}
	return string(data), nil
}

// ApplyOverride lays a stored override over the builtin definition. Fields the
// override does not mention keep whatever the running binary ships, which is the
// entire point; a null clears one.
//
// A pre-sparse row holds a full definition. It merges the same way and produces
// the same plugin, except that an optional field the admin had emptied comes
// back from the builtin — an old row cannot distinguish "cleared" from "not
// set", and inheriting is the better of the two readings.
func ApplyOverride(base ManifestPlugin, override string) (ManifestPlugin, error) {
	fields, err := definitionFields(base)
	if err != nil {
		return ManifestPlugin{}, err
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(override), &patch); err != nil {
		return ManifestPlugin{}, fmt.Errorf("read plugin override %q: %w", base.ID, err)
	}
	merged := maps.Clone(fields)
	for key, value := range patch {
		if string(value) == "null" {
			delete(merged, key)
			continue
		}
		merged[key] = value
	}
	data, err := json.Marshal(merged)
	if err != nil {
		return ManifestPlugin{}, fmt.Errorf("marshal merged plugin %q: %w", base.ID, err)
	}
	out := ManifestPlugin{}
	if err := json.Unmarshal(data, &out); err != nil {
		return ManifestPlugin{}, fmt.Errorf("read merged plugin %q: %w", base.ID, err)
	}
	// The three fields a definition deliberately omits are the caller's, not the
	// override's.
	out.ID = base.ID
	out.Enabled = base.Enabled
	out.Builtin = base.Builtin
	return out, nil
}

// PluginFromDefinition reads a whole stored definition as a plugin. Used for
// admin-added plugins, which have no builtin base.
func PluginFromDefinition(id, override string) (ManifestPlugin, error) {
	out := ManifestPlugin{}
	if err := json.Unmarshal([]byte(override), &out); err != nil {
		return ManifestPlugin{}, fmt.Errorf("read plugin definition %q: %w", id, err)
	}
	out.ID = id
	return out, nil
}
