package catalog

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

const DefaultURL = "https://models.dev/catalog.json"

type SyncStore interface {
	SnapshotStore
	UpsertModelCatalog(context.Context, SnapshotRecord) error
}

type SyncResult struct {
	NotModified bool
	Record      SnapshotRecord
	Catalog     *Catalog
}

// Sync conditionally refreshes the upstream directory. The stored payload is
// compacted before persistence so the embedded and database formats are equal.
func Sync(ctx context.Context, store SyncStore, client *http.Client, url string, now func() time.Time) (SyncResult, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if url == "" {
		url = DefaultURL
	}
	if now == nil {
		now = time.Now
	}
	previous, err := store.GetModelCatalog(ctx)
	if err != nil {
		previous = SnapshotRecord{}
	} else if len(previous.Payload) > 0 {
		if _, decodeErr := decode(previous.Payload); decodeErr != nil {
			// A provider-only v1 snapshot has an ETag for api.json, not the
			// canonical catalog.json resource. Force one unconditional refresh.
			previous = SnapshotRecord{}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return SyncResult{}, fmt.Errorf("build model catalog request: %w", err)
	}
	if previous.ETag != "" {
		req.Header.Set("If-None-Match", previous.ETag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return SyncResult{}, fmt.Errorf("fetch model catalog: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		previous.SyncedAt = now().UTC()
		if err := store.UpsertModelCatalog(ctx, previous); err != nil {
			return SyncResult{}, fmt.Errorf("touch model catalog: %w", err)
		}
		catalog, err := decode(previous.Payload)
		if err != nil {
			return SyncResult{}, fmt.Errorf("decode unchanged model catalog: %w", err)
		}
		return SyncResult{NotModified: true, Record: previous, Catalog: catalog}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return SyncResult{}, fmt.Errorf("fetch model catalog: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return SyncResult{}, fmt.Errorf("read model catalog: %w", err)
	}
	catalog, err := compact(raw)
	if err != nil {
		return SyncResult{}, err
	}
	payload, err := json.Marshal(catalog)
	if err != nil {
		return SyncResult{}, fmt.Errorf("encode compact model catalog: %w", err)
	}
	record := SnapshotRecord{Payload: payload, ETag: resp.Header.Get("ETag"), SyncedAt: now().UTC()}
	if err := store.UpsertModelCatalog(ctx, record); err != nil {
		return SyncResult{}, fmt.Errorf("store model catalog: %w", err)
	}
	return SyncResult{Record: record, Catalog: catalog}, nil
}

// CompactGzip decodes a full models.dev response and returns the compact gzip
// representation used by go:embed.
func CompactGzip(raw []byte) ([]byte, error) {
	catalog, err := compact(raw)
	if err != nil {
		return nil, err
	}
	return gzipBytes(catalog)
}

// WriteGzip writes the compact catalog in the format used by go:embed.
func WriteGzip(catalog *Catalog, w io.Writer) error {
	payload, err := json.Marshal(catalog)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(w)
	if _, err := gz.Write(payload); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

func compact(raw []byte) (*Catalog, error) {
	var upstream struct {
		Models    map[string]json.RawMessage `json:"models"`
		Providers map[string]struct {
			ID     string                     `json:"id"`
			Name   string                     `json:"name"`
			API    string                     `json:"api"`
			NPM    string                     `json:"npm"`
			Doc    string                     `json:"doc"`
			Env    []string                   `json:"env"`
			Models map[string]json.RawMessage `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &upstream); err != nil {
		return nil, fmt.Errorf("decode upstream model catalog: %w", err)
	}
	if upstream.Models == nil || upstream.Providers == nil {
		return nil, fmt.Errorf("decode upstream model catalog: expected models and providers")
	}
	out := &Catalog{
		Version:       currentFormatVersion,
		ModelsByID:    make(map[string]Model, len(upstream.Models)),
		ProvidersByID: make(map[string]Provider, len(upstream.Providers)),
	}
	for modelID, data := range upstream.Models {
		model, err := compactModel(modelID, data)
		if err != nil {
			return nil, fmt.Errorf("decode canonical model %s: %w", modelID, err)
		}
		out.ModelsByID[modelID] = model
	}
	for id, src := range upstream.Providers {
		p := Provider{ID: id, Name: src.Name, API: src.API, NPM: src.NPM, Doc: src.Doc, Env: src.Env, Models: map[string]Model{}}
		for modelID, data := range src.Models {
			model, err := compactModel(modelID, data)
			if err != nil {
				return nil, fmt.Errorf("decode hosted model %s/%s: %w", id, modelID, err)
			}
			p.Models[modelID] = model
		}
		out.ProvidersByID[id] = p
	}
	return out, nil
}

func compactModel(modelID string, data json.RawMessage) (Model, error) {
	var src struct {
		ID               string        `json:"id"`
		Name             string        `json:"name"`
		Description      string        `json:"description"`
		Family           string        `json:"family"`
		Attachment       bool          `json:"attachment"`
		Reasoning        bool          `json:"reasoning"`
		ToolCall         bool          `json:"tool_call"`
		StructuredOutput bool          `json:"structured_output"`
		Modalities       Modalities    `json:"modalities"`
		Limit            ModelLimit    `json:"limit"`
		Cost             *upstreamCost `json:"cost"`
	}
	if err := json.Unmarshal(data, &src); err != nil {
		return Model{}, err
	}
	model := Model{ID: modelID, Name: src.Name, Description: src.Description, Family: src.Family, Attachment: src.Attachment, Reasoning: src.Reasoning, ToolCall: src.ToolCall, StructuredOutput: src.StructuredOutput, Modalities: src.Modalities, Limit: src.Limit}
	if src.ID != "" {
		model.ID = src.ID
	}
	if src.Cost != nil {
		model.Cost = src.Cost.compact()
	}
	return model, nil
}

type upstreamCost struct {
	Input       *float64 `json:"input"`
	Output      *float64 `json:"output"`
	CacheRead   *float64 `json:"cache_read"`
	CacheWrite  *float64 `json:"cache_write"`
	Reasoning   *float64 `json:"reasoning"`
	InputAudio  *float64 `json:"input_audio"`
	OutputAudio *float64 `json:"output_audio"`
	Tiers       []struct {
		Tier struct {
			Type string `json:"type"`
			Size int    `json:"size"`
		} `json:"tier"`
		Input       *float64 `json:"input"`
		Output      *float64 `json:"output"`
		CacheRead   *float64 `json:"cache_read"`
		CacheWrite  *float64 `json:"cache_write"`
		Reasoning   *float64 `json:"reasoning"`
		InputAudio  *float64 `json:"input_audio"`
		OutputAudio *float64 `json:"output_audio"`
	} `json:"tiers"`
}

func (c *upstreamCost) compact() *ModelCost {
	out := &ModelCost{Input: c.Input, Output: c.Output, CacheRead: c.CacheRead, CacheWrite: c.CacheWrite, Reasoning: c.Reasoning, InputAudio: c.InputAudio, OutputAudio: c.OutputAudio}
	for _, tier := range c.Tiers {
		if tier.Tier.Type != "context" || tier.Tier.Size <= 0 {
			continue
		}
		out.Tiers = append(out.Tiers, ModelCostTier{MinContext: tier.Tier.Size, Input: tier.Input, Output: tier.Output, CacheRead: tier.CacheRead, CacheWrite: tier.CacheWrite, Reasoning: tier.Reasoning, InputAudio: tier.InputAudio, OutputAudio: tier.OutputAudio})
	}
	sort.Slice(out.Tiers, func(i, j int) bool { return out.Tiers[i].MinContext < out.Tiers[j].MinContext })
	return out
}

func gzipBytes(catalog *Catalog) ([]byte, error) {
	var b bytes.Buffer
	if err := WriteGzip(catalog, &b); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
