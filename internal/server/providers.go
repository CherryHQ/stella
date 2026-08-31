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
	"github.com/CherryHQ/stella/internal/controlplane"
	"github.com/CherryHQ/stella/internal/modelresolve"
)

func (s *Server) ListProviders(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	snapshots, err := access.ListProviderSnapshots(r.Context())
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	providers := make([]config.Provider, 0, len(snapshots))
	for _, snapshot := range snapshots {
		providers = append(providers, snapshot.Provider)
	}
	counts, err := access.ListProviderModelCounts(r.Context(), providers)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	out := make([]apitypes.Provider, 0, len(snapshots))
	for _, snapshot := range snapshots {
		provider := providerResponse(snapshot.Provider, snapshot.Version)
		count := counts[snapshot.Provider.ID]
		provider.TotalModelCount, provider.EnabledModelCount = intPtr(count[0]), intPtr(count[1])
		out = append(out, provider)
	}
	writeData(w, http.StatusOK, apitypes.ProviderList{Providers: out})
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
	snapshot, err := access.GetProviderSnapshot(r.Context(), p.ID)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusCreated, providerResponse(snapshot.Provider, snapshot.Version))
}

func (s *Server) GetProvider(w http.ResponseWriter, r *http.Request, id string) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	snapshot, err := access.GetProviderSnapshot(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, providerResponse(snapshot.Provider, snapshot.Version))
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
	resolved, err := access.ResolveProviderModel(r.Context(), id, params.ModelId)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	if !resolved.Found {
		writeError(w, http.StatusNotFound, "provider model not found")
		return
	}
	endpoint, err := normalizedProviderEndpoint(provider.BaseURL)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	cost, err := json.Marshal(modelresolve.RuntimeCost(resolved.Model.Cost))
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
	var patch apitypes.ProviderPatch
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	current, err := access.GetProvider(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	applyProviderPatch(&current, patch)
	if patch.ExpectedVersion != nil && *patch.ExpectedVersion != "" {
		_, err = access.UpdateProviderIfVersion(r.Context(), current, *patch.ExpectedVersion)
	} else {
		_, err = access.UpdateProvider(r.Context(), id, current)
	}
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	snapshot, err := access.GetProviderSnapshot(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, providerResponse(snapshot.Provider, snapshot.Version))
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
	syncedAt, err := access.ProviderModelsSyncedAt(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"models": models, "synced_at": syncedAt})
}

func (s *Server) ListModelCatalogProviders(w http.ResponseWriter, r *http.Request, params apiserver.ListModelCatalogProvidersParams) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	include := params.IncludeUnsupported != nil && *params.IncludeUnsupported
	providers, err := access.ListModelCatalogProviders(r.Context(), include)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	out := make([]apitypes.CatalogProvider, 0, len(providers))
	for _, p := range providers {
		name, doc := p.Name, p.Doc
		count := len(p.Models)
		out = append(out, apitypes.CatalogProvider{Id: p.ID, Name: name, ApiType: p.API, BaseUrl: p.API, Doc: &doc, Supported: p.API != "", ModelCount: &count})
	}
	writeData(w, http.StatusOK, apitypes.CatalogProviderList{Providers: out})
}

func (s *Server) GetModelCatalogStatus(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	catalog, record, source, err := access.ModelCatalogStatus(r.Context())
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	modelCount := 0
	for _, p := range catalog.ProvidersByID {
		modelCount += len(p.Models)
	}
	status := apitypes.ModelCatalogStatus{Source: apitypes.ModelCatalogStatusSource(source), ProviderCount: len(catalog.ProvidersByID), ModelCount: modelCount}
	if record.ETag != "" {
		status.Etag = &record.ETag
	}
	if !record.SyncedAt.IsZero() {
		synced := record.SyncedAt.UTC()
		status.SyncedAt = &synced
	}
	writeData(w, http.StatusOK, status)
}

