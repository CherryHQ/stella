package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/resources/binaries"
)

func TestPackagedXbergTableAdmission(t *testing.T) {
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
	parser, err := NewXbergCLIParser(t.Context(), binaries.ToolPath(stellaHome, "xberg"))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		file          string
		mediaType     string
		wantContent   []string
		rejectContent []string
		wantDashRows  int
		wantRowStart  uint32
		wantRowEnd    uint32
		wantSheet     string
	}{
		{
			name: "single record CSV", file: "single_record.csv", mediaType: MediaTypeCSV,
			wantContent: []string{"| a | b |"}, rejectContent: []string{"|  |  |"},
			wantRowStart: 1, wantRowEnd: 1,
		},
		{
			name: "empty header CSV", file: "empty_header.csv", mediaType: MediaTypeCSV,
			wantContent: []string{"| 1 | 2 |"}, rejectContent: []string{"|  |  |"},
			wantRowStart: 1, wantRowEnd: 1,
		},
		{
			name: "dash data CSV", file: "dash_data.csv", mediaType: MediaTypeCSV,
			wantContent: []string{"| a | b |", "| x | y |"}, wantDashRows: 2,
			wantRowStart: 1, wantRowEnd: 3,
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
			if err := validateUploadFile(path, test.mediaType); err != nil {
				t.Fatalf("validate fixture: %v", err)
			}
			chunks, err := parser.Parse(t.Context(), path, test.mediaType)
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
			first, last := chunks[0].Locator, chunks[len(chunks)-1].Locator
			if test.wantRowStart > 0 {
				if first.RowStart == nil || last.RowEnd == nil || *first.RowStart != test.wantRowStart || *last.RowEnd != test.wantRowEnd {
					t.Fatalf("row range = %v..%v, want %d..%d", first.RowStart, last.RowEnd, test.wantRowStart, test.wantRowEnd)
				}
			} else if first.RowStart != nil || last.RowEnd != nil {
				t.Fatalf("office spreadsheet exposed source row range: %+v .. %+v", first, last)
			}
			if test.wantSheet != "" && !slices.Contains(first.HeadingPath, test.wantSheet) {
				t.Fatalf("heading path = %v, want sheet %q", first.HeadingPath, test.wantSheet)
			}
		})
	}
}
