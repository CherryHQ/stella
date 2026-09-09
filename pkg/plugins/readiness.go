package plugins

import (
	"cmp"
	"slices"
)

// PluginOAuthBinding is the credential field a package needs from an OAuth
// binding. The runtime keeps this declaration separate from token material.
type PluginOAuthBinding struct {
	Credential string
	EnvVar     string
	Connection string
}

// PluginOAuthRequirement describes one package-owned OAuth requirement. A
// provider token may be shared by several packages, but each package is
// checked independently so a missing binding cannot expose its other assets.
type PluginOAuthRequirement struct {
	Provider string
	Scopes   []string
	Bindings []PluginOAuthBinding
}

// PluginPackageRequirement is the immutable authorization declaration for an
// admitted package.
type PluginPackageRequirement struct {
	PluginID string
	OAuth    []PluginOAuthRequirement
	// UnavailableReason is a bounded admission failure discovered before any
	// package credential or remote capability is touched. It must not contain
	// configuration values or secrets.
	UnavailableReason string
}

// PluginPackageStatus is the complete result for one package. Ready is an
// all-or-nothing result: callers must not expose a package's partial resource
// set when it is false. Reason is bounded and must never contain credentials.
type PluginPackageStatus struct {
	PluginID string
	Ready    bool
	Reason   string
}

// PluginBinaryPreparation records the immutable install evidence for one
// selected CLI. ResolvedVersion is populated only when the installer reports
// the version from its frozen install metadata; an empty value is explicit
// unknown and must not be replaced with the requested range or a path name.
// The type deliberately contains no install paths, options, or credentials.
type PluginBinaryPreparation struct {
	PluginResourceIdentity
	PackageDigest     string
	Name              string
	Tool              string
	RequestedVersion  string
	ResolvedVersion   string
	Backend           string
	SelectionIdentity string
}

// PluginPreparationResult is the single package-level readiness result shared
// by prompt, Skill, MCP, CLI, and sandbox environment consumers.
type PluginPreparationResult struct {
	Packages []PluginPackageStatus
	Binaries []PluginBinaryPreparation
}

// Clone returns an independent preparation result.
func (r PluginPreparationResult) Clone() PluginPreparationResult {
	r.Packages = slices.Clone(r.Packages)
	r.Binaries = slices.Clone(r.Binaries)
	return r
}

// Merge combines package-level preparation outcomes. A later failure replaces
// an earlier success, while a success never resurrects a failed package.
func (r PluginPreparationResult) Merge(update PluginPreparationResult) PluginPreparationResult {
	merged := r.Clone()
	byID := make(map[string]int, len(merged.Packages)+len(update.Packages))
	for i, status := range merged.Packages {
		byID[status.PluginID] = i
	}
	for _, status := range update.Packages {
		if i, ok := byID[status.PluginID]; ok {
			if !status.Ready || !merged.Packages[i].Ready {
				merged.Packages[i].Ready = false
				if !status.Ready {
					merged.Packages[i].Reason = status.Reason
				}
			}
			continue
		}
		byID[status.PluginID] = len(merged.Packages)
		merged.Packages = append(merged.Packages, status)
	}
	slices.SortFunc(merged.Packages, func(left, right PluginPackageStatus) int {
		return cmp.Compare(left.PluginID, right.PluginID)
	})
	byBinary := make(map[string]int, len(merged.Binaries)+len(update.Binaries))
	for i, evidence := range merged.Binaries {
		byBinary[binaryPreparationKey(evidence)] = i
	}
	for _, evidence := range update.Binaries {
		key := binaryPreparationKey(evidence)
		if i, ok := byBinary[key]; ok {
			merged.Binaries[i] = evidence
			continue
		}
		byBinary[key] = len(merged.Binaries)
		merged.Binaries = append(merged.Binaries, evidence)
	}
	merged.Binaries = slices.DeleteFunc(merged.Binaries, func(evidence PluginBinaryPreparation) bool {
		return !merged.Status(evidence.PluginID).Ready
	})
	slices.SortFunc(merged.Binaries, func(left, right PluginBinaryPreparation) int {
		for _, pair := range [][2]string{
			{left.PluginID, right.PluginID},
			{left.ConfigID, right.ConfigID},
			{left.Scope, right.Scope},
			{left.PackageDigest, right.PackageDigest},
			{left.Name, right.Name},
			{left.Tool, right.Tool},
			{left.SelectionIdentity, right.SelectionIdentity},
		} {
			if pair[0] != pair[1] {
				return cmp.Compare(pair[0], pair[1])
			}
		}
		return cmp.Compare(left.Revision, right.Revision)
	})
	return merged
}

func binaryPreparationKey(evidence PluginBinaryPreparation) string {
	return evidence.PluginID + "\x00" + evidence.ConfigID + "\x00" + evidence.Scope + "\x00" + evidence.PackageDigest + "\x00" + evidence.Name + "\x00" + evidence.Tool + "\x00" + evidence.SelectionIdentity
}

// Status returns the status for pluginID. Unknown IDs are not ready, which is
// fail-closed for consumers that receive a resource without its admission
// result.
func (r PluginPreparationResult) Status(pluginID string) PluginPackageStatus {
	for _, status := range r.Packages {
		if status.PluginID == pluginID {
			return status
		}
	}
	return PluginPackageStatus{PluginID: pluginID}
}

// ReadyPluginIDs returns package IDs whose entire preparation succeeded.
func (r PluginPreparationResult) ReadyPluginIDs() []string {
	ids := make([]string, 0, len(r.Packages))
	for _, status := range r.Packages {
		if status.Ready {
			ids = append(ids, status.PluginID)
		}
	}
	return ids
}

// FailedPluginIDs returns package IDs that were not fully prepared.
func (r PluginPreparationResult) FailedPluginIDs() []string {
	ids := make([]string, 0, len(r.Packages))
	for _, status := range r.Packages {
		if !status.Ready {
			ids = append(ids, status.PluginID)
		}
	}
	return ids
}