func (s *Server) SyncModelCatalog(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	result, err := access.SyncModelCatalog(r.Context())
	if err != nil {
		s.writeControlPlaneError(w, &controlplane.UpstreamError{Err: err})
		return
	}
	catalog := result.Catalog
	count := 0
	for _, p := range catalog.ProvidersByID {
		count += len(p.Models)
	}
	status := apitypes.ModelCatalogStatus{Source: "database", ProviderCount: len(catalog.ProvidersByID), ModelCount: count}
	if result.Record.ETag != "" {
		status.Etag = &result.Record.ETag
	}
	if !result.Record.SyncedAt.IsZero() {
		synced := result.Record.SyncedAt.UTC()
		status.SyncedAt = &synced
	}
	writeData(w, http.StatusOK, apitypes.ModelCatalogSyncResult{NotModified: result.NotModified, Status: status})
}

func (s *Server) ProbeProvider(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	var req apitypes.ProviderProbeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	models, err := access.ProbeProvider(r.Context(), req.ApiType, req.ApiKey, req.BaseUrl)
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

func applyProviderPatch(p *config.Provider, patch apitypes.ProviderPatch) {
	if patch.Type != nil {
		p.Type = *patch.Type
	}
	if patch.Name != nil {
		p.Name = *patch.Name
	}
	if patch.Enabled != nil {
		p.Enabled = *patch.Enabled
	}
	if patch.ApiKey != nil {
		p.APIKey = *patch.ApiKey
	}
	if patch.BaseUrl != nil {
		p.BaseURL = *patch.BaseUrl
	}
	if patch.CatalogId != nil {
		p.CatalogID = *patch.CatalogId
	}
	if patch.ModelPolicy != nil {
		p.ModelPolicy = string(*patch.ModelPolicy)
	}
	if patch.Models != nil {
		models := make(map[string]config.ProviderModelOverride, len(*patch.Models))
		for id, model := range *patch.Models {
			override := config.ProviderModelOverride{Enabled: &model.Enabled, Name: model.Name, Reasoning: model.Reasoning, Input: model.Input, Output: model.Output, ContextWindow: model.ContextWindow, MaxTokens: model.MaxTokens}
			if model.Cost != nil {
				override.Cost = providerModelCostOverride(*model.Cost)
			}
			models[id] = override
		}
		p.Models = models
	}
}

func providerModelCostOverride(cost apitypes.ProviderModelCost) *config.ProviderModelCost {
	return &config.ProviderModelCost{Input: cost.Input, Output: cost.Output, CacheRead: cost.CacheRead, CacheWrite: cost.CacheWrite, Reasoning: cost.Reasoning, InputAudio: cost.InputAudio, OutputAudio: cost.OutputAudio, Tiers: providerModelCostTiers(cost.Tiers)}
}

func providerModelCostTiers(in *[]apitypes.ProviderModelCostTier) []config.ProviderModelCostTier {
	if in == nil {
		return nil
	}
	out := make([]config.ProviderModelCostTier, len(*in))
	for i, tier := range *in {
		out[i] = config.ProviderModelCostTier{MinContext: tier.MinContext, Input: tier.Input, Output: tier.Output, CacheRead: tier.CacheRead, CacheWrite: tier.CacheWrite, Reasoning: tier.Reasoning, InputAudio: tier.InputAudio}
	}
	return out
}

func intPtr(value int) *int { return &value }

func providerResponse(p config.Provider, version string) apitypes.Provider {
	out := apitypes.Provider{ApiKey: p.APIKey, BaseUrl: p.BaseURL, Enabled: p.Enabled, Id: p.ID, Name: p.Name, Type: p.Type}
	if p.CatalogID != "" {
		out.CatalogId = &p.CatalogID
	}
	if p.ModelPolicy != "" {
		policy := apitypes.ProviderModelPolicy(p.ModelPolicy)
		out.ModelPolicy = &policy
	}
	if version != "" {
		out.Version = &version
	}
	hasKey := p.APIKey != ""
	out.HasApiKey = &hasKey
	models := make(map[string]apitypes.ProviderModel, len(p.Models))
	for id, model := range p.Models {
		enabled := model.Enabled != nil && *model.Enabled
		m := apitypes.ProviderModel{Enabled: enabled, Name: model.Name, Reasoning: model.Reasoning, ContextWindow: model.ContextWindow, MaxTokens: model.MaxTokens}
		if model.Input != nil {
			m.Input = model.Input
		}
		if model.Output != nil {
			m.Output = model.Output
		}
		models[id] = m
	}
	if len(models) > 0 {
		out.Models = &models
	}
	return out
}
