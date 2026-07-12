package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// runtimeKey identifies a managed runtime instance.
// Pair (RuntimeID, RuntimeName) — RuntimeID is the channel.ID for managed
// channel runtimes and the plugin ID for other plugin-scoped runtimes.
type runtimeKey struct {
	RuntimeID   string
	RuntimeName string
}

type runtimeEntry struct {
	reg     pkgplugins.RuntimeSpec
	managed pkgplugins.Runtime
}

// RuntimeHost owns the map of managed runtime instances.
type RuntimeHost struct {
	host *Host
	mu   sync.RWMutex
	rt   map[runtimeKey]*runtimeEntry

	// applyMu serializes the rare runtime build/apply lifecycle against Quiesce
	// and Stop. Without it, a runtime whose Build was in flight could be absent
	// from Quiesce's snapshot and start polling after drain began.
	applyMu  sync.Mutex
	quiesced bool
}

func NewRuntimeHost(host *Host) *RuntimeHost {
	return &RuntimeHost{host: host, rt: map[runtimeKey]*runtimeEntry{}}
}

func configMapFromJSON(raw string) map[string]any {
	if raw == "" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// Get resolves a managed runtime.
func (h *RuntimeHost) Get(_ context.Context, runtimeID string, runtimeName string) (pkgplugins.RuntimeHandle, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if entry := h.rt[runtimeKey{RuntimeID: runtimeID, RuntimeName: runtimeName}]; entry != nil && entry.managed != nil {
		return runtimeHandle{entry: entry}, true
	}
	// channel plugin lookups: caller passed "channel/<type>"; runtime was registered
	// under the channel instance ID. Fall back to matching reg.PluginID + reg.Name.
	if _, ok := strings.CutPrefix(runtimeID, config.PluginKindChannel+"/"); ok {
		for _, entry := range h.rt {
			if entry.reg.PluginID == runtimeID && entry.reg.Name == runtimeName && entry.managed != nil {
				return runtimeHandle{entry: entry}, true
			}
		}
	}
	return nil, false
}

// Lookup is an alias for Get to match the pkgplugins.RuntimeLookup interface.
func (h *RuntimeHost) Lookup(ctx context.Context, runtimeID string, runtimeName string) (pkgplugins.RuntimeHandle, bool) {
	return h.Get(ctx, runtimeID, runtimeName)
}

func (h *RuntimeHost) ApplyPlugin(ctx context.Context, pluginID string) error {
	desired, err := h.host.DesiredState(ctx, pluginID)
	if err != nil {
		return err
	}
	if channelType, ok := strings.CutPrefix(pluginID, config.PluginKindChannel+"/"); ok {
		channels, err := h.host.store.ListChannelsByType(ctx, channelType)
		if err == nil && len(channels) > 0 {
			for _, channel := range channels {
				if !desired.Enabled {
					channel.Enabled = false
				}
				if err := h.ApplyChannel(ctx, channel); err != nil {
					return err
				}
			}
			return nil
		}
	}
	regs := h.registrations(pluginID)
	for _, reg := range regs {
		if err := h.applyOne(ctx, reg, desired); err != nil {
			return err
		}
	}
	return nil
}

func (h *RuntimeHost) ApplyChannel(ctx context.Context, channel config.Channel) error {
	if channel.Type == "" {
		channel.Type = channel.ID
	}
	pluginID := config.PluginID(config.PluginKindChannel, channel.Type)
	if desired, err := h.host.config.Get(ctx, pluginID); err == nil && !desired.Enabled {
		channel.Enabled = false
	}
	regs := h.registrations(pluginID)
	desired := pkgplugins.PluginState{
		ID:      channel.ID,
		Enabled: channel.Enabled,
		Config:  configMapFromJSON(channel.Config),
	}
	for _, reg := range regs {
		if err := h.applyOneWithKey(ctx, reg, channel.ID, desired); err != nil {
			return err
		}
	}
	return nil
}

func (h *RuntimeHost) registrations(pluginID string) []pkgplugins.RuntimeSpec {
	h.host.mu.RLock()
	defer h.host.mu.RUnlock()
	regs := make([]pkgplugins.RuntimeSpec, 0, len(h.host.runtimeRegs))
	for _, reg := range h.host.runtimeRegs {
		if reg.PluginID == pluginID {
			regs = append(regs, reg)
		}
	}
	sort.Slice(regs, func(i, j int) bool { return regs[i].Name < regs[j].Name })
	return regs
}

func (h *RuntimeHost) applyOne(ctx context.Context, reg pkgplugins.RuntimeSpec, desired pkgplugins.PluginState) error {
	return h.applyOneWithKey(ctx, reg, reg.PluginID, desired)
}

func (h *RuntimeHost) applyOneWithKey(ctx context.Context, reg pkgplugins.RuntimeSpec, runtimeID string, desired pkgplugins.PluginState) error {
	h.applyMu.Lock()
	defer h.applyMu.Unlock()
	if h.quiesced {
		return fmt.Errorf("runtime host is quiescing")
	}

	key := runtimeKey{RuntimeID: runtimeID, RuntimeName: reg.Name}
	h.mu.Lock()
	entry := h.rt[key]
	if entry == nil {
		entry = &runtimeEntry{reg: reg}
		h.rt[key] = entry
	}
	managed := entry.managed
	h.mu.Unlock()
	if managed == nil {
		if err := h.host.verifyRuntimeCapabilities(reg.PluginID); err != nil {
			h.host.log.Error("refusing to start managed runtime: capability unavailable",
				"plugin", reg.PluginID, "runtime", reg.Name, "error", err)
			return fmt.Errorf("start runtime %s/%s: %w", runtimeID, reg.Name, err)
		}
		build := reg.Build
		if build == nil {
			return fmt.Errorf("runtime %s/%s has no builder", runtimeID, reg.Name)
		}
		created, err := build(pkgplugins.RuntimeContext{
			Platform: h.host.platform(reg.PluginID),
			State:    desired.Clone(),
		})
		if err != nil {
			return fmt.Errorf("create runtime %s/%s: %w", runtimeID, reg.Name, err)
		}
		managed = created
		h.mu.Lock()
		entry.managed = created
		h.mu.Unlock()
	}
	if err := managed.Apply(ctx, desired.Clone()); err != nil {
		return fmt.Errorf("apply runtime %s/%s: %w", runtimeID, reg.Name, err)
	}
	return nil
}

// ingressQuiescer is the optional two-phase-drain capability a managed runtime
// may implement. Quiesce stops NEW ingress (e.g. channel polling) while leaving
// already-accepted operations and any downstream registrations (e.g. notifier
// senders) intact until the final Stop. Runtimes that have no ingress to quiesce
// simply do not implement it; RuntimeHost skips them.
type ingressQuiescer interface {
	Quiesce(ctx context.Context)
}

// Quiesce invokes the optional ingress-quiescer on every managed runtime that
// implements it, without clearing the runtime table: the subsequent Stop still
// needs the entries to tear them down fully. This is the drain-phase counterpart
// to Stop — it halts new ingress while preserving accepted work.
func (h *RuntimeHost) Quiesce(ctx context.Context) {
	h.applyMu.Lock()
	defer h.applyMu.Unlock()
	if h.quiesced {
		return
	}
	h.quiesced = true

	h.mu.RLock()
	entries := make([]*runtimeEntry, 0, len(h.rt))
	for _, entry := range h.rt {
		entries = append(entries, entry)
	}
	h.mu.RUnlock()

	for _, entry := range entries {
		if entry.managed == nil {
			continue
		}
		if q, ok := entry.managed.(ingressQuiescer); ok {
			q.Quiesce(ctx)
		}
	}
}

// Release tears down every managed runtime without making the host terminal. It
// is used when a channel-ingress lease is lost: a later lease acquisition must be
// able to rebuild the pollers. Entries are removed before Stop is called so the
// lock isn't held while runtime teardown executes.
func (h *RuntimeHost) Release(ctx context.Context) error {
	h.applyMu.Lock()
	defer h.applyMu.Unlock()

	h.mu.Lock()
	entries := h.rt
	h.rt = map[runtimeKey]*runtimeEntry{}
	h.mu.Unlock()

	var lastErr error
	for _, entry := range entries {
		if entry.managed == nil {
			continue
		}
		if err := entry.managed.Stop(ctx); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// Stop tears down every managed runtime permanently. It also prevents any later
// Apply, which keeps a graceful drain from admitting new ingress.
func (h *RuntimeHost) Stop(ctx context.Context) error {
	h.applyMu.Lock()
	h.quiesced = true
	h.applyMu.Unlock()
	return h.Release(ctx)
}

func (h *RuntimeHost) Snapshot(ctx context.Context, runtimeID string, runtimeName string) (pkgplugins.RuntimeStatus, error) {
	handle, ok := h.Get(ctx, runtimeID, runtimeName)
	if !ok {
		return pkgplugins.RuntimeStatus{State: pkgplugins.RuntimeStateStopped, UpdatedAt: time.Now().UTC()}, nil
	}
	return handle.Snapshot(ctx)
}

type runtimeHandle struct{ entry *runtimeEntry }

func (h runtimeHandle) Snapshot(ctx context.Context) (pkgplugins.RuntimeStatus, error) {
	if h.entry == nil || h.entry.managed == nil {
		return pkgplugins.RuntimeStatus{State: pkgplugins.RuntimeStateStopped, UpdatedAt: time.Now().UTC()}, nil
	}
	return h.entry.managed.Snapshot(ctx)
}

func (h runtimeHandle) Status(ctx context.Context) (pkgplugins.RuntimeStatus, error) {
	return h.Snapshot(ctx)
}

func (h runtimeHandle) RuntimeAccessor() any {
	if h.entry == nil || h.entry.managed == nil {
		return nil
	}
	if accessor, ok := h.entry.managed.(interface{ RuntimeAccessor() any }); ok {
		return accessor.RuntimeAccessor()
	}
	return nil
}
