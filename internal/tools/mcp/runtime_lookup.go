package mcp

type runtimeWrapper struct{ manager *Manager }

func (w runtimeWrapper) Manager() *Manager { return w.manager }
