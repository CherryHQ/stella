package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/embedding"
	releasecontract "github.com/CherryHQ/stella/test/release"
)

const (
	// EnvCherryINAPIKey is shared by the Provider and Embedding live targets.
	// The non-secret JSON variables deliberately cannot carry credentials.
	EnvCherryINAPIKey = "STELLA_LIVE_CHERRYIN_API_KEY"
	// EnvProviderTargets configures the CherryIN protocols and models used by X12.
	EnvProviderTargets = "STELLA_LIVE_PROVIDER_TARGETS_JSON"
	// EnvEmbeddingTarget configures the CherryIN embedding model used by X14.
	EnvEmbeddingTarget = "STELLA_LIVE_EMBEDDING_TARGET_JSON"
)

const (
	defaultLiveTimeout = 4 * time.Minute
	minSimilarityLead  = 0.01
)

var advertisedProviderTypes = []string{"anthropic", "openai", "openai-response"}

// providerTargetConfig selects one Stella Provider implementation and the real
// CherryIN model exercised through it.
type providerTargetConfig struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Model   string `json:"model"`
	BaseURL string `json:"base_url"`
}

// providerLiveConfig contains no credential and is safe to store as a GitHub
// Environment variable. The API key comes only from EnvCherryINAPIKey.
type providerLiveConfig struct {
	TimeoutSecond int                    `json:"timeout_seconds,omitempty"`
	Targets       []providerTargetConfig `json:"targets"`
}

// embeddingLiveConfig contains only the non-secret endpoint/model selection.
type embeddingLiveConfig struct {
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	Dimensions    int    `json:"dimensions,omitempty"`
	TimeoutSecond int    `json:"timeout_seconds,omitempty"`
}

type providerJourneyResult struct {
	TargetID        string
	StreamedText    bool
	CalledBash      bool
	ObservedOutput  bool
	ObservedMarker  bool
	CompletedStream bool
}

type providerCandidate interface {
	RunProviderJourney(
		context.Context,
		providerTargetConfig,
		string,
		string,
		string,
	) (providerJourneyResult, error)
	Close(context.Context) error
}

type providerCandidateFactory func(
	context.Context,
	string,
	releasecontract.Run,
) (providerCandidate, error)

// cherryINProviderAdapter drives the exact candidate through all three
// advertised Provider protocols and a controlled bash tool call.
type cherryINProviderAdapter struct {
	factory   providerCandidateFactory
	config    providerLiveConfig
	candidate providerCandidate
	results   []providerJourneyResult
}

// NewCherryINProviderAdapter returns the X12 real-provider adapter.
func NewCherryINProviderAdapter() Adapter {
	return &cherryINProviderAdapter{factory: startStellaCandidate}
}

func (a *cherryINProviderAdapter) Preflight(_ context.Context, _ Target, inputs Inputs) error {
	raw, _ := inputs.Value(EnvProviderTargets)
	config, err := parseProviderLiveConfig(raw)
	if err != nil {
		return ProductFailure("invalid CherryIN Provider target configuration: " + err.Error())
	}
	if _, ok := inputs.Value(EnvCherryINAPIKey); !ok {
		return ProductFailure("CherryIN API key was not exposed to the Provider adapter")
	}
	if err := validateCandidateBinary(inputs.CandidateBinary()); err != nil {
		return ProductFailure(err.Error())
	}
	a.config = config
	a.results = nil
	return nil
}

func (a *cherryINProviderAdapter) Run(ctx context.Context, _ Target, inputs Inputs) error {
	if a.factory == nil {
		return ProductFailure("CherryIN Provider adapter has no candidate factory")
	}
	apiKey, _ := inputs.Value(EnvCherryINAPIKey)
	candidate, err := a.factory(ctx, inputs.CandidateBinary(), inputs.Run())
	if err != nil {
		return ProductFailure("start exact Stella candidate for CherryIN Provider checks")
	}
	a.candidate = candidate

	timeout := configTimeout(a.config.TimeoutSecond)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, providerTarget := range a.config.Targets {
		marker := liveMarker(inputs.Run().ID, providerTarget.ID)
		result, err := candidate.RunProviderJourney(
			runCtx,
			providerTarget,
			providerTarget.BaseURL,
			apiKey,
			marker,
		)
		if err != nil {
			var external *providerExternalError
			if errors.As(err, &external) {
				return ExternalBlocked(
					fmt.Sprintf("CherryIN %s target could not complete: %s", providerTarget.ID, external.reason),
					external.retryable,
				)
			}
			return ProductFailure(fmt.Sprintf(
				"Stella Session journey failed for CherryIN target %s",
				providerTarget.ID,
			))
		}
		a.results = append(a.results, result)
	}
	return nil
}

