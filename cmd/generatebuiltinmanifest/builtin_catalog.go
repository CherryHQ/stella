package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

// builtinDefinition is the generated wire format. It deliberately contains
// only the catalog identity and resource JSON; database-owned timestamps and
// creators have no place in immutable release data.
type builtinDefinition struct {
	ID             string          `json:"id"`
	DisplayName    string          `json:"display_name"`
	DefaultEnabled bool            `json:"default_enabled"`
	Revision       int64           `json:"revision"`
	Spec           json.RawMessage `json:"spec"`
}

func generateBuiltinDefinitions(sourceRoot string, reservedRuntimeNames []string, oauthProviderIDs map[string]struct{}) ([]builtinDefinition, error) {
	info, err := os.Lstat(sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("stat builtin plugins root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("builtin plugins root must be a directory: %s", sourceRoot)
	}

	var definitions []builtinDefinition
	err = filepath.WalkDir(sourceRoot, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceRoot, filename)
		if err != nil {
			return fmt.Errorf("relative plugin path %q: %w", filename, err)
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("builtin plugin path %q is a symlink", rel)
		}
		if entry.IsDir() {
			if path.Base(rel) == "plugin.yaml" {
				return fmt.Errorf("builtin plugin path %q has unsupported type directory", rel)
			}
			return nil
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat builtin plugin path %q: %w", rel, err)
		}
		if !fileInfo.Mode().IsRegular() {
			return fmt.Errorf("builtin plugin path %q has unsupported type %s", rel, fileInfo.Mode().Type())
		}
		switch path.Base(rel) {
		case "plugin.yaml", "assets.yaml":
			parts := strings.Split(rel, "/")
			if len(parts) > 3 && parts[0] == "agent" {
				return nil
			}
			return fmt.Errorf("unsupported legacy authoring file %q; use plugin.json", rel)
		case "plugin.json":
			parts := strings.Split(rel, "/")
			if len(parts) != 3 || parts[0] != "agent" || parts[2] != "plugin.json" {
				return nil
			}
			definition, err := loadAgentDefinition(filepath.Dir(filename), rel)
			if err != nil {
				return err
			}
			definitions = append(definitions, definition)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(definitions) == 0 {
		return nil, fmt.Errorf("builtin plugins root contains no plugin.json: %s", sourceRoot)
	}
	if err := validateBuiltinDefinitions(definitions, reservedRuntimeNames, oauthProviderIDs); err != nil {
		return nil, err
	}
	slices.SortFunc(definitions, func(left, right builtinDefinition) int { return cmp.Compare(left.ID, right.ID) })
	return definitions, nil
}

func loadAgentDefinition(root, relative string) (builtinDefinition, error) {
	diagnostics := agentpackage.ValidateAuthoring(root)
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == agentpackage.SeverityError {
			return builtinDefinition{}, fmt.Errorf("validate Agent package %q: %s", relative, diagnostic.Message)
		}
	}
	pkg, diagnostics := agentpackage.Load(root)
	if pkg == nil {
		return builtinDefinition{}, fmt.Errorf("load Agent package %q: %v", relative, diagnostics)
	}
	if filepath.Base(root) != pkg.Manifest.Name {
		return builtinDefinition{}, fmt.Errorf("agent package %q directory must match manifest name %q", relative, pkg.Manifest.Name)
	}
	for _, legacy := range []string{"assets.yaml", "plugin.yaml"} {
		if _, err := os.Stat(filepath.Join(root, legacy)); err == nil {
			return builtinDefinition{}, fmt.Errorf("agent package %q cannot also contain %s", relative, legacy)
		} else if !errors.Is(err, os.ErrNotExist) {
			return builtinDefinition{}, fmt.Errorf("stat Agent package %q %s: %w", relative, legacy, err)
		}
	}

	displayName := pkg.Manifest.Name
	if pkg.Extension != nil && pkg.Extension.DisplayName != "" {
		displayName = pkg.Extension.DisplayName
	}
	payload, err := plugin.ResourcePayloadFromAgentPackage(pkg)
	if err != nil {
		return builtinDefinition{}, fmt.Errorf("convert Agent package %q resources: %w", relative, err)
	}
	assetsDigest, err := agentpackage.DirectoryDigest(root)
	if err != nil {
		return builtinDefinition{}, fmt.Errorf("digest Agent package %q assets: %w", relative, err)
	}
	payload.Content = &plugin.ContentReference{Digest: assetsDigest}
	spec, err := json.Marshal(payload)
	if err != nil {
		return builtinDefinition{}, fmt.Errorf("encode Agent package %q resources: %w", relative, err)
	}
	spec, err = plugin.PublishDefinitionSpec(spec)
	if err != nil {
		return builtinDefinition{}, fmt.Errorf("publish Agent package %q definition: %w", relative, err)
	}
	return builtinDefinition{
		ID: pkg.Manifest.Name, DisplayName: displayName, DefaultEnabled: true, Revision: 1, Spec: spec,
	}, nil
}

func validateBuiltinDefinitions(definitions []builtinDefinition, reservedRuntimeNames []string, oauthProviderIDs map[string]struct{}) error {
	reserved := make(map[string]struct{}, len(reservedRuntimeNames))
	for _, name := range reservedRuntimeNames {
		reserved[name] = struct{}{}
	}
	seenIDs := make(map[string]struct{}, len(definitions))
	seenResources := make(map[string]string)
	seenBinaries := make(map[string]plugin.BinaryResource)
	for _, definition := range definitions {
		if _, exists := seenIDs[definition.ID]; exists {
			return fmt.Errorf("duplicate builtin plugin ID %q", definition.ID)
		}
		seenIDs[definition.ID] = struct{}{}
		if err := plugin.ValidateName(definition.ID); err != nil || definition.DisplayName == "" {
			return fmt.Errorf("builtin plugin %q has invalid name or display_name", definition.ID)
		}
		if definition.Revision < 1 {
			return fmt.Errorf("builtin plugin %q has invalid revision", definition.ID)
		}
		payload, err := plugin.DecodeResourcePayload(definition.Spec, "plugin "+definition.ID)
		if err != nil {
			return err
		}
		if err := plugin.ValidateResourceDeclarations(payload, "plugin "+definition.ID, oauthProviderIDs); err != nil {
			return err
		}
		for _, binary := range payload.Binaries {
			if _, isReserved := reserved[binary.Name]; isReserved {
				return fmt.Errorf("builtin plugin %q uses reserved core binary name %q", definition.ID, binary.Name)
			}
			if previous, exists := seenBinaries[binary.Name]; exists && !reflect.DeepEqual(previous, binary) {
				return fmt.Errorf("conflicting builtin plugin resource binary %q in %q and %q", binary.Name, seenResources["binary:"+binary.Name], definition.ID)
			}
			seenBinaries[binary.Name] = binary
			seenResources["binary:"+binary.Name] = definition.ID
		}
		for _, skill := range payload.Skills {
			if previous, exists := seenResources["skill:"+skill.Name]; exists {
				return fmt.Errorf("duplicate builtin plugin resource skill %q in %q and %q", skill.Name, previous, definition.ID)
			}
			seenResources["skill:"+skill.Name] = definition.ID
		}
		for _, env := range payload.SessionEnvs {
			if previous, exists := seenResources["session_env:"+env.EnvVar]; exists {
				return fmt.Errorf("duplicate builtin plugin resource session env %q in %q and %q", env.EnvVar, previous, definition.ID)
			}
			seenResources["session_env:"+env.EnvVar] = definition.ID
		}
	}
	return nil
}

func parseOAuthProviderIDs(data []byte) (map[string]struct{}, error) {
	providers, err := oauth.ParseProviders(data)
	if err != nil {
		return nil, fmt.Errorf("decode builtin OAuth providers: %w", err)
	}
	ids := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		ids[provider.ID] = struct{}{}
	}
	return ids, nil
}

