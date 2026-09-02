package config

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DefaultModelsSettingKey is the app_setting key under which the singleton
// deployment-wide default model configuration JSON is stored (same key-value
// mechanism as the embedding, runner, compaction and scheduler settings).
const DefaultModelsSettingKey = "default_models"

// DefaultModels is the deployment-wide model configuration an admin edits next
// to the providers that back it. It is the single place every model role is
// named: the three agent tiers with their thinking levels, plus the two
// auxiliary roles (vision, embedding) that used to each carry their own
// bespoke settings surface.
//
// Every field is a "provider/model" reference resolved against the configured
// providers, and every field may be empty (that role is simply unset). Agents
// override the three model tiers as one local scope — see MergeAgentModels —
// while the vision and embedding roles stay deployment-wide: reading an image or
// embedding a document is infrastructure, not personality, and per-agent copies
// would mean every new agent silently starts without them.
type DefaultModels struct {
	Model               string `json:"model"`
	ModelThinking       string `json:"model_thinking"`
	ModelStrong         string `json:"model_strong"`
	ModelStrongThinking string `json:"model_strong_thinking"`
	ModelFast           string `json:"model_fast"`
	ModelFastThinking   string `json:"model_fast_thinking"`
	ModelVision         string `json:"model_vision"`
	ModelEmbedding      string `json:"model_embedding"`
}

// LoadDefaultModels reads the singleton default-model config. A missing row is
// not an error — it means "never configured", which is the same as having no
// deployment defaults at all.
func LoadDefaultModels(ctx context.Context, store SettingStore) (DefaultModels, error) {
	raw, err := store.GetSetting(ctx, DefaultModelsSettingKey)
	if err != nil {
		return DefaultModels{}, err
	}
	var d DefaultModels
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			return DefaultModels{}, fmt.Errorf("parse default models: %w", err)
		}
	}
	return d.trimmed(), nil
}

// SaveDefaultModels persists the singleton default-model config.
func SaveDefaultModels(ctx context.Context, store SettingStore, d DefaultModels) error {
	b, err := json.Marshal(d.trimmed())
	if err != nil {
		return fmt.Errorf("marshal default models: %w", err)
	}
	return store.SetSetting(ctx, DefaultModelsSettingKey, string(b))
}

// SaveDefaultModelsIfValue atomically writes only when the exact persisted JSON
// is still the value a conversational Settings tool observed.
func SaveDefaultModelsIfValue(ctx context.Context, store ConditionalSettingStore, expectedValue string, d DefaultModels) (bool, error) {
	b, err := json.Marshal(d.trimmed())
	if err != nil {
		return false, fmt.Errorf("marshal default models: %w", err)
	}
	return store.SetSettingIfValue(ctx, DefaultModelsSettingKey, expectedValue, string(b))
}

func (d DefaultModels) trimmed() DefaultModels {
	d.Model = strings.TrimSpace(d.Model)
	d.ModelThinking = strings.TrimSpace(d.ModelThinking)
	d.ModelStrong = strings.TrimSpace(d.ModelStrong)
	d.ModelStrongThinking = strings.TrimSpace(d.ModelStrongThinking)
	d.ModelFast = strings.TrimSpace(d.ModelFast)
	d.ModelFastThinking = strings.TrimSpace(d.ModelFastThinking)
	d.ModelVision = strings.TrimSpace(d.ModelVision)
	d.ModelEmbedding = strings.TrimSpace(d.ModelEmbedding)
	return d
}

// AgentModels is the resolved per-tier model configuration a Snapshot is built
// from. Agent-local choices stay together: once an agent names its normal
// model, an unset specialized tier falls back to that model before consulting
// the deployment-wide tier. This prevents a partial agent override from
// silently routing work through another provider.
type AgentModels struct {
	Model               string
	ModelThinking       string
	ModelStrong         string
	ModelStrongThinking string
	ModelFast           string
	ModelFastThinking   string
}

