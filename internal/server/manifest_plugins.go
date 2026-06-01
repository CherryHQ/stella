package server

import (
	"net/http"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/manifestplugins"
)

type manifestPluginsResponse struct {
	Plugins        []manifestplugins.ManifestPlugin        `json:"plugins"`
	OAuthProviders []manifestplugins.ManifestOAuthProvider `json:"oauth_providers"`
}

// resolveManifestPlugins loads the builtin manifest and overlays
// overrides from the DB. Each manifest plugin's Enabled flag reflects the
// override row when present; the default otherwise. SessionEnvs are not
// touched here — those are resolved at agent runtime against the vault.
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
	for i := range builtin.Plugins {
		if ov, ok := byID[builtin.Plugins[i].ID]; ok && ov.Enabled != nil {
			builtin.Plugins[i].Enabled = *ov.Enabled
		}
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
		def, ok := builtinByID[plugin.ID]
		if !ok {
			// Unknown plugin (not in manifest); skip silently to avoid orphan DB rows.
			continue
		}
		// The Save payload only carries the enable toggle; the
		// session_env_vault_key is a separate override dimension. Read the
		// existing row so we preserve that binding instead of clobbering it.
		existing, _, err := s.store.GetManifestPluginOverride(r.Context(), plugin.ID)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		// Drop the row only when nothing is left to override: the requested
		// state matches the default and no session env binding is set.
		if plugin.Enabled == def.Enabled && existing.SessionEnvVaultKey == "" {
			if err := s.store.DeleteManifestPluginOverride(r.Context(), plugin.ID); err != nil {
				s.writeInternalError(w, err)
				return
			}
			continue
		}
		// Store an explicit enabled pointer only when it diverges from the
		// default; nil falls back to the manifest default at resolve time.
		var enabled *bool
		if plugin.Enabled != def.Enabled {
			e := plugin.Enabled
			enabled = &e
		}
		if err := s.store.UpsertManifestPluginOverride(r.Context(), config.ManifestPluginOverride{
			PluginID:           plugin.ID,
			Enabled:            enabled,
			SessionEnvVaultKey: existing.SessionEnvVaultKey,
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
