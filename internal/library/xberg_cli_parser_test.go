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

func TestXbergCLIParserProfilesAndMapsChunkMetadata(t *testing.T) {
	t.Parallel()
	var calls []xbergCommand
	run := func(_ context.Context, _ string, command xbergCommand) ([]byte, []byte, error) {
		calls = append(calls, command)
		if slices.Equal(command.Args, []string{"version", "--format", "json"}) {
			return []byte(`{"name":"xberg-cli","version":"1.1.0"}`), nil, nil
		}
		if slices.Equal(command.Args, []string{"formats", "--format", "json"}) {
			return xbergFormatsFixture(), nil, nil
		}
		return []byte(`{"result":{"chunks":[{"content":"Policy text","metadata":{"byte_start":4,"byte_end":15,"first_page":2,"last_page":3,"heading_context":{"headings":[{"level":1,"text":"Policy"},{"level":2,"text":"Approval"}]}}}]}}`), nil, nil
	}
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", nil, run)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := parser.Profile(t.Context(), MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	for _, mediaType := range XbergMediaTypes() {
		got, err := parser.Profile(t.Context(), mediaType)
		if err != nil || got == "" {
			t.Fatalf("Profile(%q) = %q, %v", mediaType, got, err)
		}
	}
	if !strings.Contains(profile, "xberg-cli-adapter:v5") || !strings.Contains(profile, "cli=1.1.0") || !strings.Contains(profile, "recipe_sha256=") {
		t.Fatalf("profile = %q", profile)
	}
	if _, err := parser.Profile(t.Context(), MediaTypeText); !errors.Is(err, ErrUnsupportedFileType) {
		t.Fatalf("text profile error = %v, want ErrUnsupportedFileType", err)
	}
	chunks, err := parser.Parse(t.Context(), "source.pdf", MediaTypePDF, profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Content != "Policy text" {
		t.Fatalf("chunks = %+v", chunks)
	}
	locator := chunks[0].Locator
	if locator.ByteStart != 4 || locator.ByteEnd != 15 || locator.FirstPage == nil || *locator.FirstPage != 2 ||
		locator.LastPage == nil || *locator.LastPage != 3 || !slices.Equal(locator.HeadingPath, []string{"Policy", "Approval"}) {
		t.Fatalf("locator = %+v", locator)
	}
	if len(calls) != 3 {
		t.Fatalf("Xberg calls = %v", calls)
	}
	wantArgs := append([]string{"extract", calls[2].Args[1]}, xbergCanonicalArgs(mustFormatSpec(t, MediaTypePDF), true)...)
	if !slices.Equal(calls[2].Args, wantArgs) {
		t.Fatalf("extract arguments = %v", calls[2].Args)
	}
}

func TestXbergCanonicalArgsPinDeterministicExtraction(t *testing.T) {
	t.Parallel()
	want := []string{
		"--no-config-discovery", "--disable-ocr", "true", "--quality", "false", "--force-ocr", "false",
		"--include-structure", "true", "--content-format", "markdown", "--extract-pages", "true",
		"--page-markers", "false", "--no-cache", "true", "--config-json", xbergChunkingConfigJSON,
		"--format", "json",
	}
	if got := xbergCanonicalArgs(mustFormatSpec(t, MediaTypePDF), true); !slices.Equal(got, want) {
		t.Fatalf("Xberg canonical args = %v", got)
	}
	presentation := xbergCanonicalArgs(mustFormatSpec(t, MediaTypePPTX), true)
	if !slices.Contains(presentation, xbergPresentationConfigJSON) {
		t.Fatalf("presentation args = %v", presentation)
	}
	for _, mediaType := range []string{MediaTypePPT, MediaTypeODP} {
		degraded := xbergCanonicalArgs(mustFormatSpec(t, mediaType), true)
		if slices.Contains(degraded, xbergPresentationConfigJSON) || !slices.Contains(degraded, xbergChunkingConfigJSON) {
			t.Fatalf("degraded presentation args for %s = %v", mediaType, degraded)
		}
	}
}

