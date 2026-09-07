package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// applyCLIWriteOnlyPatch updates only the typed binary parameter map. The
// definition remains the authority for executable identity; current carries
// existing pins and options forward without exposing those fields to callers.
func applyCLIWriteOnlyPatch(def Definition, current json.RawMessage, patch ConfigPatch) (json.RawMessage, error) {
	declaration, err := DecodeResourcePayload(def.Spec, "definition spec")
	if err != nil {
		return nil, err
	}

	parameters := ConfigParameters{}
	if len(bytes.TrimSpace(current)) != 0 {
		parameters, err = DecodeConfigParameters(current)
		if err != nil {
			return nil, fmt.Errorf("%w: config payload: %w", ErrInvalidConfig, err)
		}
	}
	if len(patch.BinaryVersions) != 0 && parameters.Binaries == nil {
		parameters.Binaries = make(map[string]BinaryParameters, len(patch.BinaryVersions))
	}
	declaredBinaries := make(map[string]struct{}, len(declaration.Binaries))
	for _, binary := range declaration.Binaries {
		declaredBinaries[binary.Name] = struct{}{}
	}
	for name := range parameters.Binaries {
		if _, ok := declaredBinaries[name]; !ok {
			return nil, fmt.Errorf("%w: unknown binary %q", ErrInvalidConfig, name)
		}
	}
	for name, version := range patch.BinaryVersions {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" {
			return nil, fmt.Errorf("%w: binary name and version are required", ErrInvalidConfig)
		}
		if _, ok := declaredBinaries[name]; !ok {
			return nil, fmt.Errorf("%w: unknown binary %q", ErrInvalidConfig, name)
		}
		parameter := parameters.Binaries[name]
		parameter.Version = version
		parameters.Binaries[name] = parameter
	}
	return json.Marshal(parameters)
}
