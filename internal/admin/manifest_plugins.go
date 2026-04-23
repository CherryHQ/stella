package admin

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/manifestplugins"
	"gopkg.in/yaml.v3"
)

type manifestFile struct {
	Plugins []manifestplugins.ManifestPlugin `yaml:"plugins"`
}

func loadMergedManifest() (*manifestplugins.Manifest, error) {
	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		return nil, err
	}
	user, err := manifestplugins.LoadUser(filepath.Join(config.AnnaHome(), "plugins.yaml"))
	if err != nil {
		return nil, err
	}
	return manifestplugins.Merge(builtin, user), nil
}

func (s *Server) listManifestPlugins(w http.ResponseWriter, r *http.Request) {
	merged, err := loadMergedManifest()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, merged.Plugins)
}

func (s *Server) saveManifestPlugins(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Plugins []manifestplugins.ManifestPlugin `json:"plugins"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	m := manifestplugins.Manifest{Plugins: req.Plugins}
	if err := manifestplugins.Validate(&m); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mf := manifestFile{Plugins: m.Plugins}
	data, err := yaml.Marshal(&mf)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	path := filepath.Join(config.AnnaHome(), "plugins.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, m.Plugins)
}

func (s *Server) syncManifestPlugins(w http.ResponseWriter, r *http.Request) {
	merged, err := loadMergedManifest()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result := manifestplugins.Reconcile(r.Context(), merged, config.AnnaHome())
	writeData(w, http.StatusOK, result)
}