func TestXbergCLIParserSuppressesUnpromisedPageCoordinates(t *testing.T) {
	t.Parallel()
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", nil, xbergFixtureRunner(
		`{"result":{"chunks":[{"content":"Chapter text","metadata":{"byte_start":0,"byte_end":12,"first_page":4,"last_page":5,"heading_path":["Chapter"]}}]}}`,
		nil,
	))
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := parseWithCurrentProfile(t.Context(), parser, "source.docx", MediaTypeDOCX)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Locator.FirstPage != nil || chunks[0].Locator.LastPage != nil ||
		!slices.Equal(chunks[0].Locator.HeadingPath, []string{"Chapter"}) {
		t.Fatalf("DOCX locator = %+v", chunks[0].Locator)
	}
}

func TestXbergCLIParserDegradesUnavailablePresentationCoordinates(t *testing.T) {
	t.Parallel()
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", nil, xbergFixtureRunner(
		`{"result":{"chunks":[{"content":"# Section A\n\nAlpha","metadata":{"byte_start":0,"byte_end":18,"first_page":1,"last_page":2,"heading_context":{"headings":[{"text":"Section A"}]}}}]}}`,
		nil,
	))
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := parseWithCurrentProfile(t.Context(), parser, "source.ppt", MediaTypePPT)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Locator.FirstPage != nil || chunks[0].Locator.LastPage != nil ||
		len(chunks[0].Locator.HeadingPath) != 0 {
		t.Fatalf("degraded locator = %+v", chunks[0].Locator)
	}
}

func TestXbergCLIParserSuppressesAmbiguousMultiHeadingContext(t *testing.T) {
	t.Parallel()
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", nil, xbergFixtureRunner(
		`{"result":{"chunks":[{"content":"# Section A\n\nAlpha\n\n# Section B\n\nBeta","metadata":{"byte_start":0,"byte_end":39,"heading_context":{"headings":[{"text":"Section A"}]}}}]}}`,
		nil,
	))
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := parseWithCurrentProfile(t.Context(), parser, "source.html", MediaTypeHTML)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || len(chunks[0].Locator.HeadingPath) != 0 {
		t.Fatalf("ambiguous heading locator = %+v", chunks[0].Locator)
	}
}

func TestXbergCLIParserRejectsRuntimeWithoutRequiredFormats(t *testing.T) {
	t.Parallel()
	run := func(_ context.Context, _ string, command xbergCommand) ([]byte, []byte, error) {
		if slices.Equal(command.Args, []string{"version", "--format", "json"}) {
			return []byte(`{"version":"1.0.14"}`), nil, nil
		}
		return []byte(`[{"extension":"pdf"},{"extension":"docx"}]`), nil, nil
	}
	if _, err := newXbergCLIParser(t.Context(), "/test/xberg", nil, run); err == nil || !strings.Contains(err.Error(), "required extension") {
		t.Fatalf("error = %v, want missing required extension", err)
	}
}

func TestXbergCLIParserAcceptsXHTMLThroughHTMLAlias(t *testing.T) {
	t.Parallel()
	run := func(_ context.Context, _ string, command xbergCommand) ([]byte, []byte, error) {
		if slices.Equal(command.Args, []string{"version", "--format", "json"}) {
			return []byte(`{"version":"1.0.14"}`), nil, nil
		}
		formats := make([]map[string]string, 0, len(XbergMediaTypes()))
		for _, mediaType := range XbergMediaTypes() {
			if mediaType == MediaTypeXHTML {
				continue
			}
			extension, err := canonicalExtension(mediaType)
			if err != nil {
				t.Fatal(err)
			}
			format := map[string]string{"extension": strings.TrimPrefix(extension, ".")}
			if mediaType == MediaTypeHTML {
				format["mime_type"] = MediaTypeHTML
			}
			formats = append(formats, format)
		}
		data, err := json.Marshal(formats)
		if err != nil {
			t.Fatal(err)
		}
		return data, nil, nil
	}
	if _, err := newXbergCLIParser(t.Context(), "/test/xberg", nil, run); err != nil {
		t.Fatalf("XHTML alias probe error = %v", err)
	}
}

