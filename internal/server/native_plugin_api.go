package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
)

const maxNativePolicyBodyBytes = 16 << 10

func (s *Server) ListNativePlugins(w http.ResponseWriter, r *http.Request, params apiserver.ListNativePluginsParams) {
	if requireAdmin(w, r) == nil {
		return
	}
	if s.nativePolicy == nil {
		writeError(w, http.StatusServiceUnavailable, "native plugin policy unavailable")
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	ids := slices.Clone(s.nativePolicy.NativeIDs())
	items := make([]apitypes.NativePlugin, 0, len(ids))
	for _, id := range ids {
		enabled, err := s.nativePolicy.GlobalEnabled(r.Context(), id)
		if err != nil {
			writeNativePolicyError(w, err)
			return
		}
		items = append(items, nativePluginView(id, enabled))
	}
	page, next := nextPageTokenForRows(nativePageWindow(items, limit, offset), limit, offset)
	if page == nil {
		page = []apitypes.NativePlugin{}
	}
	writeData(w, http.StatusOK, apitypes.NativePluginList{NativePlugins: page, NextPageToken: stringPtrOrNil(next)})
}

func nativePluginID(kind, name string) string {
	return strings.TrimSpace(kind) + "/" + strings.TrimSpace(name)
}

func (s *Server) nativeAdmin(w http.ResponseWriter, r *http.Request, id string) bool {
	if requireAdmin(w, r) == nil {
		return false
	}
	if s.nativePolicy == nil {
		writeError(w, http.StatusServiceUnavailable, "native plugin policy unavailable")
		return false
	}
	if !s.nativePolicy.IsRegistered(id) {
		writeError(w, http.StatusNotFound, "native plugin not found")
		return false
	}
	return true
}

func nativePluginView(id string, enabled bool) apitypes.NativePlugin {
	return apitypes.NativePlugin{Id: id, IsEnabled: enabled}
}

// nativePageWindow turns an in-memory result set into the limit+1 probe window
// expected by nextPageTokenForRows. Native plugin catalogs and denials are
// small, already ordered snapshots rather than DB queries with LIMIT/OFFSET.
func nativePageWindow[T any](rows []T, limit, offset int) []T {
	if offset >= len(rows) {
		return nil
	}
	return rows[offset:min(len(rows), offset+limit+1)]
}

func nativeDenyView(deny pluginpkg.NativeAgentDeny) apitypes.NativeAgentDeny {
	return apitypes.NativeAgentDeny{NativeId: deny.NativeID, AgentId: deny.AgentID, IsDenied: true}
}

func (s *Server) GetNativePlugin(w http.ResponseWriter, r *http.Request, kind, name string) {
	id := nativePluginID(kind, name)
	if !s.nativeAdmin(w, r, id) {
		return
	}
	enabled, err := s.nativePolicy.GlobalEnabled(r.Context(), id)
	if err != nil {
		writeNativePolicyError(w, err)
		return
	}
	writeData(w, http.StatusOK, nativePluginView(id, enabled))
}

func (s *Server) UpdateNativePlugin(w http.ResponseWriter, r *http.Request, kind, name string) {
	id := nativePluginID(kind, name)
	if !s.nativeAdmin(w, r, id) {
		return
	}
	var request struct {
		IsEnabled *bool `json:"is_enabled"`
	}
	if err := decodeNativeJSON(w, r, &request); err != nil || request.IsEnabled == nil {
		writeError(w, http.StatusBadRequest, "is_enabled is required and must be boolean")
		return
	}
	if err := s.nativePolicy.SetGlobalEnabled(r.Context(), id, *request.IsEnabled); err != nil {
		writeNativePolicyError(w, err)
		return
	}
	writeData(w, http.StatusOK, nativePluginView(id, *request.IsEnabled))
}

func decodeNativeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNativePolicyBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (s *Server) ListNativePluginAgentDenials(w http.ResponseWriter, r *http.Request, kind, name string, params apiserver.ListNativePluginAgentDenialsParams) {
	id := nativePluginID(kind, name)
	if !s.nativeAdmin(w, r, id) {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	denials, err := s.nativePolicy.ListAgentDenials(r.Context(), id)
	if err != nil {
		writeNativePolicyError(w, err)
		return
	}
	page, next := nextPageTokenForRows(nativePageWindow(denials, limit, offset), limit, offset)
	items := make([]apitypes.NativeAgentDeny, 0, len(page))
	for _, deny := range page {
		items = append(items, nativeDenyView(deny))
	}
	writeData(w, http.StatusOK, apitypes.NativeAgentDenyList{Denials: items, NextPageToken: stringPtrOrNil(next)})
}

func (s *Server) CreateNativePluginAgentDeny(w http.ResponseWriter, r *http.Request, kind, name string) {
	id := nativePluginID(kind, name)
	if !s.nativeAdmin(w, r, id) {
		return
	}
	var request struct {
		AgentID *string `json:"agent_id"`
	}
	if err := decodeNativeJSON(w, r, &request); err != nil || request.AgentID == nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	agentID := strings.TrimSpace(*request.AgentID)
	if strings.TrimSpace(agentID) == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	info := UserFromContext(r.Context())
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if err := s.agentAccess.Authorize(r.Context(), authority, agentID, authz.ActionRead); err != nil {
		if errors.Is(err, agentaccess.ErrNotFound) || errors.Is(err, agentaccess.ErrForbidden) {
			writeError(w, http.StatusNotFound, "Agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "Agent lookup failed")
		return
	}
	if err := s.nativePolicy.SetAgentDeny(r.Context(), id, agentID); err != nil {
		writeNativePolicyError(w, err)
		return
	}
	writeData(w, http.StatusCreated, nativeDenyView(pluginpkg.NativeAgentDeny{NativeID: id, AgentID: agentID}))
}

func (s *Server) GetNativePluginAgentDeny(w http.ResponseWriter, r *http.Request, kind, name, agentID string) {
	id := nativePluginID(kind, name)
	if !s.nativeAdmin(w, r, id) {
		return
	}
	denied, err := s.nativePolicy.AgentDenied(r.Context(), id, agentID)
	if err != nil {
		writeNativePolicyError(w, err)
		return
	}
	if !denied {
		writeError(w, http.StatusNotFound, "native plugin Agent deny not found")
		return
	}
	writeData(w, http.StatusOK, nativeDenyView(pluginpkg.NativeAgentDeny{NativeID: id, AgentID: agentID}))
}

func (s *Server) AllowNativePluginAgent(w http.ResponseWriter, r *http.Request, kind, name, agentID string) {
	id := nativePluginID(kind, name)
	if !s.nativeAdmin(w, r, id) {
		return
	}
	if err := s.nativePolicy.DeleteAgentDeny(r.Context(), id, agentID); err != nil {
		writeNativePolicyError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeNativePolicyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pluginpkg.ErrUnknownNativeID):
		writeError(w, http.StatusNotFound, "native plugin not found")
	case errors.Is(err, pluginpkg.ErrNativePolicyUnavailable):
		writeError(w, http.StatusServiceUnavailable, "native plugin policy unavailable")
	case errors.Is(err, pluginpkg.ErrNativeAgentDenyExists):
		writeError(w, http.StatusConflict, "native plugin Agent deny already exists")
	case errors.Is(err, pluginpkg.ErrNativeAgentNotFound):
		writeError(w, http.StatusNotFound, "Agent not found")
	default:
		writeError(w, http.StatusInternalServerError, "native plugin policy error")
	}
}
