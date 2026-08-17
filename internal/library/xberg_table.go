package library

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

type locatedTable struct {
	table     xbergTable
	sheetName string
}

type tableLine struct {
	text      string
	byteStart int
	byteEnd   int
	rowNumber uint32
	separator bool
}

// structuredTableChunks uses Xberg's cell matrix and rendered table instead
// of re-parsing the original format. This keeps rows indivisible. It exposes
// logical record ranges only for CSV/TSV because Xberg 1.0.14 does not retain
// original worksheet row coordinates for office spreadsheets.
func structuredTableChunks(result xbergResult, mediaType string) ([]ParsedChunk, error) {
	tables := make([]locatedTable, 0, len(result.Tables))
	for _, page := range result.Pages {
		for _, table := range page.Tables {
			tables = append(tables, locatedTable{table: table, sheetName: strings.TrimSpace(page.SheetName)})
		}
	}
	if len(tables) == 0 {
		for _, table := range result.Tables {
			tables = append(tables, locatedTable{table: table})
		}
	}

	chunks := make([]ParsedChunk, 0, len(tables))
	searchFrom := 0
	for tableIndex, located := range tables {
		if len(located.table.Cells) == 0 || strings.TrimSpace(located.table.Markdown) == "" {
			continue
		}
		tableStart, tableEnd, lines, ok := findRenderedTable(result.Content, searchFrom, len(located.table.Cells))
		if !ok {
			return nil, fmt.Errorf("%w: Xberg table %d is not present in rendered content", ErrInvalidParserData, tableIndex)
		}
		searchFrom = tableEnd
		rows := make([]tableLine, 0, len(lines))
		var separator *tableLine
		for i := range lines {
			line := lines[i]
			if line.separator {
				if separator == nil {
					copyLine := line
					separator = &copyLine
				}
				continue
			}
			line.rowNumber = uint32(len(rows) + 1)
			rows = append(rows, line)
		}
		if len(rows) != len(located.table.Cells) {
			return nil, fmt.Errorf(
				"%w: Xberg table %d has %d structured rows but %d rendered rows",
				ErrInvalidParserData,
				tableIndex,
				len(located.table.Cells),
				len(rows),
			)
		}
		reliableHeader := len(located.table.Columns) > 0 && equalTableRow(located.table.Columns, located.table.Cells[0]) && separator != nil
		// Xberg 1.0.14 does not expose original worksheet row indices and omits
		// leading empty rows. CSV/TSV positions are logical record ranges, but
		// office spreadsheets must not turn rendered row order into a false source
		// citation.
		exposeRowRange := mediaType == MediaTypeCSV || mediaType == MediaTypeTSV
		tableChunks := batchTableRows(rows, separator, reliableHeader, located.sheetName, tableStart, exposeRowRange)
		chunks = append(chunks, tableChunks...)
	}
	return chunks, nil
}

// findRenderedTable locates table rows in Xberg's final canonical Markdown.
// Some legacy XLS cells contain raw newlines, so table.markdown can differ
// from result.content even though the structured cells and rendered row count
// agree. Matching the next complete row group preserves stable byte offsets
// without parsing the original workbook a second time.
func findRenderedTable(content string, searchFrom, expectedRows int) (int, int, []tableLine, bool) {
	for offset := searchFrom; offset < len(content); {
		lineEnd := strings.IndexByte(content[offset:], '\n')
		if lineEnd < 0 {
			lineEnd = len(content)
		} else {
			lineEnd += offset
		}
		text := strings.TrimSpace(content[offset:lineEnd])
		if strings.HasPrefix(text, "|") && strings.HasSuffix(text, "|") {
			groupStart := offset
			groupEnd := lineEnd
			lines := make([]tableLine, 0, expectedRows+2)
			for {
				lineText := strings.TrimSpace(content[offset:lineEnd])
				if !strings.HasPrefix(lineText, "|") || !strings.HasSuffix(lineText, "|") {
					break
				}
				lines = append(lines, tableLine{
					text: lineText, byteStart: offset - groupStart, byteEnd: lineEnd - groupStart,
				})
				groupEnd = lineEnd
				if lineEnd == len(content) {
					offset = len(content)
					break
				}
				offset = lineEnd + 1
				lineEnd = strings.IndexByte(content[offset:], '\n')
				if lineEnd < 0 {
					lineEnd = len(content)
				} else {
					lineEnd += offset
				}
			}
			if normalized, ok := normalizeRenderedTableLines(lines, expectedRows); ok {
				return groupStart, groupEnd, normalized, true
			}
			continue
		}
		if lineEnd == len(content) {
			break
		}
		offset = lineEnd + 1
	}
	return 0, 0, nil, false
}

