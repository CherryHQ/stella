package host

func (h *Host) HasRuntime(pluginID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return hasRuntimeLocked(h.runtimeRegs, pluginID)
}

func (h *Host) HasConfig(pluginID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.configRegs[pluginID]
	return ok
}

func (h *Host) HasStatus(pluginID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.statusRegs[pluginID]
	return ok
}
