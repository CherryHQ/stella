package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// This file owns the shape of a stored customization of a builtin plugin.
//
// An override is *sparse*: it carries only the fields an admin actually took
// ownership of, and the rest keep tracking the definition compiled into the
// server. Storing the resolved plugin instead would freeze the whole definition
// at the version that happened to be running when someone edited one field — the
// next release's better command, args, or description would arrive in the binary
// and be silently discarded by the row.
//
// Ownership is *recorded, never inferred*. An earlier version of this file
// derived it by diffing the submitted plugin against the builtin, which quietly
// hands a pinned field back the moment a release happens to ship the same value:
// pin binaries to v2, upgrade into a release whose default is also v2, save
// anything, and the field drops out of the row — so the release after that moves
// the "pinned" plugin to v3. A field is owned because someone said so, and stays
// owned until someone says otherwise.
//
// Granularity is the top-level field. Lists (binaries, skills, session_env) are
// replaced whole, because there is no stable identity to merge their elements
// by; edit one binary and you own that list.
//
// A JSON null means "owned and empty" rather than "unset", which is what makes
// emptying an optional field expressible at all in a format whose whole point is
// that absent means inherit.
//
// Rows written before this existed hold a whole definition. That format has an
// exact reading — it owns every field, including the ones its JSON omits, which
// it owns as empty — so it converts to the sparse form losslessly and is
// converted on the first write that touches it. Until then it is read as what it
// is, which is what the marker below is for.

// sparseMarker tags a stored override as a field-level patch. Its absence means
// the row predates the sparse format and carries a whole definition. The name is
// deliberately not a ManifestPlugin field, so it can never collide with one.
const sparseMarker = "$sparse"

// definitionFieldNames is every field the definition carries, in declaration
// order. It is derived from the struct so a new field cannot be added without
// being serialized, resettable, and reportable in the same edit.
var definitionFieldNames = func() []string {
	t := reflect.TypeFor[ManifestPluginDefinition]()
	names := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names = append(names, name)
		}
	}
	return names
}()

// IsOwnableField reports whether name is a definition field an override may own.
func IsOwnableField(name string) bool {
	return slices.Contains(definitionFieldNames, name)
}

// OwnableFields lists every field an override may own, in definition order.
func OwnableFields() []string {
	return slices.Clone(definitionFieldNames)
}

// DefinitionJSON serializes a plugin's whole definition. It is what an
// admin-added plugin stores: there is no builtin underneath it to inherit from,
// so the row is the plugin.
func DefinitionJSON(p ManifestPlugin) (string, error) {
	// Keep kind and essential in full snapshots. They are server-owned for a
	// builtin, but an admin-added plugin has no shipped metadata underneath it.
	stored := struct {
		Kind      string `json:"kind"`
		Essential bool   `json:"essential,omitempty"`
		ManifestPluginDefinition
	}{
		Kind:                     p.Kind,
		Essential:                p.Essential,
		ManifestPluginDefinition: p.ManifestPluginDefinition,
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return "", fmt.Errorf("marshal plugin definition %q: %w", p.ID, err)
	}
	return string(data), nil
}

func definitionFields(p ManifestPlugin) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(p.ManifestPluginDefinition)
	if err != nil {
		return nil, fmt.Errorf("marshal plugin definition %q: %w", p.ID, err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("read plugin definition %q: %w", p.ID, err)
	}
	return out, nil
}

