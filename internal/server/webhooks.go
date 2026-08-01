package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/webhook"
)

func (s *Server) ListWebhooks(w http.ResponseWriter, r *http.Request, params apiserver.ListWebhooksParams) {
	userID, svc, ok := s.webhookAccess(w, r)
	if !ok {
		return
	}
	limit, offset, err := parsePageParams(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	items, err := svc.List(r.Context(), userID, int32(limit+1), int32(offset))
	if err != nil {
		s.writeWebhookError(w, err)
		return
	}
	items, next := nextPageTokenForRows(items, limit, offset)
	out := make([]apitypes.Webhook, len(items))
	for i := range items {
		out[i] = webhookView(items[i])
	}
	var token *string
	if next != "" {
		token = &next
	}
	writeData(w, http.StatusOK, apitypes.WebhookList{Webhooks: out, NextPageToken: token})
}

func (s *Server) CreateWebhook(w http.ResponseWriter, r *http.Request) {
	userID, svc, ok := s.webhookAccess(w, r)
	if !ok {
		return
	}
	var req apitypes.CreateWebhookRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	isEnabled := true
	if req.IsEnabled != nil {
		isEnabled = *req.IsEnabled
	}
	wait := webhook.DefaultWaitTimeoutSeconds
	if req.WaitTimeoutSeconds != nil {
		wait = *req.WaitTimeoutSeconds
	}
	run := webhook.DefaultMaxRunTimeoutSeconds
	if req.MaxRunTimeoutSeconds != nil {
		run = *req.MaxRunTimeoutSeconds
	}
	provider := webhook.ProviderGeneric
	if req.Provider != nil {
		provider = webhook.Provider(*req.Provider)
	}
	waitSeconds, err := webhookTimeout(wait, webhook.WaitTimeoutCeilingSeconds)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	runSeconds, err := webhookTimeout(run, webhook.RunTimeoutCeilingSeconds)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := svc.Create(r.Context(), webhook.CreateRequest{UserID: userID, Name: req.Name, AgentID: req.AgentId, Provider: provider, IsEnabled: isEnabled, WaitTimeoutSeconds: waitSeconds, MaxRunTimeoutSeconds: runSeconds})
	if err != nil {
		s.writeWebhookError(w, err)
		return
	}
	view := s.webhookSecretView(result)
	writeData(w, http.StatusCreated, view)
}

func (s *Server) GetWebhook(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	userID, svc, ok := s.webhookAccess(w, r)
	if !ok {
		return
	}
	item, err := svc.Get(r.Context(), userID, id.String())
	if err != nil {
		s.writeWebhookError(w, err)
		return
	}
	writeData(w, http.StatusOK, webhookView(item))
}

func (s *Server) UpdateWebhook(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	userID, svc, ok := s.webhookAccess(w, r)
	if !ok {
		return
	}
	req, err := decodeWebhookUpdate(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	waitSeconds, err := webhookTimeoutPtr(req.WaitTimeoutSeconds, webhook.WaitTimeoutCeilingSeconds)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	runSeconds, err := webhookTimeoutPtr(req.MaxRunTimeoutSeconds, webhook.RunTimeoutCeilingSeconds)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	item, err := svc.Update(r.Context(), webhook.UpdateRequest{ID: id.String(), UserID: userID, Name: req.Name, AgentID: req.AgentId, IsEnabled: req.IsEnabled, WaitTimeoutSeconds: waitSeconds, MaxRunTimeoutSeconds: runSeconds})
	if err != nil {
		s.writeWebhookError(w, err)
		return
	}
	writeData(w, http.StatusOK, webhookView(item))
}

func (s *Server) DeleteWebhook(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	userID, svc, ok := s.webhookAccess(w, r)
	if !ok {
		return
	}
	deleted, err := svc.Delete(r.Context(), userID, id.String())
	if err != nil {
		s.writeWebhookError(w, err)
		return
	}
	if !deleted {
		s.writeWebhookError(w, webhook.ErrNotFound)
		return
	}
	if s.webhookLimiter != nil {
		s.webhookLimiter.remove(id.String())
	}
	writeNoContent(w)
}

func (s *Server) RotateWebhook(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	userID, svc, ok := s.webhookAccess(w, r)
	if !ok {
		return
	}
	var req apitypes.RotateWebhookRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	result, err := svc.Rotate(r.Context(), userID, id.String(), req.Etag)
	if err != nil {
		s.writeWebhookError(w, err)
		return
	}
	writeData(w, http.StatusOK, s.webhookSecretView(result))
}

func (s *Server) webhookAccess(w http.ResponseWriter, r *http.Request) (string, *webhook.Service, bool) {
	info := requireAuth(w, r)
	if info == nil {
		return "", nil, false
	}
	if s.webhooks == nil {
		writeError(w, http.StatusServiceUnavailable, "webhook service unavailable")
		return "", nil, false
	}
	return info.UserID, s.webhooks, true
}

func (s *Server) writeWebhookError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, webhook.ErrNotFound):
		writeError(w, http.StatusNotFound, "webhook not found")
	case errors.Is(err, webhook.ErrStaleETag):
		writeError(w, http.StatusConflict, "webhook changed since it was read; refresh and retry")
	case errors.Is(err, webhook.ErrUserInactive):
		writeError(w, http.StatusForbidden, "user is inactive")
	case errors.Is(err, webhook.ErrUserAgentForbidden):
		writeError(w, http.StatusForbidden, "you cannot use the selected agent")
	case errors.Is(err, webhook.ErrInvalidID):
		writeError(w, http.StatusBadRequest, "invalid webhook id")
	case errors.Is(err, webhook.ErrInvalidUserID):
		writeError(w, http.StatusBadRequest, "invalid user identity")
	case errors.Is(err, webhook.ErrInvalidName):
		writeError(w, http.StatusBadRequest, "name is required")
	case errors.Is(err, webhook.ErrInvalidAgentID):
		writeError(w, http.StatusBadRequest, "agent_id is required")
	case errors.Is(err, webhook.ErrInvalidProvider):
		writeError(w, http.StatusBadRequest, "invalid provider")
	case errors.Is(err, webhook.ErrInvalidTimeout):
		writeError(w, http.StatusBadRequest, "timeout is outside the allowed range")
	case errors.Is(err, webhook.ErrInvalidETag):
		writeError(w, http.StatusBadRequest, "etag is required")
	default:
		s.log.Error("webhook handler error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func webhookView(item webhook.Webhook) apitypes.Webhook {
	id := uuid.MustParse(item.ID)
	userID := uuid.MustParse(item.UserID)
	etag := item.ETag()
	last4 := item.TokenLast4
	created := item.CreatedAt.UTC()
	updated := item.UpdatedAt.UTC()
	return apitypes.Webhook{Id: &id, UserId: &userID, Name: item.Name, AgentId: item.AgentID, Provider: apitypes.WebhookProvider(item.Provider), IsEnabled: item.IsEnabled, WaitTimeoutSeconds: int(item.WaitTimeoutSeconds), MaxRunTimeoutSeconds: int(item.MaxRunTimeoutSeconds), TokenLast4: &last4, Etag: &etag, CreatedAt: &created, UpdatedAt: &updated, RotatedAt: item.RotatedAt}
}

func (s *Server) webhookSecretView(result webhook.IssueResult) apitypes.Webhook {
	view := webhookView(result.Webhook)
	url := strings.TrimRight(s.baseURL, "/") + "/webhooks/" + result.Capability
	view.Url = &url
	return view
}

// decodeWebhookUpdate rejects JSON null instead of letting Go collapse it into
// an omitted pointer. Provider is creation-only, so its presence is invalid.
func decodeWebhookUpdate(r *http.Request) (apitypes.UpdateWebhookRequest, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return apitypes.UpdateWebhookRequest{}, errors.New("invalid JSON")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return apitypes.UpdateWebhookRequest{}, errors.New("invalid JSON")
	}
	for _, field := range []string{"name", "agent_id", "is_enabled", "wait_timeout_seconds", "max_run_timeout_seconds"} {
		if value, ok := raw[field]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return apitypes.UpdateWebhookRequest{}, errors.New(field + " cannot be null")
		}
	}
	if _, ok := raw["provider"]; ok {
		return apitypes.UpdateWebhookRequest{}, errors.New("provider is immutable")
	}
	var req apitypes.UpdateWebhookRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return apitypes.UpdateWebhookRequest{}, errors.New("invalid JSON")
	}
	return req, nil
}

func webhookTimeout(value int, ceiling int32) (int32, error) {
	if value < 1 || int64(value) > int64(ceiling) {
		return 0, webhook.ErrInvalidTimeout
	}
	return int32(value), nil
}

func webhookTimeoutPtr(value *int, ceiling int32) (*int32, error) {
	if value == nil {
		return nil, nil
	}
	out, err := webhookTimeout(*value, ceiling)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
