package plugins

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BinaryEvidenceFileName is the sanitized manifest stored beside an
// immutable CLI selection.
const BinaryEvidenceFileName = ".stella-cli-evidence.json"

// BinaryEvidenceRequest is the safe subset of a selected CLI needed to match
// mise's resolved output. It contains no options, paths, or credentials.
type BinaryEvidenceRequest struct {
	Key              string
	Lookup           string
	PublicName       string
	RequestedVersion string
}

// BinaryEvidenceTool is one sanitized resolved CLI entry.
type BinaryEvidenceTool struct {
	Key              string `json:"key"`
	Lookup           string `json:"lookup"`
	PublicName       string `json:"public_name"`
	RequestedVersion string `json:"requested_version"`
	ResolvedVersion  string `json:"resolved_version,omitempty"`
}

// BinaryInstallEvidence is frozen alongside a published selection. An empty
// ResolvedVersion is explicit unknown, never a guessed range or path name.
type BinaryInstallEvidence struct {
	Tools []BinaryEvidenceTool `json:"tools"`
}

// ParseBinaryMiseList extracts only the active installed version matching the
// selected request. Older installed versions are ignored even when they occur
// first in mise's JSON output.
func ParseBinaryMiseList(data []byte, requests []BinaryEvidenceRequest) (BinaryInstallEvidence, error) {
	var listed map[string][]struct {
		Version          string `json:"version"`
		RequestedVersion string `json:"requested_version"`
		Active           bool   `json:"active"`
		Installed        bool   `json:"installed"`
	}
	if err := json.Unmarshal(data, &listed); err != nil {
		return BinaryInstallEvidence{}, fmt.Errorf("decode mise version metadata: %w", err)
	}
	evidence := BinaryInstallEvidence{Tools: make([]BinaryEvidenceTool, 0, len(requests))}
	for _, request := range requests {
		requested := request.RequestedVersion
		if strings.TrimSpace(requested) == "" {
			requested = "latest"
		}
		lookup := request.Lookup
		if lookup == "" {
			lookup = request.Key
		}
		publicName := request.PublicName
		if publicName == "" {
			publicName = lookup
		}
		resolved := ""
		for _, item := range listed[request.Key] {
			if item.Active && item.Installed && item.Version != "" && item.RequestedVersion == requested {
				resolved = item.Version
				break
			}
		}
		evidence.Tools = append(evidence.Tools, BinaryEvidenceTool{
			Key: request.Key, Lookup: lookup, PublicName: publicName,
			RequestedVersion: requested, ResolvedVersion: resolved,
		})
	}
	return evidence, nil
}