func (a *cherryINProviderAdapter) Assert(_ context.Context, _ Target, _ Inputs) error {
	if len(a.results) != len(a.config.Targets) {
		return ProductFailure("CherryIN Provider journey did not produce one result per advertised Provider")
	}
	for _, result := range a.results {
		switch {
		case !result.CompletedStream:
			return ProductFailure("CherryIN target " + result.TargetID + " did not complete its Stella SSE stream")
		case !result.StreamedText:
			return ProductFailure("CherryIN target " + result.TargetID + " emitted no streamed text")
		case !result.CalledBash:
			return ProductFailure("CherryIN target " + result.TargetID + " did not request the controlled bash tool")
		case !result.ObservedOutput:
			return ProductFailure("CherryIN target " + result.TargetID + " did not receive the controlled tool output")
		case !result.ObservedMarker:
			return ProductFailure("CherryIN target " + result.TargetID + " did not return the Run-ID marker")
		}
	}
	return nil
}

func (a *cherryINProviderAdapter) Cleanup(ctx context.Context, _ Target, _ Inputs) error {
	candidate := a.candidate
	a.candidate = nil
	a.results = nil
	a.config = providerLiveConfig{}
	if candidate == nil {
		return nil
	}
	if err := candidate.Close(ctx); err != nil {
		return ProductFailure("exact Stella candidate did not clean up after CherryIN Provider checks")
	}
	return nil
}

// cherryINEmbeddingAdapter uses Stella's production embedding provider against
// CherryIN, then builds a tiny in-memory index and checks semantic ranking.
type cherryINEmbeddingAdapter struct {
	config  embeddingLiveConfig
	vectors [][]float32
}

// NewCherryINEmbeddingAdapter returns the X14 real-embedding adapter.
func NewCherryINEmbeddingAdapter() Adapter {
	return &cherryINEmbeddingAdapter{}
}

func (a *cherryINEmbeddingAdapter) Preflight(_ context.Context, _ Target, inputs Inputs) error {
	raw, _ := inputs.Value(EnvEmbeddingTarget)
	config, err := parseEmbeddingLiveConfig(raw)
	if err != nil {
		return ProductFailure("invalid CherryIN Embedding target configuration: " + err.Error())
	}
	if _, ok := inputs.Value(EnvCherryINAPIKey); !ok {
		return ProductFailure("CherryIN API key was not exposed to the Embedding adapter")
	}
	a.config = config
	a.vectors = nil
	return nil
}

func (a *cherryINEmbeddingAdapter) Run(ctx context.Context, _ Target, inputs Inputs) error {
	apiKey, _ := inputs.Value(EnvCherryINAPIKey)
	provider := embedding.NewAPIProvider(embedding.APIConfig{
		Name:    "cherryin-release-live",
		Model:   a.config.Model,
		Dim:     a.config.Dimensions,
		APIKey:  apiKey,
		BaseURL: a.config.BaseURL,
	})
	runCtx, cancel := context.WithTimeout(ctx, configTimeout(a.config.TimeoutSecond))
	defer cancel()
	result, err := provider.Embed(runCtx, embedding.Request{
		Texts:   embeddingFixtureTexts(),
		Mode:    embedding.ModeDocument,
		Privacy: embedding.PrivacyNormal,
	})
	if err != nil {
		return ExternalBlocked("CherryIN embedding request could not complete", retryableProviderError(err))
	}
	a.vectors = result.Vectors
	return nil
}

func (a *cherryINEmbeddingAdapter) Assert(_ context.Context, _ Target, _ Inputs) error {
	const (
		expectedCorpusIndex = 1
		queryIndex          = 3
	)
	if len(a.vectors) != len(embeddingFixtureTexts()) {
		return ProductFailure("CherryIN Embedding returned an unexpected vector count")
	}
	dim := len(a.vectors[0])
	if dim == 0 {
		return ProductFailure("CherryIN Embedding returned an empty vector")
	}
	for _, vector := range a.vectors {
		if len(vector) != dim || !finiteVector(vector) {
			return ProductFailure("CherryIN Embedding returned inconsistent or non-finite vectors")
		}
	}

	query := a.vectors[queryIndex]
	expectedScore := cosineSimilarity(query, a.vectors[expectedCorpusIndex])
	bestOther := -2.0
	for i := range queryIndex {
		if i == expectedCorpusIndex {
			continue
		}
		bestOther = max(bestOther, cosineSimilarity(query, a.vectors[i]))
	}
	if expectedScore < bestOther+minSimilarityLead {
		return ProductFailure("CherryIN Embedding did not rank the expected semantic fixture first")
	}
	return nil
}

func (a *cherryINEmbeddingAdapter) Cleanup(_ context.Context, _ Target, _ Inputs) error {
	a.config = embeddingLiveConfig{}
	a.vectors = nil
	return nil
}

