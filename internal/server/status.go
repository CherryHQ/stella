package server

import (
	"context"
	"net/http"
	"runtime"
	"time"

	"github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/version"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
)

func (s *Server) GetStatus(w http.ResponseWriter, r *http.Request) {
	resp := types.StatusResponse{
		Status:         "ok",
		Version:        version.Version,
		SandboxBackend: statusStringPtr(config.ActiveSandboxBackend()),
	}
	if version.Commit != "" {
		resp.Commit = &version.Commit
	}
	if version.BuildDate != "" {
		resp.BuildDate = &version.BuildDate
	}
	if info := UserFromContext(r.Context()); info != nil && info.IsAdmin {
		authority, err := info.authority()
		if err != nil {
			writeData(w, http.StatusOK, resp)
			return
		}
		uptimeSeconds := int64(time.Since(s.startedAt).Seconds())
		resp.UptimeSeconds = &uptimeSeconds
		resp.Runtime = s.statusRuntime()
		resp.Database = s.statusDatabase(r.Context())
		resp.Plugins = s.statusPlugins(r.Context(), authority)
	}
	writeData(w, http.StatusOK, resp)
}

func statusStringPtr(value string) *string { return &value }

func (s *Server) statusRuntime() *types.StatusRuntime {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return &types.StatusRuntime{
		GoVersion:  runtime.Version(),
		Os:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Goroutines: runtime.NumGoroutine(),
		Memory: types.StatusMemory{
			AllocBytes:     int64(mem.Alloc),
			HeapAllocBytes: int64(mem.HeapAlloc),
			SysBytes:       int64(mem.Sys),
			NumGC:          int(mem.NumGC),
		},
	}
}

func (s *Server) statusDatabase(ctx context.Context) *types.StatusDatabase {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := s.pinger.Ping(ctx); err != nil {
		s.log.Error("database health check failed", "error", err)
		msg := "database unreachable"
		return &types.StatusDatabase{Status: "error", Error: &msg}
	}
	latency := float64(time.Since(start).Microseconds()) / 1000
	return &types.StatusDatabase{Status: "ok", LatencyMs: &latency}
}

func (s *Server) statusPlugins(ctx context.Context, authority authz.Authority) *types.StatusPlugins {
	if s == nil || s.pluginFiles == nil || !authority.Valid() || authority.Kind() != authz.ActorUser || !authority.IsAdmin() {
		return nil
	}
	access, err := s.pluginFiles.Begin(authority)
	if err != nil {
		return nil
	}
	// A deployment count covers system resources only; private or agent roots
	// have no single effective state for the whole deployment.
	scope := pluginpkg.ScopeSystem
	resources, err := access.List(ctx, pluginpkg.ResourcePlugin, &scope, "")
	if err != nil {
		return nil
	}
	out := types.StatusPlugins{Total: len(resources)}
	for _, resource := range resources {
		if !resource.Disabled && !resource.Forbidden && resource.Package != nil {
			out.Enabled++
		} else {
			out.Disabled++
		}
	}
	return &out
}
