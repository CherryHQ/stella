package plugin

import (
	"context"
	"strings"
)

// DefinitionLifecycleStatus is the safe, user-facing projection of a
// definition's cleanup state. It deliberately stays separate from Definition
// so volatile owner observations never affect runtime identity or snapshots.
type DefinitionLifecycleStatus string

const (
	DefinitionLifecycleInstalled      DefinitionLifecycleStatus = "installed"
	DefinitionLifecycleInUse          DefinitionLifecycleStatus = "in_use"
	DefinitionLifecycleCleanupPending DefinitionLifecycleStatus = "cleanup_pending"
)

// DefinitionLifecycleReason is intentionally a closed set. In particular,
// ownership errors are never sent to an API client as raw runtime details.
type DefinitionLifecycleReason string

const (
	DefinitionLifecycleActive               DefinitionLifecycleReason = "active"
	DefinitionLifecycleRuntimeOwner         DefinitionLifecycleReason = "runtime_owner"
	DefinitionLifecycleAwaitingCleanup      DefinitionLifecycleReason = "awaiting_cleanup"
	DefinitionLifecycleOwnershipUnconfirmed DefinitionLifecycleReason = "ownership_unconfirmed"
)

type DefinitionLifecycle struct {
	Status DefinitionLifecycleStatus
	Reason DefinitionLifecycleReason
}

// DefinitionLifecycles projects definitions that have already passed an
// Access visibility check. Server transport uses this batch form so a list
// request takes one owner snapshot rather than one scan per definition.
func (s *Service) DefinitionLifecycles(ctx context.Context, defs []Definition) map[string]DefinitionLifecycle {
	return s.definitionLifecycles(ctx, defs)
}

func (s *Service) definitionLifecycles(ctx context.Context, defs []Definition) map[string]DefinitionLifecycle {
	out := make(map[string]DefinitionLifecycle, len(defs))
	for _, def := range defs {
		out[def.ID] = DefinitionLifecycle{
			Status: DefinitionLifecycleInstalled,
			Reason: DefinitionLifecycleActive,
		}
	}

	retired := make([]Definition, 0)
	for _, def := range defs {
		// Only custom definitions enter the retired package cleanup lifecycle.
		// Native capabilities are not represented by this resource and must not
		// inherit package cleanup semantics through a shared status helper.
		if def.Source == SourceCustom && !def.RetiredAt.IsZero() {
			retired = append(retired, def)
		}
	}
	if len(retired) == 0 {
		return out
	}

	pending := func(reason DefinitionLifecycleReason) {
		for _, def := range retired {
			out[def.ID] = DefinitionLifecycle{Status: DefinitionLifecycleCleanupPending, Reason: reason}
		}
	}
	if s == nil || s.ownerSnapshot == nil || ctx == nil {
		pending(DefinitionLifecycleOwnershipUnconfirmed)
		return out
	}
	owners, err := s.ownerSnapshot(ctx)
	if err != nil {
		pending(DefinitionLifecycleOwnershipUnconfirmed)
		return out
	}
	ownerIDs := make(map[string]struct{}, len(owners.PluginIDs))
	for _, id := range owners.PluginIDs {
		if id != "" {
			ownerIDs[id] = struct{}{}
		}
	}
	ownerDigests := make(map[string]struct{}, len(owners.Digests))
	for _, digest := range owners.Digests {
		digest = strings.TrimPrefix(digest, "sha256:")
		if validStoreDigest(digest) {
			ownerDigests[digest] = struct{}{}
		}
	}
	for _, def := range retired {
		if _, ok := ownerIDs[def.ID]; ok {
			out[def.ID] = DefinitionLifecycle{Status: DefinitionLifecycleInUse, Reason: DefinitionLifecycleRuntimeOwner}
			continue
		}
		digest, digestErr := definitionPackageDigest(def)
		if digestErr != nil {
			out[def.ID] = DefinitionLifecycle{Status: DefinitionLifecycleCleanupPending, Reason: DefinitionLifecycleOwnershipUnconfirmed}
			continue
		}
		if digest != "" {
			if _, ok := ownerDigests[digest]; ok {
				out[def.ID] = DefinitionLifecycle{Status: DefinitionLifecycleInUse, Reason: DefinitionLifecycleRuntimeOwner}
				continue
			}
		}
		out[def.ID] = DefinitionLifecycle{Status: DefinitionLifecycleCleanupPending, Reason: DefinitionLifecycleAwaitingCleanup}
	}
	return out
}
