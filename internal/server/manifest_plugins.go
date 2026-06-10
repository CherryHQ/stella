package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/manifestplugins"
)

type manifestPluginsResponse struct {
	Plugins        []manifestplugins.ManifestPlugin        `json:"plugins"`
	OAuthProviders []manifestplugins.ManifestOAuthProvider `json:"oauth_providers"`
}

// resolveManifestPlugins loads the builtin manifest and overlays
// overrides from the DB. Each override may carry a full plugin definition
// (Config JSON) plus an Enabled toggle. When Config is present the entire
// plugin definition is replaced; when only Enabled is set the builtin
// definition is kept with the enabled flag toggled.
func (s *Server) resolveManifestPlugins(r *http.Request) (*manifestplugins.Manifest, error) {
	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		return nil, err
	}
	if s.store == nil {
		return builtin, nil
	}
	overrides, err := s.store.ListManifestPluginOverrides(r.Context())
	if err != nil {
		return nil, err
	}
	byID := make(map[string]config.ManifestPluginOverride, len(overrides))
	for _, ov := range overrides {
		byID[ov.PluginID] = ov
	}
	seen := make(map[string]bool, len(builtin.Plugins))
	for i := range builtin.Plugins {
		id := builtin.Plugins[i].ID
		seen[id] = true
		ov, ok := byID[id]
		if !ok {
			continue
		}
		if ov.Config != "" {
			var p manifestplugins.ManifestPlugin
			if err := json.Unmarshal([]byte(ov.Config), &p); err != nil {
				s.log.Warn("ignoring corrupt plugin config override", "plugin", id, "error", err)
			} else {
				p.ID = id
				p.Enabled = builtin.Plugins[i].Enabled
				builtin.Plugins[i] = p
			}
		}
		if ov.Enabled != nil {
			builtin.Plugins[i].Enabled = *ov.Enabled
		}
	}
	for _, ov := range overrides {
		if seen[ov.PluginID] || ov.Config == "" {
			continue
		}
		var p manifestplugins.ManifestPlugin
		if err := json.Unmarshal([]byte(ov.Config), &p); err != nil {
			s.log.Warn("ignoring corrupt custom plugin config", "plugin", ov.PluginID, "error", err)
			continue
		}
		p.ID = ov.PluginID
		if ov.Enabled != nil {
			p.Enabled = *ov.Enabled
		}
		builtin.Plugins = append(builtin.Plugins, p)
	}
	return builtin, nil
}

func (s *Server) ListManifestPlugins(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	merged, err := s.resolveManifestPlugins(r)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, manifestPluginsResponse{Plugins: merged.Plugins, OAuthProviders: merged.OAuthProviders})
}

