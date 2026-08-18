package library

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRealXbergScannedPDFUsesBridge(t *testing.T) {
	binary := os.Getenv("STELLA_TEST_XBERG_BINARY")
	fixture := os.Getenv("STELLA_TEST_SCANNED_PDF")
	if binary == "" || fixture == "" {
		t.Skip("set STELLA_TEST_XBERG_BINARY and STELLA_TEST_SCANNED_PDF for the real Xberg OCR contract test")
	}
	config, calls, closeProvider := realXbergVisionConfig(t)
	defer closeProvider()
	parser, err := NewXbergCLIParser(t.Context(), binary, func(context.Context) (VisionOCRConfig, error) {
		return config, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}
	native, err := parser.extract(
		t.Context(),
		absolute,
		xbergCanonicalArgs(mustFormatSpec(t, MediaTypePDF), true),
		xbergChildEnvironment(""),
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := xbergOCRCandidates(native)
	if err != nil || len(candidates) == 0 {
		t.Fatalf("real Xberg fixture has no OCR candidates: pages=%v error=%v", candidates, err)
	}
	profile, err := parser.Profile(t.Context(), MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := parser.Parse(t.Context(), fixture, MediaTypePDF, profile)
	if err != nil {
		t.Fatal(err)
	}
	if calls != nil && calls.Load() == 0 {
		t.Fatal("real Xberg extraction did not call the OCR bridge")
	}
	foundCandidateText := false
	allContent := strings.Builder{}
	for _, chunk := range chunks {
		allContent.WriteString(chunk.Content)
		if chunk.Content == "" || chunk.Locator.FirstPage == nil || chunk.Locator.LastPage == nil {
			continue
		}
		for _, page := range candidates {
			if page >= *chunk.Locator.FirstPage && page <= *chunk.Locator.LastPage {
				foundCandidateText = true
			}
		}
	}
	if !foundCandidateText {
		t.Fatalf("real Xberg OCR chunks contain no candidate-page text with a locator: %+v", chunks)
	}
	if calls != nil && !strings.Contains(allContent.String(), "Verified scanned page") {
		t.Fatalf("fake-provider OCR text was not merged into the Xberg result: %+v", chunks)
	}
}

func realXbergVisionConfig(t *testing.T) (VisionOCRConfig, *atomic.Int32, func()) {
	t.Helper()
	baseURL := os.Getenv("STELLA_TEST_VISION_BASE_URL")
	model := os.Getenv("STELLA_TEST_VISION_MODEL")
	apiKey := os.Getenv("STELLA_TEST_VISION_API_KEY")
	if baseURL != "" || model != "" || apiKey != "" {
		if baseURL == "" || model == "" || apiKey == "" {
			t.Fatal("STELLA_TEST_VISION_BASE_URL, STELLA_TEST_VISION_MODEL, and STELLA_TEST_VISION_API_KEY must be set together")
		}
		// The real-provider path remains opt-in so normal test runs never consume
		// external model quota or require credentials.
		return VisionOCRConfig{
			ProviderID: "integration", ProviderType: "openai", Enabled: true,
			Model: model, BaseURL: baseURL, APIKey: apiKey,
		}, nil, func() {}
	}

	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodPost {
			t.Errorf("provider method = %s", request.Method)
		}
		_, _ = io.WriteString(w, `{"id":"fixture","object":"chat.completion","created":1,"model":"fixture-vision","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"STELLA_OCR_V1:TEXT\nVerified scanned page"}}]}`)
	}))
	return VisionOCRConfig{
		ProviderID: "fixture", ProviderType: "openai", Enabled: true,
		Model: "fixture-vision", BaseURL: provider.URL + "/v1", APIKey: "fixture-key",
	}, &calls, provider.Close
}
