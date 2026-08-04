package knowledge

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/manifestplugins"
)

func TestXbergParserParse(t *testing.T) {
	t.Parallel()

	envelope := map[string]any{
		"result": map[string]any{
			"content": "# Handbook\n\nTravel policy details.",
			"chunks": []any{
				map[string]any{
					"content":    "# Handbook\n\nTravel policy details.",
					"chunk_type": "heading",
					"metadata": map[string]any{
						"byte_start":   0,
						"byte_end":     35,
						"chunk_index":  0,
						"total_chunks": 1,
						"first_page":   3,
						"last_page":    4,
						"heading_context": map[string]any{
							"headings": []any{
								map[string]any{"level": 1, "text": "Handbook"},
								map[string]any{"level": 2, "text": "Travel"},
							},
						},
					},
				},
			},
		},
		"extraction_time_ms": 12.5,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingXbergRunner{stdout: payload}
	parser, err := newXbergParserWithRunner(DefaultXbergParserConfig("/managed/xberg"), runner)
	if err != nil {
		t.Fatal(err)
	}

	got, err := parser.Parse(context.Background(), "/tmp/handbook.pdf", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	want := []ParsedChunk{{
		Content: "# Handbook\n\nTravel policy details.",
		Locator: ChunkLocator{
			FirstPage:   uint32Pointer(3),
			LastPage:    uint32Pointer(4),
			HeadingPath: []string{"Handbook", "Travel"},
			ByteStart:   0,
			ByteEnd:     35,
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse() = %#v, want %#v", got, want)
	}
	if runner.binary != "/managed/xberg" {
		t.Fatalf("binary = %q", runner.binary)
	}
	assertXbergArgPair(t, runner.args, "--format", "json")
	assertXbergArgPair(t, runner.args, "--mime-type", "application/pdf")
	assertXbergArg(t, runner.args, "--no-config-discovery")
	assertXbergArgPair(t, runner.args, "--chunk", "true")
	assertXbergArgPair(t, runner.args, "--no-cache", "true")
	assertXbergArgPair(t, runner.args, "--disable-ocr", "true")
	assertXbergArgPair(t, runner.args, "--content-format", "markdown")
	assertXbergArgPair(t, runner.args, "--extract-pages", "true")

	config := xbergInlineConfig(t, runner.args)
	chunking, ok := config["chunking"].(map[string]any)
	if !ok {
		t.Fatalf("chunking config = %#v", config["chunking"])
	}
	if got := int(chunking["max_chars"].(float64)); got != DefaultChunkSize {
		t.Fatalf("max_chars = %d", got)
	}
	if got := int(chunking["max_overlap"].(float64)); got != DefaultChunkOverlap {
		t.Fatalf("max_overlap = %d", got)
	}
	if got := chunking["chunker_type"]; got != "markdown" {
		t.Fatalf("chunker_type = %#v", got)
	}
}

func TestXbergParserAvailability(t *testing.T) {
	t.Parallel()

	parser, err := NewXbergParser(DefaultXbergParserConfig(filepath.Join(t.TempDir(), "missing-xberg")))
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Available(t.Context()); err == nil {
		t.Fatal("Available() succeeded for a missing managed binary")
	}

	binary := filepath.Join(t.TempDir(), "xberg")
	if err := os.WriteFile(binary, []byte("managed binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	parser, err = newXbergParserWithRunner(
		DefaultXbergParserConfig(binary),
		&recordingXbergRunner{probeStdout: []byte("xberg " + XbergVersion + "\n")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Available(t.Context()); err != nil {
		t.Fatalf("Available() error = %v", err)
	}
}

func TestXbergParserAvailabilityMatchesManagedManifest(t *testing.T) {
	t.Parallel()

	manifest, err := manifestplugins.LoadBuiltin()
	if err != nil {
		t.Fatalf("load builtin manifest: %v", err)
	}
	var managedVersion string
	for _, plugin := range manifest.Plugins {
		for _, binary := range plugin.Binaries {
			if binary.Name == "xberg" {
				managedVersion = binary.Version
				break
			}
		}
	}
	if managedVersion == "" {
		t.Fatal("managed Xberg binary is missing from the builtin manifest")
	}

	binary := filepath.Join(t.TempDir(), "xberg")
	if err := os.WriteFile(binary, []byte("managed"), 0o700); err != nil {
		t.Fatal(err)
	}
	parser, err := newXbergParserWithRunner(
		DefaultXbergParserConfig(binary),
		&recordingXbergRunner{probeStdout: []byte("xberg " + managedVersion + "\n")},
	)
	if err != nil {
		t.Fatalf("new parser: %v", err)
	}
	if err := parser.Available(t.Context()); err != nil {
		t.Fatalf("managed manifest version %q must satisfy Available(): %v", managedVersion, err)
	}
}

func TestXbergParserAvailabilityRejectsBrokenRuntime(t *testing.T) {
	t.Parallel()

	binary := filepath.Join(t.TempDir(), "xberg")
	if err := os.WriteFile(binary, []byte("managed binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	parser, err := newXbergParserWithRunner(
		DefaultXbergParserConfig(binary),
		&recordingXbergRunner{
			probeStderr: []byte("undefined symbol: heif_color_conversion_options_ext_free"),
			probeErr:    errors.New("exit 127"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	err = parser.Available(t.Context())
	if err == nil || !strings.Contains(err.Error(), "undefined symbol") {
		t.Fatalf("Available() error = %v", err)
	}
}

func TestXbergParserAvailabilityRespectsContext(t *testing.T) {
	t.Parallel()

	binary := filepath.Join(t.TempDir(), "xberg")
	if err := os.WriteFile(binary, []byte("managed binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	parser, err := newXbergParserWithRunner(
		DefaultXbergParserConfig(binary),
		probeBlockingXbergRunner{},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := parser.Available(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Available() error = %v, want deadline exceeded", err)
	}
}

func TestXbergProcessEnvironmentOverridesInheritedValue(t *testing.T) {
	t.Setenv("MISE_GLOBAL_CONFIG_FILE", "/host/config.toml")

	environment := xbergProcessEnvironment(map[string]string{
		"MISE_GLOBAL_CONFIG_FILE": "/stella/config.toml",
	})
	var values []string
	for _, entry := range environment {
		if strings.HasPrefix(entry, "MISE_GLOBAL_CONFIG_FILE=") {
			values = append(values, entry)
		}
	}
	if !reflect.DeepEqual(values, []string{"MISE_GLOBAL_CONFIG_FILE=/stella/config.toml"}) {
		t.Fatalf("MISE_GLOBAL_CONFIG_FILE entries = %#v", values)
	}
}

func TestXbergParserRejectsInvalidResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		config  XbergParserConfig
		wantErr error
	}{
		{
			name:    "invalid json",
			payload: "{",
			config:  DefaultXbergParserConfig("xberg"),
			wantErr: ErrInvalidXbergJSON,
		},
		{
			name:    "no text",
			payload: `{"result":{"content":"  --- ","chunks":[]}}`,
			config:  DefaultXbergParserConfig("xberg"),
			wantErr: ErrNoExtractedText,
		},
		{
			name:    "content limit",
			payload: validXbergPayload("more than ten bytes"),
			config: XbergParserConfig{
				Binary:          "xberg",
				MaxContentBytes: 10,
			},
			wantErr: ErrParseResultLimit,
		},
		{
			name:    "empty chunk",
			payload: xbergPayload([]string{"content", " --- "}),
			config:  DefaultXbergParserConfig("xberg"),
			wantErr: ErrInvalidXbergJSON,
		},
		{
			name:    "chunk count limit",
			payload: xbergPayload([]string{"first", "second"}),
			config: XbergParserConfig{
				Binary:    "xberg",
				MaxChunks: 1,
			},
			wantErr: ErrParseResultLimit,
		},
		{
			name:    "combined chunk byte limit",
			payload: xbergPayload([]string{"first", "second"}),
			config: XbergParserConfig{
				Binary:        "xberg",
				MaxChunkBytes: 8,
			},
			wantErr: ErrParseResultLimit,
		},
		{
			name:    "trailing json",
			payload: validXbergPayload("content") + `{}`,
			config:  DefaultXbergParserConfig("xberg"),
			wantErr: ErrInvalidXbergJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parser, err := newXbergParserWithRunner(tt.config, &recordingXbergRunner{stdout: []byte(tt.payload)})
			if err != nil {
				t.Fatal(err)
			}
			_, err = parser.Parse(context.Background(), "/tmp/file.txt", "text/plain")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Parse() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestXbergParserBoundsStdout(t *testing.T) {
	t.Parallel()

	config := DefaultXbergParserConfig("xberg")
	config.MaxStdoutBytes = 8
	parser, err := newXbergParserWithRunner(config, &recordingXbergRunner{stdout: []byte("0123456789")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = parser.Parse(context.Background(), "/tmp/file.txt", "text/plain")
	if !errors.Is(err, ErrParseOutputLimit) {
		t.Fatalf("Parse() error = %v, want output limit", err)
	}
}

func TestXbergParserBoundsExecutionTime(t *testing.T) {
	t.Parallel()

	config := DefaultXbergParserConfig("xberg")
	config.Timeout = 10 * time.Millisecond
	parser, err := newXbergParserWithRunner(config, contextBlockingXbergRunner{})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = parser.Parse(context.Background(), "/tmp/file.txt", "text/plain")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Parse() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Parse() returned after %s, expected a bounded timeout", elapsed)
	}
}

func TestXbergParserDistinguishesProcessFailure(t *testing.T) {
	t.Parallel()

	parser, err := newXbergParserWithRunner(
		DefaultXbergParserConfig("xberg"),
		&recordingXbergRunner{
			stderr: []byte(" temporary\n failure at /tmp/private/file.txt\x00 "),
			err:    errors.New("exit 1"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = parser.Parse(context.Background(), "/tmp/file.txt", "text/plain")
	if err == nil || !strings.Contains(err.Error(), "temporary failure") {
		t.Fatalf("Parse() error = %v", err)
	}
	if strings.ContainsRune(err.Error(), '\x00') {
		t.Fatalf("Parse() leaked control characters: %q", err)
	}
	if strings.Contains(err.Error(), "/tmp/private") {
		t.Fatalf("Parse() leaked the temporary path: %q", err)
	}
}

func TestXbergParserBoundsFailureDiagnostics(t *testing.T) {
	t.Parallel()

	config := DefaultXbergParserConfig("xberg")
	config.MaxStderrBytes = 8
	parser, err := newXbergParserWithRunner(
		config,
		&recordingXbergRunner{
			stderr: []byte("12345678must-not-appear"),
			err:    errors.New("exit 1"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = parser.Parse(context.Background(), "/tmp/file.txt", "text/plain")
	if err == nil || !strings.Contains(err.Error(), "12345678") {
		t.Fatalf("Parse() error = %v", err)
	}
	if strings.Contains(err.Error(), "must-not-appear") {
		t.Fatalf("Parse() exceeded the stderr diagnostic bound: %v", err)
	}
}

func TestXbergParserIntegrationFixtures(t *testing.T) {
	binary := os.Getenv("STELLA_XBERG_TEST_BINARY")
	if binary == "" {
		t.Skip("set STELLA_XBERG_TEST_BINARY to run the Xberg integration fixtures")
	}

	tempDir := t.TempDir()
	fixtures := []struct {
		name      string
		mediaType string
		phrase    string
		write     func(string) error
	}{
		{
			name:      "handbook.pdf",
			mediaType: "application/pdf",
			phrase:    "PDF travel policy",
			write: func(path string) error {
				return os.WriteFile(path, simplePDF("PDF travel policy"), 0o600)
			},
		},
		{
			name:      "handbook.docx",
			mediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			phrase:    "DOCX travel policy",
			write: func(path string) error {
				return writeSimpleDOCX(path, "Travel Handbook", "DOCX travel policy")
			},
		},
		{
			name:      "handbook.md",
			mediaType: "text/markdown",
			phrase:    "Markdown travel policy",
			write: func(path string) error {
				return os.WriteFile(path, []byte("# Travel Handbook\n\nMarkdown travel policy.\n"), 0o600)
			},
		},
		{
			name:      "handbook.txt",
			mediaType: "text/plain",
			phrase:    "TXT travel policy",
			write: func(path string) error {
				return os.WriteFile(path, []byte("TXT travel policy.\n"), 0o600)
			},
		},
	}

	config := DefaultXbergParserConfig(binary)
	parser, err := NewXbergParser(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Available(t.Context()); err != nil {
		t.Fatalf("Xberg integration runtime is unavailable: %v", err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join(tempDir, fixture.name)
			if err := fixture.write(path); err != nil {
				t.Fatal(err)
			}
			chunks, err := parser.Parse(context.Background(), path, fixture.mediaType)
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := parser.Parse(context.Background(), path, fixture.mediaType)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(chunks, repeated) {
				t.Fatalf("repeated parsing changed chunk order or locators:\nfirst:  %#v\nsecond: %#v", chunks, repeated)
			}
			var content strings.Builder
			lastByteStart := -1
			for index, chunk := range chunks {
				if chunk.Locator.ByteStart < 0 || chunk.Locator.ByteEnd < chunk.Locator.ByteStart {
					t.Fatalf("chunk %d has invalid locator: %+v", index, chunk.Locator)
				}
				if chunk.Locator.ByteStart < lastByteStart {
					t.Fatalf("chunk %d locator moved backwards: %+v", index, chunk.Locator)
				}
				lastByteStart = chunk.Locator.ByteStart
				content.WriteString(chunk.Content)
			}
			if !strings.Contains(content.String(), fixture.phrase) {
				t.Fatalf("chunks do not contain %q: %q", fixture.phrase, content.String())
			}
		})
	}

	t.Run("malformed.pdf", func(t *testing.T) {
		path := filepath.Join(tempDir, "malformed.pdf")
		if err := os.WriteFile(path, []byte("%PDF-1.4\nmalformed"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := parser.Parse(context.Background(), path, "application/pdf"); err == nil {
			t.Fatal("malformed PDF unexpectedly produced chunks")
		}
	})
}

func validXbergPayload(content string) string {
	return xbergPayload([]string{content})
}

func xbergPayload(contents []string) string {
	chunks := make([]any, 0, len(contents))
	var document strings.Builder
	byteStart := 0
	for index, content := range contents {
		document.WriteString(content)
		chunks = append(chunks, map[string]any{
			"content": content,
			"metadata": map[string]any{
				"byte_start":   byteStart,
				"byte_end":     byteStart + len(content),
				"chunk_index":  index,
				"total_chunks": len(contents),
			},
		})
		byteStart += len(content)
	}
	payload, _ := json.Marshal(map[string]any{
		"result": map[string]any{
			"content": document.String(),
			"chunks":  chunks,
		},
	})
	return string(payload)
}

func xbergInlineConfig(t *testing.T, args []string) map[string]any {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--config-json" {
			continue
		}
		var config map[string]any
		if err := json.Unmarshal([]byte(args[index+1]), &config); err != nil {
			t.Fatal(err)
		}
		return config
	}
	t.Fatal("--config-json not found")
	return nil
}

func assertXbergArgPair(t *testing.T, args []string, key, value string) {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key && args[index+1] == value {
			return
		}
	}
	t.Fatalf("arguments do not contain %q %q: %#v", key, value, args)
}

func assertXbergArg(t *testing.T, args []string, value string) {
	t.Helper()
	if slices.Contains(args, value) {
		return
	}
	t.Fatalf("arguments do not contain %q: %#v", value, args)
}

func uint32Pointer(value uint32) *uint32 {
	return &value
}

func simplePDF(text string) []byte {
	text = strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(text)
	stream := fmt.Sprintf("BT /F1 18 Tf 72 720 Td (%s) Tj ET\n", text)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream),
	}

	var pdf strings.Builder
	pdf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, len(objects))
	for index, object := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xrefOffset := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(
		&pdf,
		"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1,
		xrefOffset,
	)
	return []byte(pdf.String())
}

func writeSimpleDOCX(path, heading, body string) error {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`,
		"word/_rels/document.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`,
		"word/styles.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:style w:type="paragraph" w:styleId="Heading1">
    <w:name w:val="heading 1"/>
  </w:style>
</w:styles>`,
		"word/document.xml": fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>%s</w:t></w:r></w:p>
    <w:p><w:r><w:t>%s</w:t></w:r></w:p>
    <w:sectPr/>
  </w:body>
</w:document>`, heading, body),
	}
	for name, content := range files {
		file, err := writer.Create(name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(file, content); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, archive.Bytes(), 0o600)
}

type recordingXbergRunner struct {
	binary      string
	args        []string
	runCalls    int
	probeCalls  int
	stdout      []byte
	stderr      []byte
	err         error
	probeStdout []byte
	probeStderr []byte
	probeErr    error
}

type contextBlockingXbergRunner struct{}

func (contextBlockingXbergRunner) Run(
	ctx context.Context,
	_ string,
	_ string,
	_ []string,
	_ io.Writer,
	_ io.Writer,
) error {
	<-ctx.Done()
	return ctx.Err()
}

func (contextBlockingXbergRunner) Probe(
	context.Context,
	string,
	io.Writer,
	io.Writer,
) error {
	return nil
}

type probeBlockingXbergRunner struct{}

func (probeBlockingXbergRunner) Run(
	context.Context,
	string,
	string,
	[]string,
	io.Writer,
	io.Writer,
) error {
	return nil
}

func (probeBlockingXbergRunner) Probe(
	ctx context.Context,
	_ string,
	_ io.Writer,
	_ io.Writer,
) error {
	<-ctx.Done()
	return ctx.Err()
}

func (r *recordingXbergRunner) Run(
	_ context.Context,
	binary string,
	_ string,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) error {
	r.runCalls++
	r.binary = binary
	r.args = append([]string(nil), args...)
	if len(r.stdout) > 0 {
		if _, err := stdout.Write(r.stdout); err != nil {
			return err
		}
	}
	if len(r.stderr) > 0 {
		_, _ = stderr.Write(r.stderr)
	}
	return r.err
}

func (r *recordingXbergRunner) Probe(
	_ context.Context,
	binary string,
	stdout io.Writer,
	stderr io.Writer,
) error {
	r.probeCalls++
	r.binary = binary
	if len(r.probeStdout) > 0 {
		if _, err := stdout.Write(r.probeStdout); err != nil {
			return err
		}
	}
	if len(r.probeStderr) > 0 {
		_, _ = stderr.Write(r.probeStderr)
	}
	return r.probeErr
}