func (s *Server) SaveManifestPlugins(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	var req struct {
		Plugins []manifestplugins.ManifestPlugin `json:"plugins"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	builtinByID := make(map[string]manifestplugins.ManifestPlugin, len(builtin.Plugins))
	for _, p := range builtin.Plugins {
		builtinByID[p.ID] = p
	}

	for _, plugin := range req.Plugins {
		def, isBuiltin := builtinByID[plugin.ID]

		// Essential builtin plugins back core tools (rg/fd → Grep/Glob); refuse to
		// disable them so an admin can't silently break the harness.
		if isBuiltin && def.Essential && !plugin.Enabled {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("plugin %q is essential and cannot be disabled", plugin.ID))
			return
		}

		existing, _, err := s.store.GetManifestPluginOverride(r.Context(), plugin.ID)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}

		// A toggle-only request sends {id, enabled} with zero-valued Kind.
		// Don't treat that as a config override — it would clobber the existing definition.
		isFullDefinition := plugin.Kind != ""

		var cfgStr string
		if isFullDefinition {
			configJSON := manifestPluginConfigJSON(plugin)
			if !isBuiltin || configJSON != manifestPluginConfigJSON(def) {
				candidate := &manifestplugins.Manifest{
					OAuthProviders: builtin.OAuthProviders,
					Plugins:        []manifestplugins.ManifestPlugin{plugin},
				}
				if err := manifestplugins.Validate(candidate); err != nil {
					writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid plugin %q: %v", plugin.ID, err))
					return
				}
				cfgStr = configJSON
			}
		} else {
			cfgStr = existing.Config
		}

		enabledDiffers := isBuiltin && plugin.Enabled != def.Enabled
		var enabled *bool
		if enabledDiffers {
			e := plugin.Enabled
			enabled = &e
		}
		if !isBuiltin {
			e := plugin.Enabled
			enabled = &e
		}

		needsRow := enabled != nil || existing.SessionEnvVaultKey != "" || cfgStr != ""
		if !needsRow {
			if err := s.store.DeleteManifestPluginOverride(r.Context(), plugin.ID); err != nil {
				s.writeInternalError(w, err)
				return
			}
			continue
		}

		if err := s.store.UpsertManifestPluginOverride(r.Context(), config.ManifestPluginOverride{
			PluginID:           plugin.ID,
			Enabled:            enabled,
			SessionEnvVaultKey: existing.SessionEnvVaultKey,
			Config:             cfgStr,
		}); err != nil {
			s.writeInternalError(w, err)
			return
		}
	}

	merged, err := s.resolveManifestPlugins(r)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	s.pluginHost.RegisterManifestPlugins(merged)
	if s.poolManager != nil {
		if err := s.poolManager.ReloadPluginTools(r.Context()); err != nil {
			s.log.Error("failed to reload manifest plugin tools", "error", err)
		}
		if err := s.poolManager.ReloadPluginHooks(r.Context()); err != nil {
			s.log.Error("failed to reload manifest plugin hooks", "error", err)
		}
	}
	writeData(w, http.StatusOK, manifestPluginsResponse{Plugins: merged.Plugins, OAuthProviders: merged.OAuthProviders})
}

// manifestPluginConfigJSON serializes a ManifestPlugin (excluding Enabled and ID)
// to a canonical JSON string for storage and comparison.
func manifestPluginConfigJSON(p manifestplugins.ManifestPlugin) string {
	type configOnly struct {
		Kind          string                               `json:"kind"`
		Name          string                               `json:"name"`
		DisplayName   string                               `json:"display_name"`
		Description   string                               `json:"description"`
		Category      string                               `json:"category,omitempty"`
		Essential     bool                                 `json:"essential,omitempty"`
		Prompt        string                               `json:"prompt,omitempty"`
		Binaries      []manifestplugins.ManifestBinary     `json:"binaries,omitempty"`
		Skills        []manifestplugins.ManifestSkill      `json:"skills,omitempty"`
		SessionEnvs   []manifestplugins.ManifestSessionEnv `json:"session_env,omitempty"`
		OAuthProvider string                               `json:"oauth_provider,omitempty"`
	}
	// Normalize empty slices to nil so omitempty produces stable JSON
	// regardless of whether the source was nil or [].
	binaries := p.Binaries
	if len(binaries) == 0 {
		binaries = nil
	}
	skills := p.Skills
	if len(skills) == 0 {
		skills = nil
	}
	sessionEnvs := p.SessionEnvs
	if len(sessionEnvs) == 0 {
		sessionEnvs = nil
	}
	data, _ := json.Marshal(configOnly{
		Kind:          p.Kind,
		Name:          p.Name,
		DisplayName:   p.DisplayName,
		Description:   p.Description,
		Category:      p.Category,
		Essential:     p.Essential,
		Prompt:        p.Prompt,
		Binaries:      binaries,
		Skills:        skills,
		SessionEnvs:   sessionEnvs,
		OAuthProvider: p.OAuthProvider,
	})
	return string(data)
}

func (s *Server) SyncManifestPlugins(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	merged, err := s.resolveManifestPlugins(r)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	result := manifestplugins.Reconcile(r.Context(), merged, config.StellaHome())
	writeData(w, http.StatusOK, result)
}