// ownedMap reads a stored override as the set of fields it owns.
//
// A marked row says so directly. An unmarked one predates the sparse format and
// owns its whole definition: the fields its JSON carries, plus the ones it omits,
// which it owns as empty. Writing those out explicitly is the lossless
// conversion — the row keeps meaning exactly what it meant, in a form that can
// now say which field to let go of.
func ownedMap(override string) (map[string]json.RawMessage, error) {
	if override == "" {
		return map[string]json.RawMessage{}, nil
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal([]byte(override), &stored); err != nil {
		return nil, fmt.Errorf("read plugin override: %w", err)
	}
	if marker, ok := stored[sparseMarker]; ok && string(marker) == "true" {
		delete(stored, sparseMarker)
		delete(stored, "kind")
		delete(stored, "essential")
		return stored, nil
	}

	legacy := ManifestPlugin{}
	if err := json.Unmarshal([]byte(override), &legacy); err != nil {
		return nil, fmt.Errorf("read plugin override: %w", err)
	}
	present, err := definitionFields(legacy)
	if err != nil {
		return nil, err
	}
	// A legacy row is a snapshot of the whole resolved plugin, so it carries the
	// server's own kind and essential too — incidentally, never as a decision an
	// admin made. Leaving those out is what stops such a row from pinning a
	// plugin's policy at whatever it was the day someone renamed it.
	owned := make(map[string]json.RawMessage, len(definitionFieldNames))
	for _, name := range definitionFieldNames {
		if value, ok := present[name]; ok {
			owned[name] = value
			continue
		}
		owned[name] = json.RawMessage("null")
	}
	return owned, nil
}

// OwnedFields returns the fields a stored override owns, in definition order, so
// the editor can show what is pinned and offer to let go of one.
func OwnedFields(override string) ([]string, error) {
	owned, err := ownedMap(override)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(owned))
	for _, name := range definitionFieldNames {
		if _, ok := owned[name]; ok {
			out = append(out, name)
		}
	}
	return out, nil
}

func marshalOwned(owned map[string]json.RawMessage) (string, error) {
	if len(owned) == 0 {
		return "", nil
	}
	out := make(map[string]any, len(owned)+1)
	for key, value := range owned {
		out[key] = value
	}
	out[sparseMarker] = true
	data, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal plugin override: %w", err)
	}
	return string(data), nil
}

// SetFields takes ownership of the named fields at edited's values, on top of
// whatever override already owns, and returns the row to store. Fields nobody
// mentions keep their existing ownership: a save says what it changes, not what
// the whole plugin should be.
//
// A field edited to its zero value is owned as null — "I want this empty" is a
// decision, and absent already means "inherit".
func SetFields(override string, edited ManifestPlugin, fields []string) (string, error) {
	owned, err := ownedMap(override)
	if err != nil {
		return "", err
	}
	values, err := definitionFields(edited)
	if err != nil {
		return "", err
	}
	owned = maps.Clone(owned)
	for _, name := range fields {
		if !IsOwnableField(name) {
			return "", fmt.Errorf("plugin %q: %q is not a definition field", edited.ID, name)
		}
		if value, ok := values[name]; ok {
			owned[name] = value
			continue
		}
		owned[name] = json.RawMessage("null")
	}
	return marshalOwned(owned)
}

// ReleaseField hands one field back to the definition the server ships. A legacy
// row is converted first, so releasing one field from it keeps every other field
// pinned exactly where it was.
func ReleaseField(override, field string) (string, error) {
	if !IsOwnableField(field) {
		return "", fmt.Errorf("%q is not a definition field", field)
	}
	owned, err := ownedMap(override)
	if err != nil {
		return "", err
	}
	if _, ok := owned[field]; !ok {
		return "", fmt.Errorf("%w: %s", ErrFieldNotOwned, field)
	}
	delete(owned, field)
	return marshalOwned(owned)
}

// ErrFieldNotOwned reports a reset of a field that was already following the
// shipped definition. Callers turn it into a 404: there is nothing to undo.
var ErrFieldNotOwned = errors.New("field is not overridden")

// ApplyOverride lays a stored override over the builtin definition. Fields the
// override does not own keep whatever the running binary ships, which is the
// entire point; a null owns one as empty.
func ApplyOverride(base ManifestPlugin, override string) (ManifestPlugin, error) {
	fields, err := definitionFields(base)
	if err != nil {
		return ManifestPlugin{}, err
	}
	owned, err := ownedMap(override)
	if err != nil {
		return ManifestPlugin{}, fmt.Errorf("read plugin override %q: %w", base.ID, err)
	}
	merged := maps.Clone(fields)
	for key, value := range owned {
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
	out := base
	out.ManifestPluginDefinition = ManifestPluginDefinition{}
	if err := json.Unmarshal(data, &out.ManifestPluginDefinition); err != nil {
		return ManifestPlugin{}, fmt.Errorf("read merged plugin %q: %w", base.ID, err)
	}
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
