package pluginhost

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

type RuntimeHost struct {
	host *Host
	mu   sync.RWMutex
	rt   map[string]*runtimeEntry
}

type runtimeEntry struct {
	reg     pkgplugins.RuntimeSpec
	managed pkgplugins.Runtime
}

func NewRuntimeHost(host *Host) *RuntimeHost {
	return &RuntimeHost{host: host, rt: map[string]*runtimeEntry{}}
}

func (h *RuntimeHost) Get(pluginID string, runtimeName string) (pkgplugins.RuntimeHandle, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	entry, ok := h.rt[runtimeKey(pluginID, runtimeName)]
	if !ok || entry.managed == nil {
		return nil, false
	}
	return runtimeHandle{entry: entry}, true
}

func (h *RuntimeHost) Lookup(pluginID string, runtimeName string) (pkgplugins.RuntimeHandle, bool) {
	return h.Get(pluginID, runtimeName)
}

func (h *RuntimeHost) ApplyPlugin(ctx context.Context, pluginID string) error {
	desired, err := h.host.DesiredState(ctx, pluginID)
	if err != nil {
		return err
	}
	regs := h.registrations(pluginID)
	for _, reg := range regs {
		if err := h.applyOne(ctx, reg, desired); err != nil {
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
	key := runtimeKey(reg.PluginID, reg.Name)
	h.mu.Lock()
	entry := h.rt[key]
	if entry == nil {
		entry = &runtimeEntry{reg: reg}
		h.rt[key] = entry
	}
	managed := entry.managed
	h.mu.Unlock()
	if managed == nil {
		build := reg.Build
		if build == nil {
			build = reg.Factory
		}
		if build == nil {
			return fmt.Errorf("runtime %s has no builder", key)
		}
		created, err := build(pkgplugins.RuntimeContext{Platform: h.host.platform(reg.PluginID), Services: h.host, State: desired.Clone()})
		if err != nil {
			return fmt.Errorf("create runtime %s: %w", key, err)
		}
		managed = created
		h.mu.Lock()
		entry.managed = created
		h.mu.Unlock()
	}
	if err := managed.Apply(ctx, desired.Clone()); err != nil {
		return fmt.Errorf("apply runtime %s: %w", key, err)
	}
	return nil
}

func (h *RuntimeHost) Stop(ctx context.Context) error {
	h.mu.Lock()
	entries := make([]*runtimeEntry, 0, len(h.rt))
	for _, entry := range h.rt {
		entries = append(entries, entry)
	}
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

func (h *RuntimeHost) Snapshot(ctx context.Context, pluginID string, runtimeName string) (pkgplugins.RuntimeStatus, error) {
	handle, ok := h.Get(pluginID, runtimeName)
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
