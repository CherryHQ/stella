package library

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var xbergSlideMarkerPattern = regexp.MustCompile(`<!-- STELLA_LIBRARY_SLIDE ([1-9][0-9]*) -->`)

// enforcePresentationPageBoundaries removes Stella's private Xberg markers and
// splits any chunk that spans slides. The marker is injected by the pinned
// parser profile and never enters stored chunks, BM25, or model context.
func enforcePresentationPageBoundaries(chunks []ParsedChunk) ([]ParsedChunk, error) {
	result := make([]ParsedChunk, 0, len(chunks)+2)
	for chunkIndex, chunk := range chunks {
		if chunk.Locator.FirstPage == nil || chunk.Locator.LastPage == nil {
			return nil, fmt.Errorf("%w: Xberg presentation chunk %d has no slide range", ErrInvalidParserData, chunkIndex)
		}
		matches := xbergSlideMarkerPattern.FindAllStringSubmatchIndex(chunk.Content, -1)
		if len(matches) == 0 {
			if *chunk.Locator.FirstPage != *chunk.Locator.LastPage {
				return nil, fmt.Errorf("%w: Xberg cross-slide chunk %d has no slide marker", ErrInvalidParserData, chunkIndex)
			}
			result = append(result, chunk)
			continue
		}

		currentPage := *chunk.Locator.FirstPage
		cursor := 0
		pieces := 0
		emit := func(start, end int) {
			raw := chunk.Content[start:end]
			leftTrimmed := len(raw) - len(strings.TrimLeft(raw, " \t\r\n"))
			rightTrimmed := len(raw) - len(strings.TrimRight(raw, " \t\r\n"))
			start += leftTrimmed
			end -= rightTrimmed
			if start >= end {
				return
			}
			headingPath := append([]string(nil), chunk.Locator.HeadingPath...)
			if *chunk.Locator.FirstPage != *chunk.Locator.LastPage {
				// One Xberg heading_context covers the original cross-slide chunk;
				// attributing it to every split piece would create false citations.
				headingPath = nil
			}
			page := currentPage
			result = append(result, ParsedChunk{
				Content: chunk.Content[start:end],
				Locator: ChunkLocator{
					FirstPage: &page, LastPage: &page,
					HeadingPath: headingPath,
					ByteStart:   chunk.Locator.ByteStart + start,
					ByteEnd:     chunk.Locator.ByteStart + end,
				},
			})
			pieces++
		}
		for _, match := range matches {
			emit(cursor, match[0])
			pageValue, err := strconv.ParseUint(chunk.Content[match[2]:match[3]], 10, 32)
			if err != nil {
				return nil, fmt.Errorf("%w: Xberg slide marker is invalid", ErrInvalidParserData)
			}
			page := uint32(pageValue)
			if page < currentPage || page > *chunk.Locator.LastPage {
				return nil, fmt.Errorf("%w: Xberg slide marker is outside the chunk range", ErrInvalidParserData)
			}
			currentPage = page
			cursor = match[1]
		}
		emit(cursor, len(chunk.Content))
		if *chunk.Locator.FirstPage != *chunk.Locator.LastPage && currentPage != *chunk.Locator.LastPage {
			return nil, fmt.Errorf("%w: Xberg cross-slide chunk %d is missing its final marker", ErrInvalidParserData, chunkIndex)
		}
		if pieces == 0 {
			return nil, fmt.Errorf("%w: Xberg presentation chunk %d contains no text", ErrInvalidParserData, chunkIndex)
		}
	}
	if len(result) == 0 {
		return nil, ErrNoExtractedText
	}
	return result, nil
}
