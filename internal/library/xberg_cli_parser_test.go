package library

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestXbergCLIParserProfilesAndMapsChunkMetadata(t *testing.T) {
	t.Parallel()
	var calls [][]string
	run := func(_ context.Context, _ string, args []string) ([]byte, []byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if slices.Equal(args, []string{"version", "--format", "json"}) {
			return []byte(`{"name":"xberg-cli","version":"1.1.0"}`), nil, nil
		}
		if slices.Equal(args, []string{"formats", "--format", "json"}) {
			return xbergFormatsFixture(t), nil, nil
		}
		return []byte(`{"result":{"chunks":[{"content":"Policy text","metadata":{"byte_start":4,"byte_end":15,"first_page":2,"last_page":3,"heading_context":{"headings":[{"level":1,"text":"Policy"},{"level":2,"text":"Approval"}]}}}]}}`), nil, nil
	}
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", run)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := parser.Profile(MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	for _, mediaType := range XbergMediaTypes() {
		got, err := parser.Profile(mediaType)
		if err != nil || got == "" {
			t.Fatalf("Profile(%q) = %q, %v", mediaType, got, err)
		}
	}
	if !strings.Contains(profile, "xberg-cli-adapter:v4") || !strings.Contains(profile, "cli=1.1.0") || !strings.Contains(profile, "args_sha256=") {
		t.Fatalf("profile = %q", profile)
	}
	if _, err := parser.Profile(MediaTypeText); !errors.Is(err, ErrUnsupportedFileType) {
		t.Fatalf("text profile error = %v, want ErrUnsupportedFileType", err)
	}

	chunks, err := parser.Parse(t.Context(), "source.pdf", MediaTypePDF)
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
	wantArgs := append([]string{"extract", calls[2][1]}, xbergCanonicalArgs(MediaTypePDF)...)
	if !slices.Equal(calls[2], wantArgs) {
		t.Fatalf("extract arguments = %v", calls[2])
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
	if got := xbergCanonicalArgs(MediaTypePDF); !slices.Equal(got, want) {
		t.Fatalf("Xberg canonical args = %v", got)
	}
	presentation := xbergCanonicalArgs(MediaTypePPTX)
	if !slices.Contains(presentation, xbergPresentationConfigJSON) {
		t.Fatalf("presentation args = %v", presentation)
	}
	for _, mediaType := range []string{MediaTypePPT, MediaTypeODP} {
		degraded := xbergCanonicalArgs(mediaType)
		if slices.Contains(degraded, xbergPresentationConfigJSON) || !slices.Contains(degraded, xbergChunkingConfigJSON) {
			t.Fatalf("degraded presentation args for %s = %v", mediaType, degraded)
		}
	}
}

func TestXbergCLIParserSuppressesUnpromisedPageCoordinates(t *testing.T) {
	t.Parallel()
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", xbergFixtureRunner(
		`{"result":{"chunks":[{"content":"Chapter text","metadata":{"byte_start":0,"byte_end":12,"first_page":4,"last_page":5,"heading_path":["Chapter"]}}]}}`,
		nil,
	))
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := parser.Parse(t.Context(), "source.docx", MediaTypeDOCX)
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
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", xbergFixtureRunner(
		`{"result":{"chunks":[{"content":"# Section A\n\nAlpha","metadata":{"byte_start":0,"byte_end":18,"first_page":1,"last_page":2,"heading_context":{"headings":[{"text":"Section A"}]}}}]}}`,
		nil,
	))
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := parser.Parse(t.Context(), "source.ppt", MediaTypePPT)
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
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", xbergFixtureRunner(
		`{"result":{"chunks":[{"content":"# Section A\n\nAlpha\n\n# Section B\n\nBeta","metadata":{"byte_start":0,"byte_end":39,"heading_context":{"headings":[{"text":"Section A"}]}}}]}}`,
		nil,
	))
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := parser.Parse(t.Context(), "source.html", MediaTypeHTML)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || len(chunks[0].Locator.HeadingPath) != 0 {
		t.Fatalf("ambiguous heading locator = %+v", chunks[0].Locator)
	}
}

func TestXbergCLIParserRejectsRuntimeWithoutRequiredFormats(t *testing.T) {
	t.Parallel()
	run := func(_ context.Context, _ string, args []string) ([]byte, []byte, error) {
		if slices.Equal(args, []string{"version", "--format", "json"}) {
			return []byte(`{"version":"1.0.14"}`), nil, nil
		}
		return []byte(`[{"extension":"pdf"},{"extension":"docx"}]`), nil, nil
	}
	if _, err := newXbergCLIParser(t.Context(), "/test/xberg", run); err == nil || !strings.Contains(err.Error(), "required extension") {
		t.Fatalf("error = %v, want missing required extension", err)
	}
}

func TestXbergCLIParserAcceptsXHTMLThroughHTMLAlias(t *testing.T) {
	t.Parallel()
	run := func(_ context.Context, _ string, args []string) ([]byte, []byte, error) {
		if slices.Equal(args, []string{"version", "--format", "json"}) {
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
	if _, err := newXbergCLIParser(t.Context(), "/test/xberg", run); err != nil {
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
			parser, err := newXbergCLIParser(t.Context(), "/test/xberg", run)
			if err != nil {
				t.Fatal(err)
			}
			_, err = parser.Parse(t.Context(), "source.docx", MediaTypeDOCX)
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
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", xbergFixtureRunner("", operational))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse(t.Context(), "source.pdf", MediaTypePDF); !errors.Is(err, operational) {
		t.Fatalf("operational error = %v", err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	parser.run = func(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
		return nil, nil, ctx.Err()
	}
	if _, err := parser.Parse(cancelled, "source.pdf", MediaTypePDF); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
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
	return func(_ context.Context, _ string, args []string) ([]byte, []byte, error) {
		if slices.Equal(args, []string{"version", "--format", "json"}) {
			return []byte(`{"version":"1.1.0"}`), nil, nil
		}
		if slices.Equal(args, []string{"formats", "--format", "json"}) {
			return xbergFormatsFixture(nil), nil, nil
		}
		return []byte(output), nil, extractErr
	}
}

func xbergFormatsFixture(t *testing.T) []byte {
	formats := make([]map[string]string, 0, len(XbergMediaTypes()))
	for _, mediaType := range XbergMediaTypes() {
		extension, err := canonicalExtension(mediaType)
		if err != nil {
			if t != nil {
				t.Fatal(err)
			}
			panic(err)
		}
		formats = append(formats, map[string]string{"extension": strings.TrimPrefix(extension, ".")})
	}
	data, err := json.Marshal(formats)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return data
}
