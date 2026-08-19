package manifestplugins

// StoredOverride is one persisted customization, as the caller's store holds it.
// It is declared here rather than imported so this package keeps owning the
// merge protocol without depending on the storage layer that happens to carry
// it.
type StoredOverride struct {
	PluginID string
	Enabled  *bool
	Config   string
}

// Resolve overlays stored overrides on the builtin manifest and returns the
// plugin set the server should actually run.
//
// There is exactly one of these on purpose. The startup wiring and the admin
// control plane both need the resolved manifest, and when they each carried
// their own copy of the rule they drifted: one applied definition overrides and
// the other silently applied only the enable flag, so a customization worked
// until the process restarted and then quietly didn't.
//
// onCorrupt reports a row that cannot be read. A bad row is skipped rather than
// fatal: one unreadable customization must not take the whole plugin set down
// with it.
func Resolve(builtin *Manifest, overrides []StoredOverride, onCorrupt func(pluginID string, err error)) *Manifest {
	if builtin == nil {
		return nil
	}
	if onCorrupt == nil {
		onCorrupt = func(string, error) {}
	}
	byID := make(map[string]StoredOverride, len(overrides))
	for _, ov := range overrides {
		byID[ov.PluginID] = ov
	}

	seen := make(map[string]bool, len(builtin.Plugins))
	for i := range builtin.Plugins {
		id := builtin.Plugins[i].ID
		seen[id] = true
		builtin.Plugins[i].Builtin = true
		// Builtins are release-managed unless their shipped definition opts into
		// tenant control. Historical override rows stay harmless after an upgrade
		// instead of hiding a tool that sessions need across every backend.
		if !builtin.Plugins[i].TenantManaged {
			continue
		}
		ov, ok := byID[id]
		if !ok {
			continue
		}
		if ov.Config != "" {
			merged, err := ApplyOverride(builtin.Plugins[i], ov.Config)
			if err != nil {
				onCorrupt(id, err)
			} else if owned, err := OwnedFields(ov.Config); err != nil {
				onCorrupt(id, err)
			} else {
				merged.OverriddenFields = owned
				builtin.Plugins[i] = merged
			}
		}
		if ov.Enabled != nil {
			builtin.Plugins[i].Enabled = *ov.Enabled
		}
	}

	// A row with no builtin behind it is a plugin an admin added; its definition
	// is the row.
	for _, ov := range overrides {
		if seen[ov.PluginID] || ov.Config == "" {
			continue
		}
		p, err := PluginFromDefinition(ov.PluginID, ov.Config)
		if err != nil {
			onCorrupt(ov.PluginID, err)
			continue
		}
		if ov.Enabled != nil {
			p.Enabled = *ov.Enabled
		}
		builtin.Plugins = append(builtin.Plugins, p)
	}
	return builtin
}
