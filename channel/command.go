package channel

import (
	"context"
	"fmt"
	"strings"
)

// SessionPool is the session management interface used by Commander.
// Implementations must return a struct whose ID field identifies the session.
type SessionPool interface {
	ResolveSession(channel string) (SessionInfo, error)
	RotateSession(channel string) (SessionInfo, error)
	CompactSession(ctx context.Context, sessionID string) (string, error)
}

// SessionInfo holds the minimal session metadata needed by channels.
type SessionInfo struct {
	ID string
}

// PoolAdapter adapts a concrete pool (e.g. *agent.Pool) whose session methods
// return a richer SessionInfo type into the channel.SessionPool interface.
// The adaptFn extracts the ID from the concrete type.
type PoolAdapter[T any] struct {
	ResolveFunc func(channel string) (T, error)
	RotateFunc  func(channel string) (T, error)
	CompactFunc func(ctx context.Context, sessionID string) (string, error)
	AdaptFn     func(T) SessionInfo
}

func (a *PoolAdapter[T]) ResolveSession(ch string) (SessionInfo, error) {
	v, err := a.ResolveFunc(ch)
	if err != nil {
		return SessionInfo{}, err
	}
	return a.AdaptFn(v), nil
}

func (a *PoolAdapter[T]) RotateSession(ch string) (SessionInfo, error) {
	v, err := a.RotateFunc(ch)
	if err != nil {
		return SessionInfo{}, err
	}
	return a.AdaptFn(v), nil
}

func (a *PoolAdapter[T]) CompactSession(ctx context.Context, sessionID string) (string, error) {
	return a.CompactFunc(ctx, sessionID)
}

// IndexedModel pairs a ModelOption with its 1-based global index.
type IndexedModel struct {
	ModelOption
	GlobalIdx int
}

// Commander handles shared slash commands across all channels.
// Each channel calls Commander methods and only handles presentation.
type Commander struct {
	pool     SessionPool
	listFn   ModelListFunc
	switchFn ModelSwitchFunc
}

// NewCommander creates a Commander backed by the given pool and model functions.
func NewCommander(pool SessionPool, listFn ModelListFunc, switchFn ModelSwitchFunc) *Commander {
	return &Commander{pool: pool, listFn: listFn, switchFn: switchFn}
}

// New creates a new session for the channel, returning the session ID.
func (c *Commander) New(channelID string) (string, error) {
	info, err := c.pool.RotateSession(channelID)
	if err != nil {
		return "", fmt.Errorf("create new session: %w", err)
	}
	return info.ID, nil
}

// Compact compacts the active session's history, returning the summary.
func (c *Commander) Compact(ctx context.Context, channelID string) (string, error) {
	info, err := c.pool.ResolveSession(channelID)
	if err != nil {
		return "", fmt.Errorf("no active session: %w", err)
	}
	summary, err := c.pool.CompactSession(ctx, info.ID)
	if err != nil {
		return "", fmt.Errorf("compaction failed: %w", err)
	}
	return summary, nil
}

// ModelList returns all models, optionally filtered by query.
func (c *Commander) ModelList(query string) []IndexedModel {
	models := c.listFn()
	if query == "" {
		return IndexModels(models)
	}
	return FilterModels(models, query)
}

// ModelSwitch switches to the model at the given 1-based index and rotates
// the session. Returns the selected model. This is used internally by
// channels that select models via index (e.g. Telegram inline keyboards).
func (c *Commander) ModelSwitch(channelID string, idx int) (ModelOption, error) {
	models := c.listFn()
	if idx < 1 || idx > len(models) {
		return ModelOption{}, fmt.Errorf("invalid selection, use a number between 1 and %d", len(models))
	}
	selected := models[idx-1]
	return c.doSwitch(channelID, selected)
}

// ModelSwitchByName switches to the model matching "provider/model" and
// rotates the session. Returns the selected model.
func (c *Commander) ModelSwitchByName(channelID, name string) (ModelOption, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	models := c.listFn()
	for _, m := range models {
		if strings.ToLower(m.Provider+"/"+m.Model) == name {
			return c.doSwitch(channelID, m)
		}
	}
	return ModelOption{}, fmt.Errorf("unknown model %q, use /model to list available models", name)
}

// doSwitch rotates the session first, then applies the model switch.
// This ordering ensures no partial state: if rotation fails, the model
// is unchanged; if the switch fails, the session was already rotated
// (acceptable since rotation is idempotent).
func (c *Commander) doSwitch(channelID string, selected ModelOption) (ModelOption, error) {
	if _, err := c.pool.RotateSession(channelID); err != nil {
		return ModelOption{}, fmt.Errorf("rotate session: %w", err)
	}
	if c.switchFn != nil {
		if err := c.switchFn(selected.Provider, selected.Model); err != nil {
			return ModelOption{}, fmt.Errorf("switch model: %w", err)
		}
	}
	return selected, nil
}

// ParseModelArgs parses /model arguments as a query string.
// Returns empty string when no arguments are provided.
func ParseModelArgs(args string) string {
	return strings.TrimSpace(args)
}

// IndexModels wraps a full model list with sequential 1-based indices.
func IndexModels(models []ModelOption) []IndexedModel {
	out := make([]IndexedModel, len(models))
	for i, m := range models {
		out[i] = IndexedModel{ModelOption: m, GlobalIdx: i + 1}
	}
	return out
}

// FilterModels returns indexed models matching the query, preserving
// their 1-based global indices from the full list.
func FilterModels(models []ModelOption, query string) []IndexedModel {
	query = strings.ToLower(query)
	var out []IndexedModel
	for i, m := range models {
		label := strings.ToLower(m.Provider + "/" + m.Model)
		if strings.Contains(label, query) {
			out = append(out, IndexedModel{ModelOption: m, GlobalIdx: i + 1})
		}
	}
	return out
}
