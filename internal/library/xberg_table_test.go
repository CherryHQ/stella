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
}

func TestStructuredTableChunksDropEmptyRecordsWithoutInventingRowRanges(t *testing.T) {
	t.Parallel()
	markdown := "| a | b |\n| --- | --- |\n| x | y |\n"
	result := xbergResult{
		Content: markdown,
		Tables: []xbergTable{{
			// Xberg omits an empty source record from both cells and rendered
			// Markdown. Stella keeps the useful rows without claiming source rows.
			Cells: [][]string{{"a", "b"}, {"x", "y"}}, Columns: []string{"a", "b"}, Markdown: markdown,
		}},
	}
	chunks, err := structuredTableChunks(result, mustFormatSpec(t, MediaTypeCSV).citations)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || strings.Contains(chunks[0].Content, "|  |  |") {
		t.Fatalf("chunks = %+v", chunks)
	}
}
