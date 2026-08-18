package library

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/resources/binaries"
)

func TestPackagedXbergTableAdmission(t *testing.T) {
	parser := packagedXbergParser(t)

	tests := []struct {
		name          string
		file          string
		content       string
		mediaType     string
		wantContent   []string
		rejectContent []string
		wantDashRows  int
		wantSheet     string
	}{
		{
			name: "single record CSV", file: "single_record.csv", mediaType: MediaTypeCSV,
			wantContent: []string{"| a | b |"}, rejectContent: []string{"|  |  |"},
		},
		{
			name: "empty header CSV", file: "empty_header.csv", mediaType: MediaTypeCSV,
			wantContent: []string{"| 1 | 2 |"}, rejectContent: []string{"|  |  |"},
		},
		{
			name: "empty record CSV", file: "empty_record.csv", mediaType: MediaTypeCSV,
			wantContent: []string{"| a | b |", "| x | y |"}, rejectContent: []string{"|  |  |"},
		},
		{
			name: "empty record TSV", file: "empty_record.tsv", content: "a\tb\n\t\nx\ty\n", mediaType: MediaTypeTSV,
			wantContent: []string{"| a | b |", "| x | y |"}, rejectContent: []string{"|  |  |"},
		},
		{
			name: "dash data CSV", file: "dash_data.csv", mediaType: MediaTypeCSV,
			wantContent: []string{"| a | b |", "| x | y |"}, wantDashRows: 2,
		},
		{
			name: "dash data XLSX", file: "dash_data.xlsx", mediaType: MediaTypeXLSX,
			wantContent: []string{"| a | b |", "| x | y |"}, wantDashRows: 1, wantSheet: "Data",
		},
		{
			name: "dash data ODS", file: "dash_data.ods", mediaType: MediaTypeODS,
			wantContent: []string{"| a | b |", "| x | y |"}, wantDashRows: 1, wantSheet: "Data",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join("testdata", "xberg_tables", test.file)
			if test.content != "" {
				path = filepath.Join(t.TempDir(), test.file)
				if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := validateUploadFile(path, test.mediaType); err != nil {
				t.Fatalf("validate fixture: %v", err)
			}
			chunks, err := parseWithCurrentProfile(t.Context(), parser, path, test.mediaType)
			if err != nil {
				t.Fatalf("parse fixture with packaged Xberg: %v", err)
			}
			contents := make([]string, len(chunks))
			for index := range chunks {
				contents[index] = chunks[index].Content
			}
			content := strings.Join(contents, "\n")
			for _, want := range test.wantContent {
				if !strings.Contains(content, want) {
					t.Fatalf("content %q does not contain %q", content, want)
				}
			}
			for _, reject := range test.rejectContent {
				if strings.Contains(content, reject) {
					t.Fatalf("content %q contains synthetic row %q", content, reject)
				}
			}
			if test.wantDashRows > 0 && strings.Count(content, "| --- | --- |") != test.wantDashRows {
				t.Fatalf("content %q has %d dash rows, want %d", content, strings.Count(content, "| --- | --- |"), test.wantDashRows)
			}
			first := chunks[0].Locator
			if test.wantSheet != "" && !slices.Contains(first.HeadingPath, test.wantSheet) {
				t.Fatalf("heading path = %v, want sheet %q", first.HeadingPath, test.wantSheet)
			}
		})
	}
}

func TestPackagedXbergDeepCorruptionRemainsRetryable(t *testing.T) {
	parser := packagedXbergParser(t)
	path := writeZIPFixture(t, map[string]string{
		"[Content_Types].xml": "not XML",
		"xl/workbook.xml":     "not XML",
	})
	if err := validateUploadFile(path, MediaTypeXLSX); err != nil {
		t.Fatalf("bounded container validation should admit the deep-corruption fixture: %v", err)
	}
	_, err := parseWithCurrentProfile(t.Context(), parser, path, MediaTypeXLSX)
	if err == nil || errors.Is(err, ErrInvalidParserData) {
		t.Fatalf("deep-corruption error = %v, want retryable Xberg process error", err)
	}
}

func TestPackagedXbergLegacyOfficeCanonicalAndCrossFamilySuffixes(t *testing.T) {
	parser := packagedXbergParser(t)
	formats := []struct {
		file      string
		mediaType string
	}{
		{file: "test.doc", mediaType: MediaTypeDOC},
		{file: "test.xls", mediaType: MediaTypeXLS},
		{file: "test.ppt", mediaType: MediaTypePPT},
	}
	for _, source := range formats {
		t.Run(source.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "xberg_legacy", source.file))
			if err != nil {
				t.Fatal(err)
			}
			for _, target := range formats {
				if err := validateUploadFile(filepath.Join("testdata", "xberg_legacy", source.file), target.mediaType); err != nil {
					t.Fatalf("shared CFB admission as %s: %v", target.mediaType, err)
				}
				suffix, err := canonicalExtension(target.mediaType)
				if err != nil {
					t.Fatal(err)
				}
				staged := filepath.Join(t.TempDir(), "source"+suffix)
				if err := os.WriteFile(staged, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				chunks, parseErr := parseWithCurrentProfile(t.Context(), parser, staged, target.mediaType)
				if target.mediaType == source.mediaType {
					if parseErr != nil || len(chunks) == 0 {
						t.Fatalf("canonical %s parse = %d chunks, %v", target.mediaType, len(chunks), parseErr)
					}
					continue
				}
				if parseErr == nil {
					t.Fatalf("%s bytes parsed under cross-family suffix %s", source.mediaType, suffix)
				}
			}
		})
	}
}

func packagedXbergParser(t *testing.T) *XbergCLIParser {
	t.Helper()
	if !slices.Contains(binaries.ToolNames(), "xberg") {
		if os.Getenv("STELLA_TEST_REQUIRE_PACKAGED_XBERG") == "1" {
			t.Fatal("packaged Xberg is required but was not generated for this test binary")
		}
		t.Skip("packaged Xberg is unavailable; run mise run generate first")
	}
	stellaHome := t.TempDir()
	if err := binaries.EnsureTools(stellaHome); err != nil {
		t.Fatal(err)
	}
	parser, err := NewXbergCLIParser(t.Context(), binaries.ToolPath(stellaHome, "xberg"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return parser
}
