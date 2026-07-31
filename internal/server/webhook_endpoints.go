package server

import (
	"net/http"
	"strings"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/webhook"
)

func (s *Server) GetWebhookEndpoint(w http.ResponseWriter, r *http.Request, channelID string) {
	access, ok := s.beginChannels(w, r)
	if !ok {
		return
	}
	endpoint, err := access.GetWebhookEndpoint(r.Context(), channelID)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, webhookEndpointView(endpoint))
}

func (s *Server) CreateWebhookEndpoint(w http.ResponseWriter, r *http.Request, channelID string) {
	access, ok := s.beginChannels(w, r)
	if !ok {
		return
	}
	var req apitypes.CreateWebhookEndpointRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	result, err := access.CreateWebhookEndpoint(r.Context(), channelID, webhook.Provider(req.Provider))
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusCreated, s.webhookEndpointSecretView(result))
}

func (s *Server) RotateWebhookEndpoint(w http.ResponseWriter, r *http.Request, channelID string) {
	access, ok := s.beginChannels(w, r)
	if !ok {
		return
	}
	var req apitypes.RotateWebhookEndpointRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	result, err := access.RotateWebhookEndpoint(r.Context(), channelID, req.Etag)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, s.webhookEndpointSecretView(result))
}

func (s *Server) DeleteWebhookEndpoint(w http.ResponseWriter, r *http.Request, channelID string) {
	access, ok := s.beginChannels(w, r)
	if !ok {
		return
	}
	if err := access.DeleteWebhookEndpoint(r.Context(), channelID); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeNoContent(w)
}

// webhookEndpointView builds the stable, redacted resource. It is intentionally
// constructed from webhook.Endpoint, whose type has no verifier field, so a Get
// response cannot leak a capability by construction. No url is populated.
func webhookEndpointView(e webhook.Endpoint) apitypes.WebhookEndpoint {
	return apitypes.WebhookEndpoint{
		ChannelId:   e.ChannelID,
		OwnerUserId: e.OwnerUserID,
		Provider:    apitypes.WebhookEndpointProvider(e.Provider),
		TokenLast4:  e.TokenLast4,
		Etag:        e.ETag(),
		CreatedAt:   e.CreatedAt,
		UpdatedAt:   e.UpdatedAt,
		RotatedAt:   e.RotatedAt,
	}
}

// webhookEndpointSecretView adds the one-time capability url disclosed only by
// Create and Rotate. The url is never persisted and cannot be re-derived.
func (s *Server) webhookEndpointSecretView(result webhook.IssueResult) apitypes.WebhookEndpoint {
	view := webhookEndpointView(result.Endpoint)
	url := strings.TrimRight(s.baseURL, "/") + "/webhooks/" + result.Capability
	view.Url = &url
	return view
}
