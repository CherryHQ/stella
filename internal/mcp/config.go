package mcp

import (
	"fmt"
	"strings"
)

const (
	TransportStdio          = "stdio"
	TransportSSE            = "sse"
	TransportStreamableHTTP = "streamable_http"
	TransportHTTP           = "http"

	DefaultTimeoutSeconds = 30
)

// Config is the persisted plugin config stored in settings_plugins.config.
type Config struct {
	Servers []ServerConfig `json:"servers"`
}

// ServerConfig describes one managed MCP server.
type ServerConfig struct {
	Name           string            `json:"name"`
	Enabled        bool              `json:"enabled"`
	Transport      string            `json:"transport"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers"`
	TimeoutSeconds int               `json:"timeout_seconds"`
}

func DecodeConfig(raw map[string]any) (Config, error) {
	cfg := Config{}
	if raw == nil {
		return cfg, nil
	}

	serversRaw, ok := raw["servers"]
	if !ok {
		return cfg, nil
	}

	items, ok := serversRaw.([]any)
	if !ok {
		return Config{}, fmt.Errorf("mcp config: servers must be an array")
	}

	cfg.Servers = make([]ServerConfig, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return Config{}, fmt.Errorf("mcp config: servers[%d] must be an object", i)
		}
		sc, err := decodeServerConfig(i, m)
		if err != nil {
			return Config{}, err
		}
		cfg.Servers = append(cfg.Servers, sc)
	}

	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	seen := make(map[string]struct{}, len(c.Servers))
	for i, server := range c.Servers {
		if err := server.Validate(); err != nil {
			return fmt.Errorf("mcp config: servers[%d]: %w", i, err)
		}
		key := strings.ToLower(strings.TrimSpace(server.Name))
		if _, ok := seen[key]; ok {
			return fmt.Errorf("mcp config: duplicate server name %q", server.Name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (c Config) EnabledServers() []ServerConfig {
	result := make([]ServerConfig, 0, len(c.Servers))
	for _, server := range c.Servers {
		if server.Enabled {
			result = append(result, server)
		}
	}
	return result
}

func (c Config) HasEnabledServers() bool {
	for _, server := range c.Servers {
		if server.Enabled {
			return true
		}
	}
	return false
}

func (s ServerConfig) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("name is required")
	}

	switch s.Transport {
	case TransportStdio:
		if strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("command is required for stdio transport")
		}
	case TransportSSE, TransportStreamableHTTP, TransportHTTP:
		if strings.TrimSpace(s.URL) == "" {
			return fmt.Errorf("url is required for %s transport", s.Transport)
		}
	default:
		return fmt.Errorf("unsupported transport %q", s.Transport)
	}

	if s.TimeoutSeconds < 0 {
		return fmt.Errorf("timeout_seconds must be >= 0")
	}
	return nil
}

func (s ServerConfig) WithDefaults() ServerConfig {
	out := s
	out.Name = strings.TrimSpace(out.Name)
	out.Transport = strings.TrimSpace(out.Transport)
	if out.Env == nil {
		out.Env = map[string]string{}
	}
	if out.Headers == nil {
		out.Headers = map[string]string{}
	}
	if out.TimeoutSeconds == 0 {
		out.TimeoutSeconds = DefaultTimeoutSeconds
	}
	return out
}

func decodeServerConfig(index int, raw map[string]any) (ServerConfig, error) {
	cfg := ServerConfig{
		Enabled:        true,
		TimeoutSeconds: DefaultTimeoutSeconds,
	}

	cfg.Name = stringValue(raw, "name")
	cfg.Transport = stringValue(raw, "transport")
	cfg.Command = stringValue(raw, "command")
	cfg.URL = stringValue(raw, "url")
	cfg.Args = stringSliceValue(raw, "args")
	cfg.Env = stringMapValue(raw, "env")
	cfg.Headers = stringMapValue(raw, "headers")
	if enabled, ok := boolValue(raw, "enabled"); ok {
		cfg.Enabled = enabled
	}
	if timeout, ok := intValue(raw, "timeout_seconds"); ok {
		cfg.TimeoutSeconds = timeout
	}

	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return ServerConfig{}, fmt.Errorf("mcp config: servers[%d]: %w", index, err)
	}
	return cfg, nil
}

func stringValue(raw map[string]any, key string) string {
	v, _ := raw[key].(string)
	return strings.TrimSpace(v)
}

func boolValue(raw map[string]any, key string) (bool, bool) {
	v, ok := raw[key].(bool)
	return v, ok
}

func intValue(raw map[string]any, key string) (int, bool) {
	switch v := raw[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func stringSliceValue(raw map[string]any, key string) []string {
	items, ok := raw[key].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func stringMapValue(raw map[string]any, key string) map[string]string {
	items, ok := raw[key].(map[string]any)
	if !ok {
		return map[string]string{}
	}
	result := make(map[string]string, len(items))
	for k, v := range items {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	return result
}
