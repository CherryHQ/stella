package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverStateStopped    = "stopped"
	serverStateStarting   = "starting"
	serverStateRunning    = "running"
	serverStateBackoff    = "backoff"
	serverStateSuppressed = "suppressed"

	defaultFailureThreshold = 3
	defaultBackoffBase      = time.Second
	defaultBackoffMax       = 30 * time.Second
)

type SupervisorConfig struct {
	FailureThreshold int
	BackoffBase      time.Duration
	BackoffMax       time.Duration
}

type ServerStatus struct {
	Name                string    `json:"name"`
	Transport           string    `json:"transport"`
	State               string    `json:"state"`
	Failures            int       `json:"failures"`
	Suppressed          bool      `json:"suppressed"`
	LastError           string    `json:"last_error,omitempty"`
	LastConnectedAt     time.Time `json:"last_connected_at,omitempty"`
	LastDiscoveredAt    time.Time `json:"last_discovered_at,omitempty"`
	DiscoveredToolCount int       `json:"discovered_tool_count"`
}

type serverRuntime struct {
	cfg     ServerConfig
	cancel  context.CancelFunc
	session Session
	status  ServerStatus
}

func (m *Manager) startServerLocked(parent context.Context, cfg ServerConfig) {
	ctx, cancel := context.WithCancel(parent)
	rt := &serverRuntime{
		cfg:    cfg,
		cancel: cancel,
		status: ServerStatus{Name: cfg.Name, Transport: cfg.Transport, State: serverStateStarting},
	}
	m.servers[cfg.Name] = rt
	go m.runServer(ctx, cfg)
}

func (m *Manager) stopServerLocked(name string) {
	rt, ok := m.servers[name]
	if !ok {
		m.clearServerToolsLocked(name)
		return
	}
	delete(m.servers, name)
	if rt.session != nil {
		_ = rt.session.Close()
	}
	rt.cancel()
	m.clearServerToolsLocked(name)
}

func (m *Manager) runServer(ctx context.Context, cfg ServerConfig) {
	for {
		m.updateServerStatus(cfg.Name, func(status *ServerStatus) {
			status.State = serverStateStarting
			status.Transport = cfg.Transport
		})

		session, err := m.dial(ctx, cfg)
		if err != nil {
			if !m.handleServerFailure(ctx, cfg, err) {
				return
			}
			continue
		}

		if err := m.refreshServerTools(ctx, cfg.Name, session); err != nil {
			_ = session.Close()
			if !m.handleServerFailure(ctx, cfg, err) {
				return
			}
			continue
		}

		m.setServerSession(cfg.Name, session)
		m.updateServerStatus(cfg.Name, func(status *ServerStatus) {
			status.State = serverStateRunning
			status.Failures = 0
			status.Suppressed = false
			status.LastError = ""
			status.LastConnectedAt = time.Now()
		})

		errCh := make(chan error, 1)
		go func() {
			errCh <- session.Wait()
		}()

		select {
		case <-ctx.Done():
			_ = session.Close()
			m.clearServerSession(cfg.Name)
			m.updateServerStatus(cfg.Name, func(status *ServerStatus) {
				status.State = serverStateStopped
			})
			return
		case err := <-errCh:
			m.clearServerSession(cfg.Name)
			m.clearServerTools(cfg.Name)
			if err == nil {
				m.updateServerStatus(cfg.Name, func(status *ServerStatus) {
					status.State = serverStateStopped
					status.LastError = ""
				})
				return
			}
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				m.updateServerStatus(cfg.Name, func(status *ServerStatus) {
					status.State = serverStateStopped
				})
				return
			}
			if !m.handleServerFailure(ctx, cfg, err) {
				return
			}
		}
	}
}

func (m *Manager) handleServerFailure(ctx context.Context, cfg ServerConfig, err error) bool {
	m.mu.Lock()
	rt, ok := m.servers[cfg.Name]
	if !ok {
		m.mu.Unlock()
		return false
	}
	rt.status.Failures++
	rt.status.LastError = err.Error()
	threshold := m.supervisor.FailureThreshold
	if threshold <= 0 {
		threshold = defaultFailureThreshold
	}
	failures := rt.status.Failures
	if failures >= threshold {
		rt.status.State = serverStateSuppressed
		rt.status.Suppressed = true
		m.mu.Unlock()
		m.clearServerTools(cfg.Name)
		return false
	}
	rt.status.State = serverStateBackoff
	rt.status.Suppressed = false
	base := m.supervisor.BackoffBase
	if base <= 0 {
		base = defaultBackoffBase
	}
	maxDelay := m.supervisor.BackoffMax
	if maxDelay <= 0 {
		maxDelay = defaultBackoffMax
	}
	delay := base << (failures - 1)
	if delay > maxDelay {
		delay = maxDelay
	}
	m.mu.Unlock()

	m.clearServerTools(cfg.Name)
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

func (m *Manager) refreshServerTools(ctx context.Context, serverName string, session Session) error {
	var all []*officialmcp.Tool
	var cursor string
	for {
		res, err := session.ListTools(ctx, &officialmcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return fmt.Errorf("list tools for %s: %w", serverName, err)
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}

	infos := make([]ToolInfo, 0, len(all))
	for _, tool := range all {
		if tool == nil || tool.Name == "" {
			continue
		}
		infos = append(infos, ToolInfo{
			ServerName:   serverName,
			ToolName:     tool.Name,
			Name:         displayName(tool),
			Description:  tool.Description,
			InputSchema:  anyToMap(tool.InputSchema),
			OutputSchema: anyToMap(tool.OutputSchema),
			Annotations:  annotationsToMap(tool.Annotations),
			Valid:        true,
		})
	}
	m.replaceServerTools(serverName, infos)
	m.updateServerStatus(serverName, func(status *ServerStatus) {
		status.LastDiscoveredAt = time.Now()
		status.DiscoveredToolCount = len(infos)
		status.LastError = ""
		if status.State != serverStateSuppressed {
			status.State = serverStateRunning
		}
	})
	return nil
}

func displayName(tool *officialmcp.Tool) string {
	if tool == nil {
		return ""
	}
	if tool.Title != "" {
		return tool.Title
	}
	if tool.Annotations != nil && tool.Annotations.Title != "" {
		return tool.Annotations.Title
	}
	return tool.Name
}

func annotationsToMap(ann *officialmcp.ToolAnnotations) map[string]any {
	if ann == nil {
		return nil
	}
	result := map[string]any{}
	if ann.Title != "" {
		result["title"] = ann.Title
	}
	if ann.DestructiveHint != nil {
		result["destructive_hint"] = *ann.DestructiveHint
	}
	result["idempotent_hint"] = ann.IdempotentHint
	if ann.OpenWorldHint != nil {
		result["open_world_hint"] = *ann.OpenWorldHint
	}
	result["read_only_hint"] = ann.ReadOnlyHint
	return result
}

func anyToMap(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		out[k] = val
	}
	return out
}

func (m *Manager) setServerSession(name string, session Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt, ok := m.servers[name]; ok {
		rt.session = session
	}
}

func (m *Manager) clearServerSession(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt, ok := m.servers[name]; ok {
		rt.session = nil
	}
}

func (m *Manager) updateServerStatus(name string, update func(*ServerStatus)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt, ok := m.servers[name]; ok {
		update(&rt.status)
	}
}

func (m *Manager) Statuses() []ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ServerStatus, 0, len(m.servers))
	for _, rt := range m.servers {
		result = append(result, rt.status)
	}
	return result
}
