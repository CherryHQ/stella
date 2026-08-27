package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/config"
)

func (s *Server) ListProviders(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	pList, err := access.ListProviders(r.Context())
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"providers": pList})
}

func (s *Server) CreateProvider(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var p config.Provider
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if p.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if p.Type == "" {
		p.Type = p.ID
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	if err := access.CreateProvider(r.Context(), p); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusCreated, p)
}

func (s *Server) GetProvider(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	p, err := access.GetProvider(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, p)
}

// GetProviderEvidence deliberately exports the smallest identity needed by the
// trusted Harbor driver. In particular it must never reuse GetProvider's
// response because that response contains the provider API key.
func (s *Server) GetProviderEvidence(w http.ResponseWriter, r *http.Request, id string, params apiserver.GetProviderEvidenceParams) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	if params.ModelId == "" {
		writeError(w, http.StatusBadRequest, "model_id is required")
		return
	}
	provider, err := access.GetProvider(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	model, ok := provider.Models[params.ModelId]
	if !ok {
		writeError(w, http.StatusNotFound, "provider model not found")
		return
	}
	endpoint, err := normalizedProviderEndpoint(provider.BaseURL)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	cost, err := json.Marshal(model.Cost)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	digest := sha256.Sum256(cost)
	providerType := provider.Type
	if providerType == "" {
		providerType = provider.ID
	}
	writeData(w, http.StatusOK, apitypes.ProviderEvidence{
		ProviderId: id, ModelId: params.ModelId, GatewayEndpoint: endpoint,
		ProviderType: providerType, ModelPriceDigest: hex.EncodeToString(digest[:]),
	})
}

// normalizedProviderEndpoint removes credentials and rejects query fragments.
// The evidence DTO is an identity, never an operational URL to replay.
func normalizedProviderEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("configured provider has an invalid base URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("configured provider has an invalid base URL scheme")
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" {
		host += ":" + port
	}
	basePath := path.Clean(parsed.EscapedPath())
	if basePath == "." {
		basePath = "/"
	}
	return scheme + "://" + host + basePath, nil
}

func (s *Server) UpdateProvider(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var p config.Provider
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	saved, err := access.UpdateProvider(r.Context(), id, p)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, saved)
}

func (s *Server) DeleteProvider(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	if err := access.DeleteProvider(r.Context(), id); err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeNoContent(w)
}

func (s *Server) ListProviderModels(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	models, err := access.ListProviderModels(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) FetchProviderModels(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var body struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	models, err := access.FetchProviderModels(r.Context(), id, body.APIKey, body.BaseURL)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) ListProviderTypes(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	types, err := access.ListProviderTypes()
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}

	type providerType struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		DefaultURL string `json:"default_url"`
	}
	out := make([]providerType, 0, len(types))
	for _, pt := range types {
		out = append(out, providerType{ID: pt.ID, Name: pt.Name, DefaultURL: pt.DefaultURL})
	}
	writeData(w, http.StatusOK, map[string]any{"provider_types": out})
}
