package library

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStructuredTableChunksKeepRowsAndRepeatReliableHeader(t *testing.T) {
	t.Parallel()
	header := []string{"Name", "Amount"}
	cells := [][]string{header}
	lines := []string{"| Name | Amount |", "| --- | --- |"}
	for index := 1; index <= 80; index++ {
		cells = append(cells, []string{strings.Repeat("row", 6), "1"})
		lines = append(lines, "| "+strings.Repeat("row", 6)+" | 1 |")
	}
	markdown := strings.Join(lines, "\n") + "\n"
	result := xbergResult{
		Content: markdown,
		Tables:  []xbergTable{{Cells: cells, Columns: header, Markdown: markdown, PageNumber: 1}},
	}
	chunks, err := structuredTableChunks(result, mustFormatSpec(t, MediaTypeCSV).citations)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want multiple batches", len(chunks))
	}
	for index, chunk := range chunks {
		if !strings.HasPrefix(chunk.Content, "| Name | Amount |\n| --- | --- |") {
			t.Fatalf("chunk %d does not repeat the reliable header: %q", index, chunk.Content)
		}
		if utf8.RuneCountInString(chunk.Content) > TextChunkRunes {
			t.Fatalf("chunk %d has %d runes", index, utf8.RuneCountInString(chunk.Content))
		}
		if chunk.Locator.RowStart == nil || chunk.Locator.RowEnd == nil || *chunk.Locator.RowEnd < *chunk.Locator.RowStart {
			t.Fatalf("chunk %d locator = %+v", index, chunk.Locator)
		}
	}
	if *chunks[0].Locator.RowStart != 1 || *chunks[len(chunks)-1].Locator.RowEnd != 81 {
		t.Fatalf("row coverage = %d..%d", *chunks[0].Locator.RowStart, *chunks[len(chunks)-1].Locator.RowEnd)
	}
}

func TestStructuredTableChunksUseSheetPathWithoutInventingHeader(t *testing.T) {
	t.Parallel()
	markdown := "## Sales\n\n| first | 1 |\n| --- | --- |\n| second | 2 |\n"
	result := xbergResult{
		Content: markdown,
		Pages: []xbergPage{{
			PageNumber: 1, SheetName: "Sales",
			Tables: []xbergTable{{
				Cells: [][]string{{"first", "1"}, {"second", "2"}}, Markdown: markdown, PageNumber: 1,
			}},
		}},
	}
	chunks, err := structuredTableChunks(result, mustFormatSpec(t, MediaTypeXLSX).citations)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || strings.Contains(chunks[0].Content, "---") || strings.Join(chunks[0].Locator.HeadingPath, "/") != "Sales" {
		t.Fatalf("chunks = %+v", chunks)
	}
	if chunks[0].Locator.RowStart != nil || chunks[0].Locator.RowEnd != nil {
		t.Fatalf("office spreadsheet invented source rows: %+v", chunks[0].Locator)
	}
}

func TestStructuredTableChunksDropSyntheticEmptyHeader(t *testing.T) {
	t.Parallel()
	markdown := "|  |  |\n| --- | --- |\n| a | b |\n"
	result := xbergResult{
		Content: markdown,
		Tables: []xbergTable{{
			Cells: [][]string{{"a", "b"}}, Markdown: markdown, PageNumber: 1,
		}},
	}
	chunks, err := structuredTableChunks(result, mustFormatSpec(t, MediaTypeCSV).citations)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Content != "| a | b |" {
		t.Fatalf("chunks = %+v", chunks)
	}
	if chunks[0].Locator.RowStart == nil || chunks[0].Locator.RowEnd == nil ||
		*chunks[0].Locator.RowStart != 1 || *chunks[0].Locator.RowEnd != 1 {
		t.Fatalf("locator = %+v", chunks[0].Locator)
	}
}

func TestStructuredTableChunksKeepDashOnlyDataRow(t *testing.T) {
	t.Parallel()
	markdown := "| a | b |\n| --- | --- |\n| --- | --- |\n| x | y |\n"
	result := xbergResult{
		Content: markdown,
		Tables: []xbergTable{{
			Cells:   [][]string{{"a", "b"}, {"---", "---"}, {"x", "y"}},
			Columns: []string{"a", "b"}, Markdown: markdown, PageNumber: 1,
		}},
	}
	chunks, err := structuredTableChunks(result, mustFormatSpec(t, MediaTypeCSV).citations)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || strings.Count(chunks[0].Content, "| --- | --- |") != 2 ||
		!strings.Contains(chunks[0].Content, "| x | y |") {
		t.Fatalf("chunks = %+v", chunks)
	}
	if chunks[0].Locator.RowStart == nil || chunks[0].Locator.RowEnd == nil ||
		*chunks[0].Locator.RowStart != 1 || *chunks[0].Locator.RowEnd != 3 {
		t.Fatalf("locator = %+v", chunks[0].Locator)
	}
}

func TestStructuredTableChunksLocateNormalizedLegacyXLSMarkdown(t *testing.T) {
	t.Parallel()
	content := "## Sheet\n\n| Name | Amount |\n| --- | --- |\n| Alpha | 1 |\n"
	result := xbergResult{
		Content: content,
		Pages: []xbergPage{{
			SheetName: "Sheet",
			Tables: []xbergTable{{
				Cells: [][]string{{"Name", "Amount"}, {"Alpha\n", "1"}},
				// Legacy XLS may retain a raw cell newline here even though the
				// final rendered document normalizes it onto one row.
				Markdown: "## Sheet\n\n| Name | Amount |\n| --- | --- |\n| Alpha\n | 1 |\n",
			}},
		}},
	}
	chunks, err := structuredTableChunks(result, mustFormatSpec(t, MediaTypeXLS).citations)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Content != "| Name | Amount |\n| Alpha | 1 |" {
		t.Fatalf("chunks = %+v", chunks)
	}
	if chunks[0].Locator.RowStart != nil || chunks[0].Locator.RowEnd != nil {
		t.Fatalf("legacy spreadsheet invented source rows: %+v", chunks[0].Locator)
	}
}
