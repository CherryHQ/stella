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

// resolveManifestPlugins loads the builtin manifest and overlays per-org
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
		writeError(w, http.StatusInternalServerError, err.Error())
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
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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
		// Persist an override only when the requested state differs from the default.
		if plugin.Enabled == def.Enabled {
			if err := s.store.DeleteManifestPluginOverride(r.Context(), plugin.ID); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			continue
		}
		enabled := plugin.Enabled
		if err := s.store.UpsertManifestPluginOverride(r.Context(), config.ManifestPluginOverride{
			PluginID: plugin.ID,
			Enabled:  &enabled,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	merged, err := s.resolveManifestPlugins(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result := manifestplugins.Reconcile(r.Context(), merged, config.StellaHome())
	writeData(w, http.StatusOK, result)
}