func renderBuiltinDefinitions(definitions []builtinDefinition) ([]byte, error) {
	data, err := json.Marshal(definitions)
	if err != nil {
		return nil, fmt.Errorf("encode builtin plugin catalog: %w", err)
	}
	return []byte("// Code generated by cmd/generatebuiltinmanifest; DO NOT EDIT.\n\npackage resources\n\nconst builtinPluginsJSON = " + strconv.Quote(string(data)) + "\n"), nil
}

func writeBuiltinDefinitions(sourceRoot, output string, reservedRuntimeNames []string, providerIDs map[string]struct{}) error {
	definitions, err := generateBuiltinDefinitions(sourceRoot, reservedRuntimeNames, providerIDs)
	if err != nil {
		return err
	}
	rendered, err := renderBuiltinDefinitions(definitions)
	if err != nil {
		return err
	}
	if current, err := os.ReadFile(output); err == nil && bytes.Equal(current, rendered) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(output), ".builtin-plugins-*.tmp")
	if err != nil {
		return fmt.Errorf("create builtin plugin output: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod builtin plugin output: %w", err)
	}
	if _, err := tmp.Write(rendered); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write builtin plugin output: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close builtin plugin output: %w", err)
	}
	if err := os.Rename(tmpName, output); err != nil {
		return fmt.Errorf("install builtin plugin output: %w", err)
	}
	return nil
}
