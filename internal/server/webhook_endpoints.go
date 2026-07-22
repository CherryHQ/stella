package server

import (
	"net/http"
	"strings"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/webhook"
)

func (s *Server) GetWebhookEndpoint(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	endpoint, err := access.GetWebhookEndpoint(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	policy, err := access.WebhookPolicy(r.Context(), id, endpoint.Provider)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, webhookEndpointView(endpoint, policy))
}

func (s *Server) IssueWebhookEndpoint(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var req apitypes.IssueWebhookEndpointRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	provider := webhook.Provider(req.Provider)
	result, err := access.IssueWebhookEndpoint(r.Context(), id, req.OwnerUserId, provider)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	policy, err := access.WebhookPolicy(r.Context(), id, provider)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusCreated, webhookSecretView(s.baseURL, result, policy))
}

func (s *Server) RotateWebhookEndpoint(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	result, err := access.RotateWebhookEndpoint(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	policy, err := access.WebhookPolicy(r.Context(), id, result.Endpoint.Provider)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, webhookSecretView(s.baseURL, result, policy))
}

func (s *Server) DeleteWebhookEndpoint(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	if err := access.DeleteWebhookEndpoint(r.Context(), id); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeNoContent(w)
}

func webhookSecretView(baseURL string, result webhook.IssueResult, policy webhook.GitHubPolicy) apitypes.WebhookEndpointSecret {
	view := apitypes.WebhookEndpointSecret{
		Endpoint: webhookEndpointView(result.Endpoint, policy),
		Url:      strings.TrimRight(baseURL, "/") + "/webhooks/" + result.Capability,
	}
	if result.GitHubWebhookSecret != "" {
		view.GithubWebhookSecret = &result.GitHubWebhookSecret
	}
	return view
}

// webhookEndpointView is intentionally built from webhook.Endpoint, whose type
// has no token verifier or provider ciphertext fields. Stable GET responses
// cannot leak one-time credentials by construction.
func webhookEndpointView(endpoint webhook.Endpoint, policy webhook.GitHubPolicy) apitypes.WebhookEndpoint {
	return apitypes.WebhookEndpoint{
		Id:                 &endpoint.ID,
		ChannelId:          &endpoint.ChannelID,
		OwnerUserId:        &endpoint.OwnerUserID,
		Provider:           apitypes.WebhookProvider(endpoint.Provider),
		TokenLast4:         &endpoint.TokenLast4,
		CreatedAt:          &endpoint.CreatedAt,
		UpdatedAt:          &endpoint.UpdatedAt,
		RotatedAt:          endpoint.RotatedAt,
		GithubEvents:       &policy.Events,
		GithubRepositories: &policy.Repositories,
	}
}
