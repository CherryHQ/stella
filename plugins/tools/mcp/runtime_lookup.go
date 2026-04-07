package mcp

import pkgmcp "github.com/vaayne/anna/pkg/mcp"

type runtimeWrapper struct{ manager *pkgmcp.Manager }

func (w runtimeWrapper) Manager() *pkgmcp.Manager { return w.manager }
