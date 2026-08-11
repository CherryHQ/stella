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
		return []byte(`{"result":{"chunks":[{"content":"Policy text","metadata":{"byte_start":4,"byte_end":15,"first_page":2,"last_page":3,"heading_path":["Policy","Approval"]}}]}}`), nil, nil
	}
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", run)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := parser.Profile(MediaTypePDF)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profile, "cli=1.1.0") || !strings.Contains(profile, "config_sha256=") {
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
	if len(calls) != 2 || len(calls[1]) != 7 || calls[1][0] != "extract" || calls[1][2] != "--no-config-discovery" ||
		calls[1][3] != "--config-json" || calls[1][4] != xbergCanonicalConfig || calls[1][5] != "--format" || calls[1][6] != "json" {
		t.Fatalf("extract arguments = %v", calls[1])
	}
}

func TestXbergCanonicalConfigPinsDeterministicExtraction(t *testing.T) {
	t.Parallel()
	var config struct {
		UseCache                bool   `json:"use_cache"`
		EnableQualityProcessing bool   `json:"enable_quality_processing"`
		DisableOCR              bool   `json:"disable_ocr"`
		ForceOCR                bool   `json:"force_ocr"`
		IncludeStructure        bool   `json:"include_document_structure"`
		OutputFormat            string `json:"output_format"`
		Chunking                struct {
			MaxCharacters int    `json:"max_characters"`
			Overlap       int    `json:"overlap"`
			ChunkerType   string `json:"chunker_type"`
			Sizing        string `json:"sizing"`
			Trim          bool   `json:"trim"`
		} `json:"chunking"`
		Pages struct {
			ExtractPages      bool `json:"extract_pages"`
			InsertPageMarkers bool `json:"insert_page_markers"`
		} `json:"pages"`
	}
	if err := json.Unmarshal([]byte(xbergCanonicalConfig), &config); err != nil {
		t.Fatal(err)
	}
	if config.UseCache || config.EnableQualityProcessing || !config.DisableOCR || config.ForceOCR ||
		!config.IncludeStructure || config.OutputFormat != "markdown" || config.Chunking.MaxCharacters != 1_000 ||
		config.Chunking.Overlap != 200 || config.Chunking.ChunkerType != "text" || config.Chunking.Sizing != "characters" ||
		!config.Chunking.Trim || !config.Pages.ExtractPages || config.Pages.InsertPageMarkers {
		t.Fatalf("Xberg canonical config drifted: %+v", config)
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
		return []byte(output), nil, extractErr
	}
}
