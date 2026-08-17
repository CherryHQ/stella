package library

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestXbergCLIParserNativePDFUsesOnePass(t *testing.T) {
	t.Parallel()
	var calls []xbergCommand
	run := func(_ context.Context, _ string, command xbergCommand) ([]byte, []byte, error) {
		calls = append(calls, command)
		if slices.Equal(command.Args, []string{"version", "--format", "json"}) {
			return []byte(`{"version":"1.0.14"}`), nil, nil
		}
		return []byte(nativeXbergFixture), nil, nil
	}
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", nil, run)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := parser.Profile(t.Context(), MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profile, "xberg-cli-adapter:v3") || !strings.Contains(profile, "cli=1.0.14") {
		t.Fatalf("profile = %q", profile)
	}
	chunks, err := parser.Parse(t.Context(), "source.pdf", MediaTypePDF, profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Content != "Policy text" || chunks[0].Locator.FirstPage == nil || *chunks[0].Locator.FirstPage != 1 {
		t.Fatalf("chunks = %+v", chunks)
	}
	if len(calls) != 2 {
		t.Fatalf("Xberg calls = %d, want version plus one extraction", len(calls))
	}
	if !slices.Contains(calls[1].Args, "true") || slices.ContainsFunc(calls[1].Env, func(value string) bool {
		return strings.HasPrefix(value, "XBERG_LLM_API_KEY=")
	}) {
		t.Fatalf("native command = %+v", calls[1])
	}
}

func TestXbergCLIParserOCRsOnlyCandidatePages(t *testing.T) {
	t.Parallel()
	config := VisionOCRConfig{
		ProviderID: "vision", ProviderType: "openai", Enabled: true,
		Model: "vision-model", BaseURL: "https://vision.example/v1", APIKey: "real-secret",
	}
	var calls []xbergCommand
	run := func(_ context.Context, _ string, command xbergCommand) ([]byte, []byte, error) {
		calls = append(calls, command)
		switch len(calls) {
		case 1:
			return []byte(`{"version":"1.0.14"}`), nil, nil
		case 2:
			return []byte(scannedXbergFixture), nil, nil
		default:
			return []byte(ocrXbergFixture), nil, nil
		}
	}
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", func(context.Context) (VisionOCRConfig, error) {
		return config, nil
	}, run)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := parser.Profile(t.Context(), MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := parser.Parse(t.Context(), "source.pdf", MediaTypePDF, profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Content != "OCR policy" {
		t.Fatalf("chunks = %+v", chunks)
	}
	if len(calls) != 3 {
		t.Fatalf("Xberg calls = %d, want two extraction passes", len(calls))
	}
	override := flagValue(t, calls[2].Args, "--config-json-base64")
	decoded, err := base64.StdEncoding.DecodeString(override)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		ForceOCRPages []uint32 `json:"force_ocr_pages"`
		Concurrency   struct {
			MaxThreads int `json:"max_threads"`
		} `json:"concurrency"`
	}
	if err := json.Unmarshal(decoded, &settings); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(settings.ForceOCRPages, []uint32{2, 3}) || settings.Concurrency.MaxThreads != ocrMaxThreads {
		t.Fatalf("OCR override = %+v", settings)
	}
	environment := strings.Join(calls[2].Env, "\n")
	if strings.Contains(environment, config.APIKey) || !strings.Contains(environment, "XBERG_LLM_API_KEY=") {
		t.Fatalf("second-pass environment leaked or omitted credentials: %q", environment)
	}
}