func TestXbergCLIParserRejectsEmptyAndInvalidResults(t *testing.T) {
	t.Parallel()
	for name, output := range map[string]string{
		"empty":     `{"result":{"chunks":[]}}`,
		"malformed": `{"result":`,
		"trailing":  `{"result":{"chunks":[]}} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			run := xbergFixtureRunner(output, nil)
			parser, err := newXbergCLIParser(t.Context(), "/test/xberg", nil, run)
			if err != nil {
				t.Fatal(err)
			}
			_, err = parseWithCurrentProfile(t.Context(), parser, "source.docx", MediaTypeDOCX)
			if name == "empty" && !errors.Is(err, ErrNoExtractedText) {
				t.Fatalf("error = %v, want ErrNoExtractedText", err)
			}
			if name != "empty" && !errors.Is(err, ErrInvalidParserData) {
				t.Fatalf("error = %v, want ErrInvalidParserData", err)
			}
		})
	}
}

func TestXbergCLIParserPreservesCancellationAndOperationalErrors(t *testing.T) {
	t.Parallel()
	operational := errors.New("xberg exited")
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", nil, xbergFixtureRunner("", operational))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseWithCurrentProfile(t.Context(), parser, "source.pdf", MediaTypePDF); !errors.Is(err, operational) {
		t.Fatalf("operational error = %v", err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	parser.run = func(ctx context.Context, _ string, _ xbergCommand) ([]byte, []byte, error) {
		return nil, nil, exitCodeFixture(23)
	}
	if _, err := parseWithCurrentProfile(cancelled, parser, "source.pdf", MediaTypePDF); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}
}

func TestXbergCLIParserKeepsUnknownProcessExitsRetryable(t *testing.T) {
	t.Parallel()
	for _, code := range []exitCodeFixture{23, 101} {
		parser, err := newXbergCLIParser(t.Context(), "/test/xberg", nil, xbergFixtureRunner("", code))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseWithCurrentProfile(t.Context(), parser, "source.pdf", MediaTypePDF); !errors.Is(err, code) {
			t.Fatalf("process exit %d error = %v, want retryable wrapped error", code, err)
		}
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
		switch {
		case slices.Equal(command.Args, []string{"version", "--format", "json"}):
			return []byte(`{"version":"1.0.14"}`), nil, nil
		case slices.Equal(command.Args, []string{"formats", "--format", "json"}):
			return xbergFormatsFixture(), nil, nil
		case len(calls) == 3:
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
	chunks, err := parseWithCurrentProfile(t.Context(), parser, "source.pdf", MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Content != "OCR policy" {
		t.Fatalf("chunks = %+v", chunks)
	}
	if len(calls) != 4 {
		t.Fatalf("Xberg calls = %d, want two probes plus two extraction passes", len(calls))
	}
	override := flagValue(t, calls[3].Args, "--config-json-base64")
	if slices.Contains(calls[3].Args, "--config-json") {
		t.Fatalf("OCR command contains competing config inputs: %v", calls[3].Args)
	}
	decoded, err := base64.StdEncoding.DecodeString(override)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		ForceOCRPages []uint32 `json:"force_ocr_pages"`
		Chunking      struct {
			MaxChars int `json:"max_chars"`
		} `json:"chunking"`
		Concurrency struct {
			MaxThreads int `json:"max_threads"`
		} `json:"concurrency"`
	}
	if err := json.Unmarshal(decoded, &settings); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(settings.ForceOCRPages, []uint32{2, 3}) || settings.Concurrency.MaxThreads != ocrMaxThreads || settings.Chunking.MaxChars != 1000 {
		t.Fatalf("OCR override = %+v", settings)
	}
	environment := strings.Join(calls[3].Env, "\n")
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
	_, err = parseWithCurrentProfile(t.Context(), parser, "source.pdf", MediaTypePDF)
	if !errors.Is(err, ErrOCRPageLimit) {
		t.Fatalf("error = %v, want ErrOCRPageLimit", err)
	}
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
	if _, err := parseWithCurrentProfile(t.Context(), parser, "source.pdf", MediaTypePDF); !errors.Is(err, ErrOCRConfiguration) {
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
		switch {
		case slices.Equal(command.Args, []string{"version", "--format", "json"}):
			return []byte(`{"version":"1.0.14"}`), nil, nil
		case slices.Equal(command.Args, []string{"formats", "--format", "json"}):
			return xbergFormatsFixture(), nil, nil
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

func TestXbergCommandReceivesOnlyRuntimeEnvironment(t *testing.T) {
	t.Setenv("STELLA_TEST_PROVIDER_KEY", "must-not-leak")
	t.Setenv("PATH", "/xberg-test-path")
	cmd := newXbergCommand(t.Context(), "/test/xberg", nil)
	if !slices.Contains(cmd.Env, "PATH=/xberg-test-path") {
		t.Fatalf("Xberg environment does not preserve PATH: %v", cmd.Env)
	}
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "STELLA_TEST_PROVIDER_KEY=") {
			t.Fatalf("Xberg environment leaked provider key: %v", cmd.Env)
		}
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
			return []byte(`{"version":"1.1.0"}`), nil, nil
		}
		if slices.Equal(command.Args, []string{"formats", "--format", "json"}) {
			return xbergFormatsFixture(), nil, nil
		}
		return []byte(output), nil, extractErr
	}
}

func parseWithCurrentProfile(ctx context.Context, parser *XbergCLIParser, path, mediaType string) ([]ParsedChunk, error) {
	profile, err := parser.Profile(ctx, mediaType)
	if err != nil {
		return nil, err
	}
	return parser.Parse(ctx, path, mediaType, profile)
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

func xbergFormatsFixture() []byte {
	// Keep the unit runner independent from Stella's registry. The packaged
	// contract test separately probes the real 1.0.14 binary, while this frozen
	// subset makes registry additions fail until their runtime admission is
	// deliberately represented here as well.
	return []byte(`[
		{"extension":"pdf"},{"extension":"doc"},{"extension":"docx"},
		{"extension":"odt"},{"extension":"rtf"},{"extension":"xls"},
		{"extension":"xlsx"},{"extension":"ods"},{"extension":"csv"},
		{"extension":"tsv"},{"extension":"ppt"},{"extension":"pptx"},
		{"extension":"odp"},{"extension":"html","mime_type":"text/html"},
		{"extension":"epub"},{"extension":"fb2"},{"extension":"mdx"},
		{"extension":"rst"},{"extension":"org"},{"extension":"json"},
		{"extension":"yaml"},{"extension":"toml"},{"extension":"xml"}
	]`)
}

type exitCodeFixture int

func (e exitCodeFixture) Error() string { return "Xberg exited" }

func (e exitCodeFixture) ExitCode() int { return int(e) }

const (
	nativeXbergFixture  = `{"result":{"metadata":{"format":{"scanned_pages":[]}},"pages":[{"page_number":1,"is_blank":false}],"chunks":[{"content":"Policy text","metadata":{"byte_start":0,"byte_end":11,"first_page":1,"last_page":1}}]}}`
	scannedXbergFixture = `{"result":{"metadata":{"format":{"scanned_pages":[3,2]}},"pages":[{"page_number":2,"is_blank":true}],"chunks":[]}}`
	ocrXbergFixture     = `{"result":{"metadata":{"format":{"scanned_pages":[2,3]}},"pages":[{"page_number":2,"is_blank":false}],"chunks":[{"content":"OCR policy","metadata":{"byte_start":0,"byte_end":10,"first_page":2,"last_page":3}}]}}`
)
