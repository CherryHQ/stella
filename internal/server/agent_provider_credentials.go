package server

import (
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent/providercred"
)

// ListAgentProviderCredentials returns only write-safe credential state. Agent
// Management owns the Read authorization and persistence boundary; the handler
// never reaches the store, cipher, or control plane directly.
func (s *Server) ListAgentProviderCredentials(w http.ResponseWriter, r *http.Request, id string, params apiserver.ListAgentProviderCredentialsParams) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	metadata, err := s.agentManagement.ListProviderCredentials(r.Context(), authority, id)
	if err != nil {
		s.writeAgentCredentialError(w, err)
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination")
		return
	}
	start := min(offset, len(metadata))
	end := min(start+limit, len(metadata))
	items := make([]apitypes.AgentProviderCredential, end-start)
	for i, item := range metadata[start:end] {
		items[i] = agentProviderCredentialToAPI(item)
	}
	var nextPageToken *string
	if end < len(metadata) {
		token := encodeOffsetToken(end)
		nextPageToken = &token
	}
	writeData(w, http.StatusOK, apitypes.AgentProviderCredentialList{
		ProviderCredentials: items,
		NextPageToken:       nextPageToken,
		TotalSize:           len(metadata),
	})
}

// GetAgentProviderCredential returns one safe metadata resource.
func (s *Server) GetAgentProviderCredential(w http.ResponseWriter, r *http.Request, id string, providerId string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	metadata, err := s.agentManagement.ListProviderCredentials(r.Context(), authority, id)
	if err != nil {
		s.writeAgentCredentialError(w, err)
		return
	}
	for _, item := range metadata {
		if item.ProviderID == providerId {
			writeData(w, http.StatusOK, agentProviderCredentialToAPI(item))
			return
		}
	}
	writeError(w, http.StatusNotFound, "provider credential not found")
}

// UpdateAgentProviderCredential accepts a write-only key and delegates all
// validation, authorization, encryption, persistence, and invalidation to Agent
// Management. No request value is logged or used in an error response.
func (s *Server) UpdateAgentProviderCredential(w http.ResponseWriter, r *http.Request, id string, providerId string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	var req apiserver.UpdateAgentProviderCredentialJSONRequestBody
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	input := providercred.Input{ProviderID: providerId, APIKey: stringValue(req.ApiKey)}
	metadata, err := s.agentManagement.SetProviderCredential(r.Context(), authority, id, input)
	if err != nil {
		s.writeAgentCredentialError(w, err)
		return
	}
	writeData(w, http.StatusOK, agentProviderCredentialToAPI(metadata))
}

func agentProviderCredentialToAPI(item providercred.Metadata) apitypes.AgentProviderCredential {
	providerID := item.ProviderID
	hasAPIKey := item.HasAPIKey
	updatedAt := item.UpdatedAt.UTC()
	return apitypes.AgentProviderCredential{
		Id:        &providerID,
		HasApiKey: &hasAPIKey,
		UpdatedAt: &updatedAt,
	}
}

// DeleteAgentProviderCredential idempotently restores the deployment-global key
// fallback for this Agent and canonical Provider.
func (s *Server) DeleteAgentProviderCredential(w http.ResponseWriter, r *http.Request, id string, providerId string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	if err := s.agentManagement.DeleteProviderCredential(r.Context(), authority, id, providerId); err != nil {
		s.writeAgentCredentialError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) writeAgentCredentialError(w http.ResponseWriter, err error) {
	code, message := agentManagementError(err)
	if code == http.StatusInternalServerError {
		s.writeInternalError(w, err)
		return
	}
	writeError(w, code, message)
}
