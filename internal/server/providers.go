package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/controlplane"
	"github.com/CherryHQ/stella/internal/modelcatalog"
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
	if _, parseErr := time.Parse(time.RFC3339Nano, patch.ExpectedVersion); parseErr != nil {
		writeError(w, http.StatusBadRequest, "expected_version must be RFC3339")
		return
	}
	applyProviderPatch(&current, patch)
	current, err = access.NormalizeProvider(r.Context(), current)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	_, err = access.UpdateProviderIfVersion(r.Context(), current, patch.ExpectedVersion)
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

func (s *Server) DeleteProvider(w http.ResponseWriter, r *http.Request, id string, params apiserver.DeleteProviderParams) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	if _, err := time.Parse(time.RFC3339Nano, params.ExpectedVersion); err != nil {
		writeError(w, http.StatusBadRequest, "expected_version must be RFC3339")
		return
	}
	if err := access.DeleteProviderIfVersion(r.Context(), id, params.ExpectedVersion); err != nil {
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
	writeData(w, http.StatusOK, apitypes.ProviderModelList{Models: providerModelItems(models)})
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
	if err := decodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
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
	writeData(w, http.StatusOK, apitypes.ProviderModelList{Models: providerModelItems(models), SyncedAt: syncedAt})
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
		apiType := modelcatalog.APIType(p.ID, p)
		out = append(out, apitypes.CatalogProvider{
			Id:         p.ID,
			Name:       name,
			ApiType:    apiType,
			BaseUrl:    modelcatalog.BaseURL(p.ID, p),
			Doc:        &doc,
			Supported:  !modelcatalog.IsUnsupported(p.ID) && apiType != "",
			ModelCount: &count,
		})
	}
	writeData(w, http.StatusOK, apitypes.CatalogProviderList{Providers: out})
}

func (s *Server) ListModelCatalogModels(w http.ResponseWriter, r *http.Request) {
	access, ok := s.beginControlPlane(w, r)
	if !ok {
		return
	}
	models, err := access.ListModelCatalogModels(r.Context())
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	out := make([]apitypes.CatalogModel, 0, len(models))
	for _, model := range models {
		out = append(out, *catalogModelResponse(model))
	}
	writeData(w, http.StatusOK, apitypes.CatalogModelList{Models: out})
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
	status := apitypes.ModelCatalogStatus{Source: apitypes.ModelCatalogStatusSource(source), ProviderCount: len(catalog.ProvidersByID), ModelCount: len(catalog.ModelsByID)}
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
	status := apitypes.ModelCatalogStatus{Source: "database", ProviderCount: len(catalog.ProvidersByID), ModelCount: len(catalog.ModelsByID)}
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
	writeData(w, http.StatusOK, apitypes.ProviderModelList{Models: providerModelItems(models)})
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

func providerModelItems(models []controlplane.ProviderModelItem) []apitypes.ProviderModelItem {
	out := make([]apitypes.ProviderModelItem, 0, len(models))
	for _, model := range models {
		item := apitypes.ProviderModelItem{
			Id:      model.ID,
			Source:  apitypes.ProviderModelItemSource(model.Source),
			Enabled: model.Enabled,
			Config:  providerModelResponse(model.Config),
		}
		if model.Name != "" {
			item.Name = &model.Name
		}
		if model.Override != nil {
			item.Override = providerModelOverrideResponse(*model.Override)
		}
		if model.Catalog != nil {
			item.Catalog = catalogModelResponse(*model.Catalog)
		}
		out = append(out, item)
	}
	return out
}

func providerModelResponse(model config.ProviderModel) *apitypes.ProviderModel {
	out := &apitypes.ProviderModel{Enabled: model.Enabled}
	if model.ID != "" {
		out.Id = &model.ID
	}
	if model.Name != "" {
		out.Name = &model.Name
	}
	if model.Reasoning {
		out.Reasoning = &model.Reasoning
	}
	if model.Input != nil {
		out.Input = &model.Input
	}
	if model.Output != nil {
		out.Output = &model.Output
	}
	if model.ContextWindow > 0 {
		out.ContextWindow = &model.ContextWindow
	}
	if model.MaxTokens > 0 {
		out.MaxTokens = &model.MaxTokens
	}
	if hasProviderModelCost(model.Cost) {
		out.Cost = providerModelCostResponse(model.Cost)
	}
	return out
}

func providerModelOverrideResponse(model config.ProviderModelOverride) *apitypes.ProviderModelOverride {
	out := &apitypes.ProviderModelOverride{CatalogModel: model.CatalogModel, Enabled: model.Enabled, Name: model.Name, Reasoning: model.Reasoning, ContextWindow: model.ContextWindow, MaxTokens: model.MaxTokens, Input: model.Input, Output: model.Output}
	if model.Cost != nil {
		out.Cost = providerModelCostResponse(*model.Cost)
	}
	return out
}

func catalogModelResponse(model modelcatalog.Model) *apitypes.CatalogModel {
	out := &apitypes.CatalogModel{
		Id:               model.ID,
		Attachment:       &model.Attachment,
		Reasoning:        &model.Reasoning,
		ToolCall:         &model.ToolCall,
		StructuredOutput: &model.StructuredOutput,
		ContextWindow:    &model.Limit.Context,
		MaxTokens:        &model.Limit.Output,
	}
	if model.Name != "" {
		out.Name = &model.Name
	}
	if model.Description != "" {
		out.Description = &model.Description
	}
	if model.Family != "" {
		out.Family = &model.Family
	}
	if model.Modalities.Input != nil {
		out.Input = &model.Modalities.Input
	}
	if model.Modalities.Output != nil {
		out.Output = &model.Modalities.Output
	}
	if model.Cost != nil {
		cost := config.ProviderModelCost{Input: model.Cost.Input, Output: model.Cost.Output, CacheRead: model.Cost.CacheRead, CacheWrite: model.Cost.CacheWrite, Reasoning: model.Cost.Reasoning, InputAudio: model.Cost.InputAudio, OutputAudio: model.Cost.OutputAudio}
		for _, tier := range model.Cost.Tiers {
			cost.Tiers = append(cost.Tiers, config.ProviderModelCostTier{MinContext: tier.MinContext, Input: tier.Input, Output: tier.Output, CacheRead: tier.CacheRead, CacheWrite: tier.CacheWrite, Reasoning: tier.Reasoning, InputAudio: tier.InputAudio, OutputAudio: tier.OutputAudio})
		}
		out.Cost = providerModelCostResponse(cost)
	}
	return out
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
			override := config.ProviderModelOverride{CatalogModel: model.CatalogModel, Enabled: model.Enabled, Name: model.Name, Reasoning: model.Reasoning, Input: model.Input, Output: model.Output, ContextWindow: model.ContextWindow, MaxTokens: model.MaxTokens}
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
		out[i] = config.ProviderModelCostTier{MinContext: tier.MinContext, Input: tier.Input, Output: tier.Output, CacheRead: tier.CacheRead, CacheWrite: tier.CacheWrite, Reasoning: tier.Reasoning, InputAudio: tier.InputAudio, OutputAudio: tier.OutputAudio}
	}
	return out
}

