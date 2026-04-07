package mcp

import pkgmcp "github.com/vaayne/anna/pkg/mcp"

const (
	TransportStdio          = pkgmcp.TransportStdio
	TransportSSE            = pkgmcp.TransportSSE
	TransportStreamableHTTP = pkgmcp.TransportStreamableHTTP
	TransportHTTP           = pkgmcp.TransportHTTP

	DefaultTimeoutSeconds = pkgmcp.DefaultTimeoutSeconds
)

type Config = pkgmcp.Config
type ServerConfig = pkgmcp.ServerConfig

func DecodeConfig(raw map[string]any) (Config, error) {
	return pkgmcp.DecodeConfig(raw)
}
