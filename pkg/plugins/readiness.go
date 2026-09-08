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

// PluginPreparationResult is the single package-level readiness result shared
// by prompt, Skill, MCP, CLI, and sandbox environment consumers.
type PluginPreparationResult struct {
	Packages []PluginPackageStatus
}

// Clone returns an independent preparation result.
func (r PluginPreparationResult) Clone() PluginPreparationResult {
	r.Packages = slices.Clone(r.Packages)
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
	return merged
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