func TestXbergCLIParserRejectsCandidateBudgetBeforeVisionUse(t *testing.T) {
	t.Parallel()
	pages := make([]uint32, maxOCRCandidatePages+1)
	for i := range pages {
		pages[i] = uint32(i + 1)
	}
	fixture, err := json.Marshal(map[string]any{"result": map[string]any{
		"metadata": map[string]any{"format": map[string]any{"scanned_pages": pages}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	visionCalls := 0
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", func(context.Context) (VisionOCRConfig, error) {
		visionCalls++
		return VisionOCRConfig{}, nil
	}, xbergFixtureRunner(string(fixture), nil))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := parser.Profile(t.Context(), MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	_, err = parser.Parse(t.Context(), "source.pdf", MediaTypePDF, profile)
	if !errors.Is(err, ErrOCRPageLimit) {
		t.Fatalf("error = %v, want ErrOCRPageLimit", err)
	}
	// Profile resolution occurs once outside Parse and once to bind Parse. The
	// budget failure must happen before a third resolution can expose credentials.
	if visionCalls != 2 {
		t.Fatalf("Vision resolver calls = %d, want 2", visionCalls)
	}
}

func TestXbergCLIParserRequiresVisionOnlyForScannedPDF(t *testing.T) {
	t.Parallel()
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", nil, xbergFixtureRunner(scannedXbergFixture, nil))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := parser.Profile(t.Context(), MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse(t.Context(), "source.pdf", MediaTypePDF, profile); !errors.Is(err, ErrOCRConfiguration) {
		t.Fatalf("error = %v, want ErrOCRConfiguration", err)
	}
}

func TestXbergCLIParserRejectsChangedProfile(t *testing.T) {
	t.Parallel()
	config := VisionOCRConfig{ProviderID: "vision", ProviderType: "openai", Enabled: true, Model: "a", APIKey: "key"}
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", func(context.Context) (VisionOCRConfig, error) {
		return config, nil
	}, xbergFixtureRunner(nativeXbergFixture, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse(t.Context(), "source.pdf", MediaTypePDF, "old-profile"); !errors.Is(err, ErrGenerationChanged) {
		t.Fatalf("error = %v, want ErrGenerationChanged", err)
	}
	if _, err := parser.Profile(t.Context(), MediaTypeText); !errors.Is(err, ErrUnsupportedFileType) {
		t.Fatalf("text profile error = %v", err)
	}
}

func TestXbergCLIParserProfileExcludesAPIKeyButBindsOCRIdentity(t *testing.T) {
	t.Parallel()
	config := VisionOCRConfig{
		ProviderID: "vision", ProviderType: "openai", Enabled: true,
		Model: "model-a", BaseURL: "https://vision.example/v1", APIKey: "first-secret",
	}
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", func(context.Context) (VisionOCRConfig, error) {
		return config, nil
	}, xbergFixtureRunner(nativeXbergFixture, nil))
	if err != nil {
		t.Fatal(err)
	}
	first, err := parser.Profile(t.Context(), MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	config.APIKey = "rotated-secret"
	rotated, err := parser.Profile(t.Context(), MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	if rotated != first || strings.Contains(first, "secret") {
		t.Fatalf("API-key rotation changed or leaked profile: before=%q after=%q", first, rotated)
	}
	config.Model = "model-b"
	changed, err := parser.Profile(t.Context(), MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("OCR model identity did not change the parser profile")
	}
}

func TestXbergCLIParserPreparedAttemptUsesOneVisionSnapshot(t *testing.T) {
	t.Parallel()
	config := VisionOCRConfig{
		ProviderID: "vision", ProviderType: "openai", Enabled: true,
		Model: "model-a", BaseURL: "https://vision.example/v1", APIKey: "secret",
	}
	var extractionCalls int
	run := func(_ context.Context, _ string, command xbergCommand) ([]byte, []byte, error) {
		if slices.Equal(command.Args, []string{"version", "--format", "json"}) {
			return []byte(`{"version":"1.0.14"}`), nil, nil
		}
		extractionCalls++
		if extractionCalls == 1 {
			return []byte(scannedXbergFixture), nil, nil
		}
		return []byte(ocrXbergFixture), nil, nil
	}
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", func(context.Context) (VisionOCRConfig, error) {
		return config, nil
	}, run)
	if err != nil {
		t.Fatal(err)
	}
	resolveCalls := 0
	parser.resolveVision = func(context.Context) (VisionOCRConfig, error) {
		resolveCalls++
		return config, nil
	}
	attempt, err := parser.PrepareAttempt(t.Context(), MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	parser.resolveVision = func(context.Context) (VisionOCRConfig, error) {
		return VisionOCRConfig{}, errors.New("prepared attempt reloaded Vision configuration")
	}
	if _, err := attempt.Parse(t.Context(), "source.pdf"); err != nil {
		t.Fatalf("prepared parse: %v", err)
	}
	if resolveCalls != 1 || extractionCalls != 2 {
		t.Fatalf("Vision resolutions=%d extraction calls=%d, want 1/2", resolveCalls, extractionCalls)
	}
}

func TestXbergCLIParserPreservesOperationalErrors(t *testing.T) {
	t.Parallel()
	operational := errors.New("xberg exited")
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", nil, xbergFixtureRunner("", operational))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := parser.Profile(t.Context(), MediaTypeDOCX)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse(t.Context(), "source.docx", MediaTypeDOCX, profile); !errors.Is(err, operational) {
		t.Fatalf("operational error = %v", err)
	}
}

func TestCappedBufferBoundsCommandOutput(t *testing.T) {
	t.Parallel()
	buffer := &cappedBuffer{max: 4}
	if written, err := buffer.Write([]byte("abcdef")); err != nil || written != 6 {
		t.Fatalf("Write = %d, %v", written, err)
	}
	if !buffer.exceeded || buffer.String() != "abcd" {
		t.Fatalf("capped buffer = %q, exceeded=%v", buffer.String(), buffer.exceeded)
	}
}

func xbergFixtureRunner(output string, extractErr error) xbergCommandRunner {
	return func(_ context.Context, _ string, command xbergCommand) ([]byte, []byte, error) {
		if slices.Equal(command.Args, []string{"version", "--format", "json"}) {
			return []byte(`{"version":"1.0.14"}`), nil, nil
		}
		return []byte(output), nil, extractErr
	}
}

func flagValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag {
			return args[index+1]
		}
	}
	t.Fatalf("flag %s is missing from %v", flag, args)
	return ""
}

const (
	nativeXbergFixture  = `{"result":{"metadata":{"format":{"scanned_pages":[]}},"pages":[{"page_number":1,"is_blank":false}],"chunks":[{"content":"Policy text","metadata":{"byte_start":0,"byte_end":11,"first_page":1,"last_page":1}}]}}`
	scannedXbergFixture = `{"result":{"metadata":{"format":{"scanned_pages":[3,2]}},"pages":[{"page_number":2,"is_blank":true}],"chunks":[]}}`
	ocrXbergFixture     = `{"result":{"metadata":{"format":{"scanned_pages":[2,3]}},"pages":[{"page_number":2,"is_blank":false}],"chunks":[{"content":"OCR policy","metadata":{"byte_start":0,"byte_end":10,"first_page":2,"last_page":3}}]}}`
)
