package server

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"

	"gopkg.in/yaml.v3"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/manifestplugins"
)

type manifestFile struct {
	Plugins []manifestplugins.ManifestPlugin `yaml:"plugins"`
}

type manifestPluginsResponse struct {
	Plugins        []manifestplugins.ManifestPlugin        `json:"plugins"`
	OAuthProviders []manifestplugins.ManifestOAuthProvider `json:"oauth_providers"`
}

func loadMergedManifest() (*manifestplugins.Manifest, error) {
	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		return nil, err
	}
	user, err := manifestplugins.LoadUser(filepath.Join(config.StellaHome(), "plugins.yaml"))
	if err != nil {
		return nil, err
	}
	return manifestplugins.Merge(builtin, user), nil
}

func (s *Server) ListManifestPlugins(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	merged, err := loadMergedManifest()
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
	userPlugins := userManifestOverrides(builtin.Plugins, req.Plugins)
	userManifest := manifestplugins.Manifest{Plugins: userPlugins}
	merged := manifestplugins.Merge(builtin, &userManifest)
	if err := manifestplugins.Validate(merged); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mf := manifestFile{Plugins: userPlugins}
	data, err := yaml.Marshal(&mf)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	path := filepath.Join(config.StellaHome(), "plugins.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
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
	_ = manifestplugins.Reconcile(r.Context(), merged, config.StellaHome())
	writeData(w, http.StatusOK, manifestPluginsResponse{Plugins: merged.Plugins, OAuthProviders: merged.OAuthProviders})
}

func (s *Server) SyncManifestPlugins(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	merged, err := loadMergedManifest()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result := manifestplugins.Reconcile(r.Context(), merged, config.StellaHome())
	writeData(w, http.StatusOK, result)
}

func userManifestOverrides(builtinPlugins []manifestplugins.ManifestPlugin, requestedPlugins []manifestplugins.ManifestPlugin) []manifestplugins.ManifestPlugin {
	builtinByID := make(map[string]manifestplugins.ManifestPlugin, len(builtinPlugins))
	for _, plugin := range builtinPlugins {
		builtinByID[plugin.ID] = plugin
	}

	out := make([]manifestplugins.ManifestPlugin, 0, len(requestedPlugins))
	for _, plugin := range requestedPlugins {
		builtin, ok := builtinByID[plugin.ID]
		if ok && reflect.DeepEqual(plugin, builtin) {
			continue
		}
		out = append(out, plugin)
	}
	return out
}
