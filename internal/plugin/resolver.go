package plugin

import "fmt"

// Resolve applies the common winner-first rule for a trusted user/agent tuple.
// A selected false or unavailable record never falls back to a broader record.
func Resolve(def Definition, configs []Config, userID, agentID string) (Effective, error) {
	if err := def.Validate(); err != nil {
		return Effective{}, err
	}
	byScope := make(map[Scope]Config, len(configs))
	for _, config := range configs {
		if config.PluginID != def.ID {
			return Effective{}, fmt.Errorf("%w: definition mismatch", ErrInvalidConfig)
		}
		if err := config.Validate(); err != nil {
			return Effective{}, err
		}
		// A catalog read can contain every user's row. Only an exact owner tuple
		// is eligible for this trusted context; unrelated rows must not collide
		// with the selected scope or disclose their state.
		if !matchesContext(config, userID, agentID) {
			continue
		}
		if _, exists := byScope[config.Scope]; exists {
			return Effective{}, fmt.Errorf("%w: duplicate scope %q", ErrInvalidConfig, config.Scope)
		}
		byScope[config.Scope] = cloneConfig(config)
	}

	// These two records are upper bounds, so they are checked before the
	// more-specific records. A matching false can never be bypassed by a true
	// user or user_agent record.
	for _, scope := range []Scope{ScopeSystem, ScopeSystemAgent} {
		config, ok := byScope[scope]
		if ok && matchesContext(config, userID, agentID) && config.Enabled != nil && !*config.Enabled {
			return effectiveFrom(def, config), nil
		}
	}
	for _, scope := range scopePrecedence {
		config, ok := byScope[scope]
		if ok && matchesContext(config, userID, agentID) {
			return effectiveFrom(def, config), nil
		}
	}
	return Effective{
		PluginID:             def.ID,
		IsEffectivelyEnabled: def.DefaultEnabled,
		AvailabilityReason:   "shipped_default",
		Payload:              cloneRaw(def.Spec),
	}, nil
}

func configsFor(def Definition, configs []Config) []Config {
	owned := make([]Config, 0)
	for _, config := range configs {
		if config.PluginID == def.ID {
			owned = append(owned, config)
		}
	}
	return owned
}

func effectiveFrom(def Definition, config Config) Effective {
	enabled := def.DefaultEnabled
	if config.Enabled != nil {
		enabled = *config.Enabled
	}
	reason := "scope_enabled"
	if !enabled {
		reason = "scope_disabled"
	}
	payload := cloneRaw(config.Payload)
	if len(config.Payload) != 0 {
		if resolved, err := mergeObjects(def.Spec, config.Payload); err == nil {
			payload = resolved
		}
	}
	return Effective{
		PluginID:             def.ID,
		ConfigID:             config.ID,
		SourceScope:          config.Scope,
		IsEffectivelyEnabled: enabled,
		AvailabilityReason:   reason,
		Payload:              payload,
	}
}

func matchesContext(config Config, userID, agentID string) bool {
	switch config.Scope {
	case ScopeSystem:
		return config.UserID == "" && config.AgentID == ""
	case ScopeSystemAgent:
		return agentID != "" && config.UserID == "" && config.AgentID == agentID
	case ScopeUser:
		return userID != "" && config.UserID == userID && config.AgentID == ""
	case ScopeUserAgent:
		return userID != "" && agentID != "" && config.UserID == userID && config.AgentID == agentID
	default:
		return false
	}
}

// IsDenied reports whether a resolved plugin is explicitly unavailable.
func (e Effective) IsDenied() bool { return !e.IsEffectivelyEnabled }
