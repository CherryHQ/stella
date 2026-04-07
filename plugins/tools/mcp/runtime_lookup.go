package mcp

import annamcp "github.com/vaayne/anna/internal/mcp"

type runtimeWrapper struct{ manager *annamcp.Manager }

func (w runtimeWrapper) Manager() *annamcp.Manager { return w.manager }