// normalizeRenderedTableLines identifies the Markdown separator by its fixed
// structural position, never by scanning arbitrary row contents. Xberg 1.0.14
// renders either one separator after the first structured row, or an extra
// empty synthetic header plus separator when it cannot infer a header. The
// latter two lines are not present in table.cells and must not count as data.
func normalizeRenderedTableLines(lines []tableLine, expectedRows int) ([]tableLine, bool) {
	if len(lines) < 2 || !isMarkdownTableSeparator(lines[1].text) {
		return nil, false
	}
	lines[1].separator = true
	switch {
	case len(lines) == expectedRows+1:
		return lines, true
	case len(lines) == expectedRows+2 && isEmptyMarkdownTableRow(lines[0].text):
		return lines[1:], true
	default:
		return nil, false
	}
}

func isEmptyMarkdownTableRow(line string) bool {
	cells := strings.Split(strings.Trim(line, "|"), "|")
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}

func isMarkdownTableSeparator(line string) bool {
	cells := strings.Split(strings.Trim(line, "|"), "|")
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		cell = strings.Trim(cell, ":")
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}

func equalTableRow(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if strings.TrimSpace(left[i]) != strings.TrimSpace(right[i]) {
			return false
		}
	}
	return true
}

func batchTableRows(
	rows []tableLine,
	separator *tableLine,
	reliableHeader bool,
	sheetName string,
	tableStart int,
	exposeRowRange bool,
) []ParsedChunk {
	if len(rows) == 0 {
		return nil
	}
	headingPath := []string(nil)
	if sheetName != "" {
		headingPath = []string{sheetName}
	}
	headerPrefix := ""
	dataStart := 0
	if reliableHeader {
		headerPrefix = rows[0].text + "\n" + separator.text
		dataStart = 1
	}
	if dataStart == len(rows) {
		var rowStart, rowEnd *uint32
		if exposeRowRange {
			start, end := uint32(1), uint32(1)
			rowStart, rowEnd = &start, &end
		}
		return []ParsedChunk{{
			Content: headerPrefix,
			Locator: ChunkLocator{
				RowStart: rowStart, RowEnd: rowEnd,
				HeadingPath: headingPath,
				ByteStart:   tableStart + rows[0].byteStart,
				ByteEnd:     tableStart + rows[0].byteEnd,
			},
		}}
	}

	chunks := make([]ParsedChunk, 0, 4)
	current := make([]tableLine, 0, 16)
	currentRunes := 0
	if reliableHeader {
		currentRunes = utf8.RuneCountInString(headerPrefix)
	}
	emit := func() {
		if len(current) == 0 {
			return
		}
		parts := make([]string, 0, len(current)+1)
		if reliableHeader {
			parts = append(parts, headerPrefix)
		}
		for _, row := range current {
			parts = append(parts, row.text)
		}
		rowStart := current[0].rowNumber
		rowEnd := current[len(current)-1].rowNumber
		byteStart := tableStart + current[0].byteStart
		if reliableHeader && len(chunks) == 0 {
			rowStart = 1
			byteStart = tableStart + rows[0].byteStart
		}
		var publicRowStart, publicRowEnd *uint32
		if exposeRowRange {
			publicRowStart, publicRowEnd = &rowStart, &rowEnd
		}
		chunks = append(chunks, ParsedChunk{
			Content: strings.Join(parts, "\n"),
			Locator: ChunkLocator{
				RowStart: publicRowStart, RowEnd: publicRowEnd,
				HeadingPath: append([]string(nil), headingPath...),
				ByteStart:   byteStart,
				ByteEnd:     tableStart + current[len(current)-1].byteEnd,
			},
		})
		current = current[:0]
		currentRunes = 0
		if reliableHeader {
			currentRunes = utf8.RuneCountInString(headerPrefix)
		}
	}

	for _, row := range rows[dataStart:] {
		rowRunes := utf8.RuneCountInString(row.text)
		separatorRunes := 0
		if currentRunes > 0 {
			separatorRunes = 1
		}
		if len(current) > 0 && currentRunes+separatorRunes+rowRunes > TextChunkRunes {
			emit()
			separatorRunes = 0
			if currentRunes > 0 {
				separatorRunes = 1
			}
		}
		current = append(current, row)
		currentRunes += separatorRunes + rowRunes
	}
	emit()
	return chunks
}
