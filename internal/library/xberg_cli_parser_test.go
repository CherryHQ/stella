package library

import (
	"context"
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
	if !strings.Contains(profile, "xberg-cli-adapter:v2") || !strings.Contains(profile, "cli=1.1.0") || !strings.Contains(profile, "args_sha256=") {
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
	if len(calls) != 2 {
		t.Fatalf("Xberg calls = %v", calls)
	}
	wantArgs := append([]string{"extract", calls[1][1]}, xbergCanonicalArgs()...)
	if !slices.Equal(calls[1], wantArgs) {
		t.Fatalf("extract arguments = %v", calls[1])
	}
}

func TestXbergCanonicalArgsPinDeterministicExtraction(t *testing.T) {
	t.Parallel()
	want := []string{
		"--no-config-discovery", "--disable-ocr", "true", "--quality", "false", "--force-ocr", "false",
		"--include-structure", "true", "--content-format", "markdown", "--extract-pages", "true",
		"--page-markers", "false", "--no-cache", "true", "--chunk", "true", "--chunk-size", "1000",
		"--chunk-overlap", "200", "--format", "json",
	}
	if got := xbergCanonicalArgs(); !slices.Equal(got, want) {
		t.Fatalf("Xberg canonical args = %v", got)
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

func TestXbergCLIParserRequiresHomeStorageAdmissionBeforeExecution(t *testing.T) {
	gateErr := errors.New("storage closed")
	parser, err := newXbergCLIParser(t.Context(), "/test/xberg", xbergFixtureRunner(`{"version":"1"}`, nil))
	if err != nil {
		t.Fatal(err)
	}
	parser.admission = func(context.Context) error { return gateErr }
	parser.run = func(context.Context, string, []string) ([]byte, []byte, error) {
		t.Fatal("Xberg executed while Home storage admission was closed")
		return nil, nil, nil
	}
	if _, err := parser.Parse(t.Context(), "source.pdf", MediaTypePDF); !errors.Is(err, gateErr) {
		t.Fatalf("closed admission error = %v", err)
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
