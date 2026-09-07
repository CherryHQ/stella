package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

// Backend identifies the implementation that supplies a plugin's capabilities.
type Backend string

const (
	BackendCLI Backend = "cli"
	BackendMCP Backend = "mcp"
)

// Source identifies whether a definition ships with Stella or was installed.
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceCustom  Source = "custom"
)

// Scope is the closed set of configuration owners. It is intentionally separate
// from an implementation backend's credential mode.
type Scope string

const (
	ScopeSystem      Scope = "system"
	ScopeSystemAgent Scope = "system_agent"
	ScopeUser        Scope = "user"
	ScopeUserAgent   Scope = "user_agent"
)

var scopePrecedence = [...]Scope{ScopeUserAgent, ScopeUser, ScopeSystemAgent, ScopeSystem}

// Definition is the authored, non-secret identity of a plugin.
type Definition struct {
	ID                string
	DisplayName       string
	Backend           Backend
	Source            Source
	ImplementationKey string
	Spec              json.RawMessage
	DefaultEnabled    bool
	Revision          int64
	CreatorUserID     string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Config is one complete scope record. Enabled is nullable: nil inherits the
// definition default and never falls through to another scope.
type Config struct {
	ID             string
	PluginID       string
	Scope          Scope
	UserID         string
	AgentID        string
	Enabled        *bool
	Payload        json.RawMessage
	CredentialRefs json.RawMessage
	Revision       int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Effective is the winner-first resolution result for one trusted context.
type Effective struct {
	PluginID             string
	ConfigID             string
	SourceScope          Scope
	IsEffectivelyEnabled bool
	AvailabilityReason   string
	Payload              json.RawMessage
}

var (
	ErrInvalidDefinition = errors.New("plugin: invalid definition")
	ErrInvalidConfig     = errors.New("plugin: invalid config")
	ErrUnknownScope      = errors.New("plugin: unknown scope")
)

func (d Definition) Validate() error {
	if d.DisplayName == "" || d.ImplementationKey == "" || d.Revision < 1 {
		return fmt.Errorf("%w: identity or revision", ErrInvalidDefinition)
	}
	if err := ValidateName(d.ID); err != nil {
		return err
	}
	if d.Backend != BackendCLI && d.Backend != BackendMCP {
		return fmt.Errorf("%w: backend %q", ErrInvalidDefinition, d.Backend)
	}
	if d.Source != SourceBuiltin && d.Source != SourceCustom {
		return fmt.Errorf("%w: source %q", ErrInvalidDefinition, d.Source)
	}
	if d.Source == SourceBuiltin && d.CreatorUserID != "" {
		return fmt.Errorf("%w: builtin creator", ErrInvalidDefinition)
	}
	if d.Source == SourceCustom && d.DefaultEnabled {
		return fmt.Errorf("%w: custom definitions default disabled", ErrInvalidDefinition)
	}
	if len(d.Spec) == 0 || !json.Valid(d.Spec) || bytes.Equal(bytes.TrimSpace(d.Spec), []byte("null")) {
		return fmt.Errorf("%w: spec must be JSON", ErrInvalidDefinition)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(d.Spec, &object); err != nil || object == nil {
		return fmt.Errorf("%w: spec must be object: %w", ErrInvalidDefinition, err)
	}
	return nil
}

// ValidateName enforces the standard Agent Plugin package name contract.
func ValidateName(name string) error {
	if !agentpackage.ValidName(name) {
		return fmt.Errorf("%w: invalid plugin name %q", ErrInvalidDefinition, name)
	}
	return nil
}

func (c Config) Validate() error {
	if c.ID == "" || c.PluginID == "" || c.Revision < 1 {
		return fmt.Errorf("%w: identity or revision", ErrInvalidConfig)
	}
	if !validScope(c.Scope) {
		return fmt.Errorf("%w: %q", ErrUnknownScope, c.Scope)
	}
	if !ownerMatches(c.Scope, c.UserID, c.AgentID) {
		return fmt.Errorf("%w: owner tuple for %q", ErrInvalidConfig, c.Scope)
	}
	if len(c.CredentialRefs) == 0 {
		c.CredentialRefs = json.RawMessage(`{}`)
	}
	if !jsonObject(c.CredentialRefs) {
		return fmt.Errorf("%w: credential refs must be an object", ErrInvalidConfig)
	}
	if len(c.Payload) == 0 {
		if c.Enabled == nil || *c.Enabled {
			return fmt.Errorf("%w: enabled config requires payload", ErrInvalidConfig)
		}
		if !emptyJSONObject(c.CredentialRefs) {
			return fmt.Errorf("%w: negative config cannot keep credentials", ErrInvalidConfig)
		}
	} else if !jsonObject(c.Payload) {
		return fmt.Errorf("%w: payload must be an object", ErrInvalidConfig)
	}
	return nil
}

func validScope(scope Scope) bool {
	for _, candidate := range scopePrecedence {
		if scope == candidate {
			return true
		}
	}
	return false
}

func ownerMatches(scope Scope, userID, agentID string) bool {
	switch scope {
	case ScopeSystem:
		return userID == "" && agentID == ""
	case ScopeSystemAgent:
		return userID == "" && agentID != ""
	case ScopeUser:
		return userID != "" && agentID == ""
	case ScopeUserAgent:
		return userID != "" && agentID != ""
	default:
		return false
	}
}

func jsonObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func emptyJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil && len(object) == 0
}

func cloneRaw(raw json.RawMessage) json.RawMessage { return bytes.Clone(raw) }

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneDefinition(def Definition) Definition {
	def.Spec = cloneRaw(def.Spec)
	return def
}

func cloneConfig(config Config) Config {
	config.Enabled = cloneBool(config.Enabled)
	config.Payload = cloneRaw(config.Payload)
	config.CredentialRefs = cloneRaw(config.CredentialRefs)
	return config
}
