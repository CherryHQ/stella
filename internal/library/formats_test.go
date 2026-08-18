package library

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFormatRegistryMapsEverySupportedExtension(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"a.txt": MediaTypeText, "a.md": MediaTypeMarkdown, "a.markdown": MediaTypeMarkdown,
		"a.pdf": MediaTypePDF, "a.doc": MediaTypeDOC, "a.docx": MediaTypeDOCX,
		"a.odt": MediaTypeODT, "a.rtf": MediaTypeRTF, "a.xls": MediaTypeXLS,
		"a.xlsx": MediaTypeXLSX, "a.ods": MediaTypeODS, "a.csv": MediaTypeCSV,
		"a.tsv": MediaTypeTSV, "a.ppt": MediaTypePPT, "a.pptx": MediaTypePPTX, "a.odp": MediaTypeODP,
		"a.html": MediaTypeHTML, "a.htm": MediaTypeHTML,
		"a.xhtml": MediaTypeXHTML, "a.epub": MediaTypeEPUB, "a.fb2": MediaTypeFB2,
		"a.mdx": MediaTypeMDX, "a.rst": MediaTypeRST, "a.org": MediaTypeORG,
		"a.json": MediaTypeJSON, "a.yaml": MediaTypeYAML, "a.yml": MediaTypeYAML,
		"a.toml": MediaTypeTOML, "a.xml": MediaTypeXML,
	}
	for name, want := range tests {
		_, got, err := validateUploadName(strings.ToUpper(name))
		if err != nil || got != want {
			t.Errorf("validateUploadName(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
	if len(SupportedMediaTypes()) != len(formatSpecs) {
		t.Fatalf("supported media types = %d, want %d", len(SupportedMediaTypes()), len(formatSpecs))
	}
	if slices.Contains(XbergMediaTypes(), MediaTypeText) || slices.Contains(XbergMediaTypes(), MediaTypeMarkdown) {
		t.Fatal("built-in text formats leaked into Xberg routes")
	}
}

func TestFormatRegistryOwnsExtractionAndCitationBehavior(t *testing.T) {
	t.Parallel()
	type behavior struct {
		parser    parserKind
		mode      extractionMode
		citations citationPolicy
	}
	expected := map[string]behavior{
		MediaTypeText:     {parser: parserKindText, mode: extractionModeNarrative},
		MediaTypeMarkdown: {parser: parserKindText, mode: extractionModeNarrative},
		MediaTypePDF:      {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true, pageRange: true}},
		MediaTypeDOC:      {parser: parserKindXberg, mode: extractionModeNarrative},
		MediaTypeDOCX:     {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
		MediaTypeODT:      {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
		MediaTypeRTF:      {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
		MediaTypeXLS:      {parser: parserKindXberg, mode: extractionModeTable, citations: citationPolicy{headingPath: true}},
		MediaTypeXLSX:     {parser: parserKindXberg, mode: extractionModeTable, citations: citationPolicy{headingPath: true}},
		MediaTypeODS:      {parser: parserKindXberg, mode: extractionModeTable, citations: citationPolicy{headingPath: true}},
		MediaTypeCSV:      {parser: parserKindXberg, mode: extractionModeTable, citations: citationPolicy{headingPath: true}},
		MediaTypeTSV:      {parser: parserKindXberg, mode: extractionModeTable, citations: citationPolicy{headingPath: true}},
		MediaTypePPT:      {parser: parserKindXberg, mode: extractionModeNarrative},
		MediaTypePPTX:     {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true, pageRange: true, enforcePageBoundary: true}},
		MediaTypeODP:      {parser: parserKindXberg, mode: extractionModeNarrative},
		MediaTypeHTML:     {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
		MediaTypeXHTML:    {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
		MediaTypeEPUB:     {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
		MediaTypeFB2:      {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
		MediaTypeMDX:      {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
		MediaTypeRST:      {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
		MediaTypeORG:      {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
		MediaTypeJSON:     {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
		MediaTypeYAML:     {parser: parserKindXberg, mode: extractionModeNarrative},
		MediaTypeTOML:     {parser: parserKindXberg, mode: extractionModeNarrative},
		MediaTypeXML:      {parser: parserKindXberg, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
	}
	if len(expected) != len(formatSpecs) {
		t.Fatalf("expected formats = %d, registry formats = %d", len(expected), len(formatSpecs))
	}
	for _, spec := range formatSpecs {
		want, ok := expected[spec.mediaType]
		if !ok {
			t.Errorf("unexpected format %q", spec.mediaType)
			continue
		}
		if spec.validate == nil || spec.parser != want.parser || spec.mode != want.mode || spec.citations != want.citations {
			t.Errorf("format %q behavior = parser %d mode %d citations %+v", spec.mediaType, spec.parser, spec.mode, spec.citations)
		}
	}
}

func TestValidateStructuredTextFormats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mediaType string
		valid     string
		invalid   string
	}{
		{"rtf", MediaTypeRTF, `{\rtf1 hello}`, `plain text`},
		{"csv", MediaTypeCSV, "name,value\nalpha,1\n", "name,\"unterminated\n"},
		{"tsv", MediaTypeTSV, "name\tvalue\nalpha\t1\n", "name\t\x00\n"},
		{"html", MediaTypeHTML, "<html><body>Hello</body></html>", "plain text"},
		{"xhtml", MediaTypeXHTML, `<html xmlns="http://www.w3.org/1999/xhtml"><body/></html>`, `<body/>`},
		{"fb2", MediaTypeFB2, `<FictionBook><body/></FictionBook>`, `<book/>`},
		{"json", MediaTypeJSON, `{"enabled":true}`, `{"enabled":}`},
		{"yaml", MediaTypeYAML, "enabled: true\n", "key: [\n"},
		{"toml", MediaTypeTOML, "enabled = true\n", "enabled =\n"},
		{"xml", MediaTypeXML, `<root><item/></root>`, `<root>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			valid := filepath.Join(t.TempDir(), "valid")
			if err := os.WriteFile(valid, []byte(test.valid), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := validateUploadFile(valid, test.mediaType); err != nil {
				t.Fatalf("valid document rejected: %v", err)
			}
			invalid := filepath.Join(t.TempDir(), "invalid")
			if err := os.WriteFile(invalid, []byte(test.invalid), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := validateUploadFile(invalid, test.mediaType); !errors.Is(err, ErrInvalidFile) {
				t.Fatalf("invalid document error = %v, want ErrInvalidFile", err)
			}
		})
	}
}

func TestValidateOfficeZIPPackagesAndRejectsTraversal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mediaType string
		entries   map[string]string
	}{
		{"xlsx", MediaTypeXLSX, map[string]string{"[Content_Types].xml": "<Types/>", "xl/workbook.xml": "<workbook/>"}},
		{"pptx", MediaTypePPTX, map[string]string{"[Content_Types].xml": "<Types/>", "ppt/presentation.xml": "<presentation/>"}},
		{"odt", MediaTypeODT, map[string]string{"mimetype": "application/vnd.oasis.opendocument.text", "content.xml": "<document/>"}},
		{"ods", MediaTypeODS, map[string]string{"mimetype": "application/vnd.oasis.opendocument.spreadsheet", "content.xml": "<document/>"}},
		{"odp", MediaTypeODP, map[string]string{"mimetype": "application/vnd.oasis.opendocument.presentation", "content.xml": "<document/>"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			file := writeZIPFixture(t, test.entries)
			if err := validateUploadFile(file, test.mediaType); err != nil {
				t.Fatal(err)
			}
		})
	}
	traversal := writeZIPFixture(t, map[string]string{
		"[Content_Types].xml": "<Types/>", "word/document.xml": "<document/>", "../escape": "bad",
	})
	if err := validateUploadFile(traversal, MediaTypeDOCX); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("traversal error = %v, want ErrInvalidFile", err)
	}
	renamedXLSX := writeZIPFixture(t, map[string]string{
		"[Content_Types].xml": "<Types/>", "xl/workbook.xml": "<workbook/>",
	})
	if err := validateUploadFile(renamedXLSX, MediaTypeDOCX); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("renamed XLSX error = %v, want ErrInvalidFile", err)
	}
}

func TestValidateEPUBRequiresDeclaredRootfile(t *testing.T) {
	t.Parallel()
	container := `<?xml version="1.0"?><container><rootfiles><rootfile full-path="OPS/package.opf"/></rootfiles></container>`
	valid := writeZIPFixture(t, map[string]string{
		"mimetype": "application/epub+zip", "META-INF/container.xml": container, "OPS/package.opf": "<package/>",
	})
	if err := validateUploadFile(valid, MediaTypeEPUB); err != nil {
		t.Fatal(err)
	}
	missing := writeZIPFixture(t, map[string]string{
		"mimetype": "application/epub+zip", "META-INF/container.xml": container,
	})
	if err := validateUploadFile(missing, MediaTypeEPUB); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("missing rootfile error = %v, want ErrInvalidFile", err)
	}
}

func TestValidateLegacyOfficeRequiresOnlySharedCFBSignature(t *testing.T) {
	t.Parallel()
	valid := writeBytesFixture(t, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1})
	for _, mediaType := range []string{MediaTypeDOC, MediaTypeXLS, MediaTypePPT} {
		if err := validateUploadFile(valid, mediaType); err != nil {
			t.Fatalf("%s valid CFB signature: %v", mediaType, err)
		}
		for name, content := range map[string][]byte{
			"empty": nil,
			"ZIP":   []byte("PK\x03\x04not CFB"),
			"PDF":   []byte("%PDF-not CFB"),
			"wrong": []byte("not CFB!"),
		} {
			path := writeBytesFixture(t, content)
			if err := validateUploadFile(path, mediaType); !errors.Is(err, ErrInvalidFile) {
				t.Fatalf("%s %s error = %v, want ErrInvalidFile", mediaType, name, err)
			}
		}
	}
}

func writeZIPFixture(t *testing.T, entries map[string]string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "fixture.zip")
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for entryName, content := range entries {
		entry, err := writer.Create(entryName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

func writeBytesFixture(t *testing.T, content []byte) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(name, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func mustFormatSpec(t *testing.T, mediaType string) formatSpec {
	t.Helper()
	spec, ok := formatByMediaType(mediaType)
	if !ok {
		t.Fatalf("format %q is not registered", mediaType)
	}
	return spec
}
