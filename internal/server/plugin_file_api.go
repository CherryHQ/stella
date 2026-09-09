package server

import (
	"io/fs"
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
)

func (s *Server) beginPluginFileAccess(w http.ResponseWriter, r *http.Request) (*pluginpkg.FileAccess, authz.Authority, bool) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil, authz.Authority{}, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil, authz.Authority{}, false
	}
	if s.pluginFiles == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin file service unavailable")
		return nil, authority, false
	}
	access, err := s.pluginFiles.Begin(authority)
	if err != nil {
		writePluginError(w, err)
		return nil, authority, false
	}
	return access, authority, true
}

func (s *Server) ListPlugins(w http.ResponseWriter, r *http.Request, params apiserver.ListPluginsParams) {
	access, _, ok := s.beginPluginFileAccess(w, r)
	if !ok {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	var scope *pluginpkg.Scope
	if params.Scope != nil {
		value := pluginpkg.Scope(*params.Scope)
		scope = &value
	}
	agentID := ""
	if params.AgentId != nil {
		agentID = *params.AgentId
	}
	resources, err := access.List(r.Context(), pluginpkg.ResourcePlugin, scope, agentID)
	if err != nil {
		writePluginError(w, err)
		return
	}
	page, next := nextPageTokenForRows(resources, limit, offset)
	items := make([]apitypes.PluginResource, 0, len(page))
	for _, resource := range page {
		item, err := pluginFileResourceView(resource, access.CanWrite(r.Context(), resource.Key))
		if err != nil {
			writePluginError(w, err)
			return
		}
		items = append(items, item)
	}
	writeData(w, http.StatusOK, apitypes.PluginList{Plugins: items, NextPageToken: stringPtrOrNil(next)})
}

func (s *Server) GetPlugin(w http.ResponseWriter, r *http.Request, pluginID string) {
	access, _, ok := s.beginPluginFileAccess(w, r)
	if !ok {
		return
	}
	resource, err := access.Get(r.Context(), pluginID)
	if err != nil {
		writePluginError(w, err)
		return
	}
	view, err := pluginFileResourceView(resource, access.CanWrite(r.Context(), resource.Key))
	if err != nil {
		writePluginError(w, err)
		return
	}
	writeData(w, http.StatusOK, view)
}

func (s *Server) CreatePlugin(w http.ResponseWriter, r *http.Request) {
	access, _, ok := s.beginPluginFileAccess(w, r)
	if !ok {
		return
	}
	var request apitypes.CreatePluginRequest
	if err := decodeStrictJSON(r, &request); err != nil || request.Name == "" || len(request.Files) == 0 {
		writeError(w, http.StatusBadRequest, "invalid plugin request")
		return
	}
	files := make(map[string]pluginpkg.ResourceFile, len(request.Files))
	for name, input := range request.Files {
		mode := fs.FileMode(0o644)
		if input.IsExecutable != nil && *input.IsExecutable {
			mode = 0o755
		}
		files[name] = pluginpkg.ResourceFile{Data: input.ContentBase64, Mode: mode}
	}
	resource, err := access.CreatePlugin(r.Context(), pluginpkg.Scope(request.Scope), stringPtrValue(request.AgentId), request.Name, files)
	if err != nil {
		writePluginError(w, err)
		return
	}
	view, err := pluginFileResourceView(resource, true)
	if err != nil {
		writePluginError(w, err)
		return
	}
	writeData(w, http.StatusCreated, view)
}

func (s *Server) ImportPluginPackage(w http.ResponseWriter, r *http.Request) {
	access, _, ok := s.beginPluginFileAccess(w, r)
	if !ok {
		return
	}
	var request apitypes.ImportPluginRequest
	if err := decodeStrictJSON(r, &request); err != nil || request.SourcePath == "" {
		writeError(w, http.StatusBadRequest, "invalid plugin request")
		return
	}
	resource, err := access.ImportPlugin(r.Context(), request.SourcePath, pluginpkg.Scope(request.Scope), stringPtrValue(request.AgentId), stringPtrValue(request.Name))
	if err != nil {
		writePluginError(w, err)
		return
	}
	view, err := pluginFileResourceView(resource, true)
	if err != nil {
		writePluginError(w, err)
		return
	}
	writeData(w, http.StatusCreated, view)
}

func (s *Server) UpdatePlugin(w http.ResponseWriter, r *http.Request, pluginID string) {
	access, _, ok := s.beginPluginFileAccess(w, r)
	if !ok {
		return
	}
	var request struct {
		IsEnabled              bool    `json:"is_enabled"`
		ExpectedSettingsDigest *string `json:"expected_settings_digest"`
	}
	if err := decodeStrictJSON(r, &request); err != nil || request.ExpectedSettingsDigest == nil {
		writeError(w, http.StatusBadRequest, "invalid plugin request")
		return
	}
	resource, err := access.SetEnabled(r.Context(), pluginID, *request.ExpectedSettingsDigest, request.IsEnabled)
	if err != nil {
		writePluginError(w, err)
		return
	}
	view, err := pluginFileResourceView(resource, access.CanWrite(r.Context(), resource.Key))
	if err != nil {
		writePluginError(w, err)
		return
	}
	writeData(w, http.StatusOK, view)
}

func (s *Server) DeletePlugin(w http.ResponseWriter, r *http.Request, pluginID string, params apiserver.DeletePluginParams) {
	access, _, ok := s.beginPluginFileAccess(w, r)
	if !ok {
		return
	}
	if params.ExpectedDigest == "" {
		writeError(w, http.StatusBadRequest, "expected_digest is required")
		return
	}
	if err := access.Delete(r.Context(), pluginID, params.ExpectedDigest); err != nil {
		writePluginError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) CopyPlugin(w http.ResponseWriter, r *http.Request, pluginID string) {
	access, _, ok := s.beginPluginFileAccess(w, r)
	if !ok {
		return
	}
	var request apitypes.CopyPluginRequest
	if err := decodeStrictJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid plugin request")
		return
	}
	resource, err := access.CopyPlugin(r.Context(), pluginID, pluginpkg.Scope(request.Scope), stringPtrValue(request.AgentId), stringPtrValue(request.Name))
	if err != nil {
		writePluginError(w, err)
		return
	}
	view, err := pluginFileResourceView(resource, true)
	if err != nil {
		writePluginError(w, err)
		return
	}
	writeData(w, http.StatusCreated, view)
}

func (s *Server) GetPluginFile(w http.ResponseWriter, r *http.Request, pluginID string, params apiserver.GetPluginFileParams) {
	access, _, ok := s.beginPluginFileAccess(w, r)
	if !ok {
		return
	}
	if params.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	file, digest, err := access.ReadFile(r.Context(), pluginID, params.Path)
	if err != nil {
		writePluginError(w, err)
		return
	}
	executable := file.Mode.Perm()&0o111 != 0
	writeData(w, http.StatusOK, apitypes.ResourceFileContent{Path: params.Path, ContentBase64: file.Data, IsExecutable: executable, ResourceDigest: &digest})
}

func (s *Server) UpdatePluginFile(w http.ResponseWriter, r *http.Request, pluginID string) {
	access, _, ok := s.beginPluginFileAccess(w, r)
	if !ok {
		return
	}
	var request apitypes.UpdatePluginFileRequest
	if err := decodeStrictJSON(r, &request); err != nil || request.Path == "" || request.ExpectedDigest == "" {
		writeError(w, http.StatusBadRequest, "invalid plugin file request")
		return
	}
	mode := fs.FileMode(0o644)
	if request.IsExecutable != nil && *request.IsExecutable {
		mode = 0o755
	}
	resource, err := access.UpdateFile(r.Context(), pluginID, request.Path, pluginpkg.ResourceFile{Data: request.ContentBase64, Mode: mode}, request.ExpectedDigest)
	if err != nil {
		writePluginError(w, err)
		return
	}
	view, err := pluginFileResourceView(resource, access.CanWrite(r.Context(), resource.Key))
	if err != nil {
		writePluginError(w, err)
		return
	}
	writeData(w, http.StatusOK, view)
}

func (s *Server) DeletePluginFile(w http.ResponseWriter, r *http.Request, pluginID string, params apiserver.DeletePluginFileParams) {
	access, _, ok := s.beginPluginFileAccess(w, r)
	if !ok {
		return
	}
	if params.Path == "" || params.ExpectedDigest == "" {
		writeError(w, http.StatusBadRequest, "path and expected_digest are required")
		return
	}
	if _, err := access.DeleteFile(r.Context(), pluginID, params.Path, params.ExpectedDigest); err != nil {
		writePluginError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