// MergeAgentModels resolves the agent and deployment scopes without allowing a
// deployment tier to jump ahead of an agent's normal model. The order for a
// specialized tier is agent tier → agent normal → deployment tier → deployment
// normal. The final deployment-normal fallback remains in Snapshot so an
// unconfigured specialized role stays distinguishable from an explicitly
// configured one.
func MergeAgentModels(def DefaultModels, a Agent) AgentModels {
	agentModel := strings.TrimSpace(a.Model)
	agentThinking := strings.TrimSpace(a.ModelThinking)

	return AgentModels{
		Model:               firstNonEmpty(agentModel, def.Model),
		ModelThinking:       firstNonEmpty(agentThinking, def.ModelThinking),
		ModelStrong:         firstNonEmpty(a.ModelStrong, agentModel, def.ModelStrong),
		ModelStrongThinking: mergeTierThinking(a.ModelStrong, a.ModelStrongThinking, agentModel, agentThinking, def.ModelStrongThinking, def.ModelThinking),
		ModelFast:           firstNonEmpty(a.ModelFast, agentModel, def.ModelFast),
		ModelFastThinking:   mergeTierThinking(a.ModelFast, a.ModelFastThinking, agentModel, agentThinking, def.ModelFastThinking, def.ModelThinking),
	}
}

func mergeTierThinking(agentTierModel, agentTierThinking, agentModel, agentThinking, defaultTier, defaultNormal string) string {
	if strings.TrimSpace(agentTierModel) != "" {
		return firstNonEmpty(agentTierThinking, agentThinking, defaultTier, defaultNormal)
	}
	if strings.TrimSpace(agentModel) != "" {
		return firstNonEmpty(agentTierThinking, agentThinking, defaultNormal)
	}
	return firstNonEmpty(agentTierThinking, defaultTier, defaultNormal)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// MaxDefaultModelRefBytes bounds each deployment-wide model reference. It is a
// byte limit because the JSON setting and provider identifiers are persisted as
// bytes, not runes.
const MaxDefaultModelRefBytes = 256

// ValidateDefaultModels returns the first invalid deployment-wide model field.
// Both HTTP and CAS writes use it so a stale-path refactor cannot bypass the
// same shape and storage-boundary validation.
func ValidateDefaultModels(d DefaultModels) (field, value string, isModel, ok bool) {
	for _, candidate := range []struct{ field, value string }{
		{"model", d.Model},
		{"model_strong", d.ModelStrong},
		{"model_fast", d.ModelFast},
		{"model_vision", d.ModelVision},
		{"model_embedding", d.ModelEmbedding},
	} {
		value := strings.TrimSpace(candidate.value)
		if len(value) > MaxDefaultModelRefBytes || !ValidModelRef(value) {
			return candidate.field, candidate.value, true, false
		}
	}
	for _, candidate := range []struct{ field, value string }{
		{"model_thinking", d.ModelThinking},
		{"model_strong_thinking", d.ModelStrongThinking},
		{"model_fast_thinking", d.ModelFastThinking},
	} {
		if !ValidThinkingLevel(candidate.value) {
			return candidate.field, candidate.value, false, false
		}
	}
	return "", "", false, true
}

// ValidModelRef reports whether one model reference is resolvable at runtime.
// Empty stays valid: an unset role means "inherit" or "not configured", never
// a broken reference.
//
// The rule is the one ResolveModelTier already implies. It splits the ref with
// ParseModelRef and hands both halves to the provider, so a half-typed value
// like "openai/" resolves to an empty model id and asks a provider for no model
// at all. The model picker is a free-text combobox, so that value is one
// keystroke away — and once stored, every runtime reader can only degrade. The
// write path is the last place it can still be refused.
func ValidModelRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return true
	}
	provider, model := ParseModelRef(ref)
	return strings.TrimSpace(provider) != "" && strings.TrimSpace(model) != ""
}

// ValidThinkingLevel reports whether a thinking-level value is one the runtime
// understands. Empty is valid and means "inherit" — the deployment default for
// an agent tier, the model's own default for a deployment tier.
func ValidThinkingLevel(level string) bool {
	switch strings.TrimSpace(level) {
	case "", "minimal", "low", "medium", "high", "xhigh":
		return true
	default:
		return false
	}
}
