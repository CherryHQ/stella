// Package modelcatalog provides the embedded and optionally synchronized
// models.dev provider/model directory. The directory is reference data, not
// provider configuration, so callers must tolerate a missing catalog entry.
package modelcatalog

import (
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

//go:embed data/models-dev.json.gz
var embeddedFS embed.FS

const (
	embeddedName         = "data/models-dev.json.gz"
	currentFormatVersion = 2
)

// Catalog is the compact subset of models.dev used by Stella.
type Catalog struct {
	Version int `json:"version"`
	// ModelsByID contains provider-agnostic lab model metadata keyed by
	// <lab>/<model>. ProvidersByID contains API hosts and their serving details.
	ModelsByID    map[string]Model    `json:"models"`
	ProvidersByID map[string]Provider `json:"providers"`
}

type Provider struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	API    string           `json:"api,omitempty"`
	NPM    string           `json:"npm,omitempty"`
	Doc    string           `json:"doc,omitempty"`
	Env    []string         `json:"env,omitempty"`
	Models map[string]Model `json:"models"`
}

type Model struct {
	ID               string     `json:"id"`
	Name             string     `json:"name,omitempty"`
	Description      string     `json:"description,omitempty"`
	Family           string     `json:"family,omitempty"`
	Attachment       bool       `json:"attachment,omitempty"`
	Reasoning        bool       `json:"reasoning,omitempty"`
	ToolCall         bool       `json:"tool_call,omitempty"`
	StructuredOutput bool       `json:"structured_output,omitempty"`
	Modalities       Modalities `json:"modalities,omitempty"`
	Limit            ModelLimit `json:"limit,omitempty"`
	Cost             *ModelCost `json:"cost,omitempty"`
}

type Modalities struct {
	Input  []string `json:"input,omitempty"`
	Output []string `json:"output,omitempty"`
}

type ModelLimit struct {
	Context int `json:"context,omitempty"`
	Output  int `json:"output,omitempty"`
}

// ModelCost keeps presence in the wire representation. A nil field means the
// upstream omitted that rate, while a non-nil pointer to zero means free.
type ModelCost struct {
	Input       *float64        `json:"input,omitempty"`
	Output      *float64        `json:"output,omitempty"`
	CacheRead   *float64        `json:"cache_read,omitempty"`
	CacheWrite  *float64        `json:"cache_write,omitempty"`
	Reasoning   *float64        `json:"reasoning,omitempty"`
	InputAudio  *float64        `json:"input_audio,omitempty"`
	OutputAudio *float64        `json:"output_audio,omitempty"`
	Tiers       []ModelCostTier `json:"tiers,omitempty"`
}

type ModelCostTier struct {
	MinContext  int      `json:"min_context"`
	Input       *float64 `json:"input,omitempty"`
	Output      *float64 `json:"output,omitempty"`
	CacheRead   *float64 `json:"cache_read,omitempty"`
	CacheWrite  *float64 `json:"cache_write,omitempty"`
	Reasoning   *float64 `json:"reasoning,omitempty"`
	InputAudio  *float64 `json:"input_audio,omitempty"`
	OutputAudio *float64 `json:"output_audio,omitempty"`
}

// Embedded returns the built-in directory. It is safe to call repeatedly.
func Embedded() (*Catalog, error) {
	raw, err := embeddedFS.ReadFile(embeddedName)
	if err != nil {
		return nil, fmt.Errorf("read embedded model catalog: %w", err)
	}
	return decode(raw)
}

func decode(raw []byte) (*Catalog, error) {
	payload := raw
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("open model catalog gzip: %w", err)
		}
		var readErr error
		payload, readErr = io.ReadAll(gz)
		closeErr := gz.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read model catalog gzip: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close model catalog gzip: %w", closeErr)
		}
	}
	var catalog Catalog
	if err := json.Unmarshal(payload, &catalog); err != nil {
		return nil, fmt.Errorf("decode model catalog: %w", err)
	}
	if catalog.Version != currentFormatVersion {
		return nil, fmt.Errorf("decode model catalog: unsupported format version %d", catalog.Version)
	}
	if catalog.ModelsByID == nil {
		catalog.ModelsByID = map[string]Model{}
	}
	if catalog.ProvidersByID == nil {
		catalog.ProvidersByID = map[string]Provider{}
	}
	return &catalog, nil
}

// Lookup returns a provider by its stable models.dev id.
func (c *Catalog) Lookup(providerID string) (Provider, bool) {
	if c == nil {
		return Provider{}, false
	}
	p, ok := c.ProvidersByID[providerID]
	return p, ok
}

// HostedModel returns how one API host serves a model, including host pricing.
func (c *Catalog) HostedModel(providerID, modelID string) (Model, bool) {
	p, ok := c.Lookup(providerID)
	if !ok {
		return Model{}, false
	}
	m, ok := p.Models[modelID]
	return m, ok
}

// CanonicalModel returns provider-agnostic metadata for a model created by a lab.
func (c *Catalog) CanonicalModel(modelID string) (Model, bool) {
	if c == nil {
		return Model{}, false
	}
	m, ok := c.ModelsByID[modelID]
	return m, ok
}

// Models returns canonical lab models in stable ID order.
func (c *Catalog) Models() []Model {
	if c == nil {
		return nil
	}
	ids := make([]string, 0, len(c.ModelsByID))
	for id := range c.ModelsByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Model, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.ModelsByID[id])
	}
	return out
}

// Providers returns providers in stable id order, optionally including entries
// Stella cannot serve because no matching adapter is available.
func (c *Catalog) Providers(includeUnsupported bool) []Provider {
	if c == nil {
		return nil
	}
	ids := make([]string, 0, len(c.ProvidersByID))
	for id := range c.ProvidersByID {
		if includeUnsupported || !IsUnsupported(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]Provider, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.ProvidersByID[id])
	}
	return out
}