func hasProviderModelCost(cost config.ProviderModelCost) bool {
	return cost.Input != nil || cost.Output != nil || cost.CacheRead != nil || cost.CacheWrite != nil || cost.Reasoning != nil || cost.InputAudio != nil || cost.OutputAudio != nil || cost.Tiers != nil
}

func providerModelCostResponse(cost config.ProviderModelCost) *apitypes.ProviderModelCost {
	out := &apitypes.ProviderModelCost{Input: cost.Input, Output: cost.Output, CacheRead: cost.CacheRead, CacheWrite: cost.CacheWrite, Reasoning: cost.Reasoning, InputAudio: cost.InputAudio, OutputAudio: cost.OutputAudio}
	if cost.Tiers != nil {
		tiers := make([]apitypes.ProviderModelCostTier, len(cost.Tiers))
		for i, tier := range cost.Tiers {
			tiers[i] = apitypes.ProviderModelCostTier{MinContext: tier.MinContext, Input: tier.Input, Output: tier.Output, CacheRead: tier.CacheRead, CacheWrite: tier.CacheWrite, Reasoning: tier.Reasoning, InputAudio: tier.InputAudio, OutputAudio: tier.OutputAudio}
		}
		out.Tiers = &tiers
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
	models := make(map[string]apitypes.ProviderModelOverride, len(p.Models))
	for id, model := range p.Models {
		m := apitypes.ProviderModelOverride{CatalogModel: model.CatalogModel, Enabled: model.Enabled, Name: model.Name, Reasoning: model.Reasoning, ContextWindow: model.ContextWindow, MaxTokens: model.MaxTokens, Input: model.Input, Output: model.Output}
		if model.Cost != nil {
			m.Cost = providerModelCostResponse(*model.Cost)
		}
		models[id] = m
	}
	if len(models) > 0 {
		out.Models = &models
	}
	return out
}