func parseProviderLiveConfig(raw string) (providerLiveConfig, error) {
	var config providerLiveConfig
	if err := decodeStrictJSON(raw, &config); err != nil {
		return providerLiveConfig{}, err
	}
	if config.TimeoutSecond < 0 {
		return providerLiveConfig{}, fmt.Errorf("timeout_seconds cannot be negative")
	}
	seen := map[string]struct{}{}
	seenIDs := map[string]struct{}{}
	for i, target := range config.Targets {
		if !targetIDPattern.MatchString(target.ID) {
			return providerLiveConfig{}, fmt.Errorf("targets[%d].id must be kebab-case", i)
		}
		if _, exists := seenIDs[target.ID]; exists {
			return providerLiveConfig{}, fmt.Errorf("provider target id %s is repeated", target.ID)
		}
		seenIDs[target.ID] = struct{}{}
		if _, exists := seen[target.Type]; exists {
			return providerLiveConfig{}, fmt.Errorf("provider type %s is repeated", target.Type)
		}
		seen[target.Type] = struct{}{}
		if strings.TrimSpace(target.Model) == "" || containsControl(target.Model) {
			return providerLiveConfig{}, fmt.Errorf("targets[%d].model is required", i)
		}
		if err := validateHTTPSBaseURL(target.BaseURL); err != nil {
			return providerLiveConfig{}, fmt.Errorf("targets[%d].base_url: %w", i, err)
		}
	}
	expected := append([]string(nil), advertisedProviderTypes...)
	actual := make([]string, 0, len(seen))
	for providerType := range seen {
		actual = append(actual, providerType)
	}
	sort.Strings(expected)
	sort.Strings(actual)
	if strings.Join(actual, ",") != strings.Join(expected, ",") {
		return providerLiveConfig{}, fmt.Errorf(
			"targets must cover exactly the advertised Provider types: %s",
			strings.Join(expected, ", "),
		)
	}
	return config, nil
}

func parseEmbeddingLiveConfig(raw string) (embeddingLiveConfig, error) {
	var config embeddingLiveConfig
	if err := decodeStrictJSON(raw, &config); err != nil {
		return embeddingLiveConfig{}, err
	}
	if err := validateHTTPSBaseURL(config.BaseURL); err != nil {
		return embeddingLiveConfig{}, err
	}
	if strings.TrimSpace(config.Model) == "" || containsControl(config.Model) {
		return embeddingLiveConfig{}, fmt.Errorf("model is required")
	}
	if config.Dimensions < 0 {
		return embeddingLiveConfig{}, fmt.Errorf("dimensions cannot be negative")
	}
	if config.TimeoutSecond < 0 {
		return embeddingLiveConfig{}, fmt.Errorf("timeout_seconds cannot be negative")
	}
	return config, nil
}

func decodeStrictJSON(raw string, dst any) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("configuration JSON is required")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	// Avoid including the raw configuration in errors because future fields
	// may be sensitive even though today's schema deliberately is not.
	if err == nil {
		return fmt.Errorf("multiple JSON documents are not allowed")
	}
	return fmt.Errorf("decode trailing JSON: %w", err)
}

func validateHTTPSBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("base_url must be an absolute HTTPS URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("base_url cannot contain a query or fragment")
	}
	if parsed.User != nil {
		return fmt.Errorf("base_url cannot contain credentials")
	}
	return nil
}

func validateCandidateBinary(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("CherryIN Provider adapter requires STELLA_SYSTEM_BINARY")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("exact candidate binary is unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("exact candidate binary must be an executable regular file")
	}
	return nil
}

func configTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultLiveTimeout
	}
	return time.Duration(seconds) * time.Second
}

func liveMarker(runID, targetID string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, runID+"-"+targetID)
	return "stella-live-" + strings.Trim(clean, "-")
}

func embeddingFixtureTexts() []string {
	return []string{
		"A mechanic replaced worn brake pads and inspected the car's hydraulic lines.",
		"Saturn is a gas giant whose prominent rings contain ice and rock particles.",
		"Sourdough bread develops flavor through slow fermentation of flour and water.",
		"Which passage discusses astronomy and a planet surrounded by rings?",
	}
}

func finiteVector(vector []float32) bool {
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

func cosineSimilarity(left, right []float32) float64 {
	var dot, leftNorm, rightNorm float64
	for i := range left {
		l := float64(left[i])
		r := float64(right[i])
		dot += l * r
		leftNorm += l * l
		rightNorm += r * r
	}
	if leftNorm == 0 || rightNorm == 0 {
		return -1
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func retryableProviderError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		strings.Contains(text, "timeout") ||
		strings.Contains(text, "429") ||
		strings.Contains(text, "rate limit") ||
		strings.Contains(text, "500") ||
		strings.Contains(text, "502") ||
		strings.Contains(text, "503") ||
		strings.Contains(text, "504")
}

type providerExternalError struct {
	reason    string
	retryable bool
}

func (e *providerExternalError) Error() string {
	return e.reason
}
