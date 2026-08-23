package tools

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultMaxLines = 2000
	defaultMaxBytes = 50 * 1024 // 50KB

	// Deliberate base ceiling: one agent turn gets 64KiB for provider-visible
	// textual tool results. This leaves room above the 50KiB per-call payload cap
	// for its diagnostics, while bounding parallel-call amplification. It is
	// clamped upward only when a truncated share cannot hold its complete marker.
	// Raise it otherwise only when measured task completion gains outweigh the
	// repeated context cost.
	defaultMaxTurnBytes = 64 * 1024
)

type TruncatedBy string

const (
	TruncatedByNone  TruncatedBy = ""
	TruncatedByLines TruncatedBy = "lines"
	TruncatedByBytes TruncatedBy = "bytes"
)

// TruncationResult holds truncated output plus metadata about what happened.
// Content remains the user-facing string, including truncation headers/footers.
type TruncationResult struct {
	Content               string
	Truncated             bool
	TruncatedBy           TruncatedBy
	TotalLines            int
	TotalBytes            int
	OutputLines           int
	OutputBytes           int
	LastLinePartial       bool
	FirstLineExceedsLimit bool
	MaxLines              int
	MaxBytes              int
}

func maxLines() int {
	if v := os.Getenv("STELLA_TOOL_MAX_LINES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxLines
}

func maxBytes() int {
	if v := os.Getenv("STELLA_TOOL_MAX_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxBytes
}

// OutputByteLimit returns the current per-call byte budget for textual tool
// output, including an operator override when configured.
func OutputByteLimit() int {
	return maxBytes()
}

func maxTurnBytes() int {
	if v := os.Getenv("STELLA_TOOL_MAX_TURN_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxTurnBytes
}

// TurnOutputBudgetResult is one textual tool result after the turn-wide budget
// has been applied. Content includes the actionable turn-budget marker when
// truncation was necessary.
type TurnOutputBudgetResult struct {
	Content      string
	Head         string
	Tail         string
	Marker       string
	Truncated    bool
	OmittedBytes int
}

// ApplyTurnOutputBudget distributes the current turn budget across outputs by
// max-min fairness. Small outputs surrender unused capacity to larger outputs;
// ties and remainder bytes are resolved by call order for reproducibility. A
// too-small budget is clamped only enough to keep every required marker whole.
func ApplyTurnOutputBudget(outputs []string) []TurnOutputBudgetResult {
	limits := fairShareBytesWithMarkerFloor(outputs, maxTurnBytes())
	results := make([]TurnOutputBudgetResult, len(outputs))
	for i, output := range outputs {
		results[i] = truncateForTurnBudget(output, limits[i])
	}
	return results
}

func fairShareBytesWithMarkerFloor(outputs []string, budget int) []int {
	for {
		limits := fairShareBytes(outputs, budget)
		deficit := 0
		for i, output := range outputs {
			if len(output) <= limits[i] {
				continue
			}
			markerBytes := len(formatTurnBudgetMarker(len(output)))
			if limits[i] < markerBytes {
				deficit += markerBytes - limits[i]
			}
		}
		if deficit == 0 {
			return limits
		}
		// A mistuned budget is clamped only by the bytes actually missing from
		// truncated results' complete markers. The budget grows monotonically and
		// must stop no later than the point where every output fits untruncated.
		budget += deficit
	}
}

func fairShareBytes(outputs []string, budget int) []int {
	limits := make([]int, len(outputs))
	active := make([]int, len(outputs))
	for i := range outputs {
		active[i] = i
	}

	remaining := budget
	for len(active) > 0 {
		share := remaining / len(active)
		unsatisfied := active[:0]
		for _, i := range active {
			if len(outputs[i]) <= share {
				limits[i] = len(outputs[i])
				remaining -= limits[i]
				continue
			}
			unsatisfied = append(unsatisfied, i)
		}
		if len(unsatisfied) == len(active) {
			for _, i := range unsatisfied {
				limits[i] = share
			}
			for _, i := range unsatisfied[:remaining-share*len(unsatisfied)] {
				limits[i]++
			}
			break
		}
		active = unsatisfied
	}
	return limits
}

func truncateForTurnBudget(output string, limit int) TurnOutputBudgetResult {
	if len(output) <= limit {
		return TurnOutputBudgetResult{Content: output}
	}

	// Reserve marker width from the largest possible omitted count. The actual
	// count cannot have more decimal digits, so its marker is never larger. This
	// avoids a fixed-point loop where UTF-8 boundary rounding and a 9→10 digit
	// transition can oscillate forever.
	reservedMarkerBytes := len(formatTurnBudgetMarker(len(output)))
	// A block-preserving caller may need one implicit FlattenText separator
	// between the marker-bearing head block and a retained tail block.
	available := max(limit-reservedMarkerBytes-1, 0)
	head := truncateStringToBytes(output, (available+1)/2)
	tail := truncateStringToBytesFromEnd(output[len(head):], available-len(head))
	omitted := len(output) - len(head) - len(tail)
	marker := formatTurnBudgetMarker(omitted)

	return TurnOutputBudgetResult{
		Content:      head + marker + tail,
		Head:         head,
		Tail:         tail,
		Marker:       marker,
		Truncated:    true,
		OmittedBytes: omitted,
	}
}

func formatTurnBudgetMarker(omittedBytes int) string {
	return fmt.Sprintf("\n[Tool output truncated: this turn's output budget was exhausted; omitted %d bytes. Use smaller reads or split the work across turns.]\n", omittedBytes)
}

// SplitLines splits text into lines preserving newline suffixes.
// Trims the trailing empty element that strings.SplitAfter produces.
func SplitLines(text string) []string {
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// TruncateHead keeps the first N lines / bytes (whichever limit is hit first).
// Suitable for file reads and search results where the beginning matters most.
// Never returns a partial line.
func TruncateHead(output string) TruncationResult {
	return truncateHead(output, maxLines(), maxBytes())
}

// TruncateTail keeps the last N lines / bytes (whichever limit is hit first).
// Suitable for command output and logs where the end matters most.
// When the last line alone exceeds the byte limit, it keeps a UTF-8-safe tail slice.
func TruncateTail(output string) TruncationResult {
	return truncateTail(output, maxLines(), maxBytes())
}

func truncateHead(output string, lineLimit, byteLimit int) TruncationResult {
	lines := SplitLines(output)
	result := baseResult(output, lines, lineLimit, byteLimit)
	if !needsTruncation(result) {
		result.Content = output
		result.OutputLines = result.TotalLines
		result.OutputBytes = result.TotalBytes
		return result
	}

	if len(lines) > 0 && len(lines[0]) > byteLimit {
		result.Truncated = true
		result.TruncatedBy = TruncatedByBytes
		result.FirstLineExceedsLimit = true
		return finalizeTruncation(result, "first", "")
	}

	kept := make([]string, 0, min(len(lines), lineLimit))
	keptBytes := 0
	truncatedBy := TruncatedByLines
	for i, line := range lines {
		if i >= lineLimit {
			truncatedBy = TruncatedByLines
			break
		}
		if keptBytes+len(line) > byteLimit {
			truncatedBy = TruncatedByBytes
			break
		}
		kept = append(kept, line)
		keptBytes += len(line)
	}

	result.Truncated = true
	result.TruncatedBy = truncatedBy
	result.OutputLines = len(kept)
	result.OutputBytes = keptBytes
	return finalizeTruncation(result, "first", strings.Join(kept, ""))
}

func truncateTail(output string, lineLimit, byteLimit int) TruncationResult {
	lines := SplitLines(output)
	result := baseResult(output, lines, lineLimit, byteLimit)
	if !needsTruncation(result) {
		result.Content = output
		result.OutputLines = result.TotalLines
		result.OutputBytes = result.TotalBytes
		return result
	}

	kept := make([]string, 0, min(len(lines), lineLimit))
	keptBytes := 0
	truncatedBy := TruncatedByLines
	for i := len(lines) - 1; i >= 0; i-- {
		if len(kept) >= lineLimit {
			truncatedBy = TruncatedByLines
			break
		}

		line := lines[i]
		if keptBytes+len(line) > byteLimit {
			truncatedBy = TruncatedByBytes
			if len(kept) == 0 && len(line) > byteLimit {
				partial := truncateStringToBytesFromEnd(line, byteLimit)
				if partial != "" {
					kept = append(kept, partial)
					keptBytes = len(partial)
					result.LastLinePartial = true
				}
			}
			break
		}

		kept = append(kept, line)
		keptBytes += len(line)
	}

	reverseStrings(kept)
	result.Truncated = true
	result.TruncatedBy = truncatedBy
	result.OutputLines = len(kept)
	result.OutputBytes = keptBytes
	return finalizeTruncation(result, "last", strings.Join(kept, ""))
}

func baseResult(output string, lines []string, lineLimit, byteLimit int) TruncationResult {
	return TruncationResult{
		Content:     output,
		TotalLines:  len(lines),
		TotalBytes:  len(output),
		MaxLines:    lineLimit,
		MaxBytes:    byteLimit,
		TruncatedBy: TruncatedByNone,
	}
}

func needsTruncation(result TruncationResult) bool {
	return result.TotalLines > result.MaxLines || result.TotalBytes > result.MaxBytes
}

func finalizeTruncation(result TruncationResult, direction, truncated string) TruncationResult {
	fullPath := saveTempFile(result.Content)
	result.Content = formatTruncated(result, direction, truncated, fullPath)
	return result
}

func saveTempFile(output string) string {
	if output == "" {
		return ""
	}

	tmpFile, err := os.CreateTemp("", "stella-tool-*.txt")
	if err != nil {
		return ""
	}
	defer func() { _ = tmpFile.Close() }()
	if _, err := tmpFile.WriteString(output); err != nil {
		_ = os.Remove(tmpFile.Name())
		return ""
	}
	return tmpFile.Name()
}

func formatTruncated(result TruncationResult, direction, truncated, fullPath string) string {
	header := formatHeader(result, direction)
	footer := formatFooter(fullPath)
	body := truncated
	if result.FirstLineExceedsLimit {
		body = fmt.Sprintf("[First line exceeds byte limit of %s; narrow the selection]", formatSize(result.MaxBytes))
	}

	if direction == "last" {
		if body == "" {
			return header + "\n\n...\n" + footer
		}
		return header + "\n\n...\n" + body + "\n" + footer
	}
	if body == "" {
		return header + "\n\n...\n\n" + footer
	}
	return header + "\n\n" + body + "\n...\n\n" + footer
}

func formatHeader(result TruncationResult, direction string) string {
	shown := fmt.Sprintf("showing %s %d of %d lines", direction, result.OutputLines, result.TotalLines)
	limit := fmt.Sprintf("truncated by %s (limit: %s)", result.TruncatedBy, formatTruncationLimit(result))
	if result.LastLinePartial {
		limit += "; last line shown partially"
	}
	if result.FirstLineExceedsLimit {
		limit += "; first line exceeds byte limit"
	}
	return fmt.Sprintf("[Output truncated — %s, %s, %s total]", shown, limit, formatSize(result.TotalBytes))
}

func formatFooter(fullPath string) string {
	if fullPath == "" {
		return "[Full output could not be saved to a temp file — re-run a narrower command]"
	}
	return fmt.Sprintf("[Full output saved to %s — reachable only when the tool ran on the stella host; otherwise re-run a narrower command]", fullPath)
}

func formatTruncationLimit(result TruncationResult) string {
	if result.TruncatedBy == TruncatedByBytes {
		return formatSize(result.MaxBytes)
	}
	return fmt.Sprintf("%d lines", result.MaxLines)
}

func formatSize(bytes int) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}

func reverseStrings(values []string) {
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
}

// truncateStringToBytes returns the head of str that fits within maxBytes,
// adjusting to a valid UTF-8 rune boundary.
func truncateStringToBytes(str string, maxBytes int) string {
	if len(str) <= maxBytes {
		return str
	}
	end := max(maxBytes, 0)
	for end > 0 && !utf8.RuneStart(str[end]) {
		end--
	}
	return str[:end]
}

// truncateStringToBytesFromEnd returns the tail of str that fits within maxBytes,
// adjusting to a valid UTF-8 rune boundary.
func truncateStringToBytesFromEnd(str string, maxBytes int) string {
	if len(str) <= maxBytes {
		return str
	}
	start := len(str) - maxBytes
	for start < len(str) && !utf8.RuneStart(str[start]) {
		start++
	}
	if start >= len(str) {
		return ""
	}
	return str[start:]
}

// IsBinary reports whether data appears to be binary (non-text) content.
// It samples up to the first 8KB and checks for a high ratio of non-UTF-8
// bytes or null bytes, which are reliable indicators of binary data.
func IsBinary(data string) bool {
	const sampleSize = 8 * 1024
	sample := data
	if len(sample) > sampleSize {
		sample = sample[:sampleSize]
	}
	if len(sample) == 0 {
		return false
	}

	// Null bytes are a strong binary signal.
	if strings.ContainsRune(sample, '\x00') {
		return true
	}

	// Count bytes that are not valid UTF-8 sequences and not common
	// control characters (tab, newline, carriage return).
	nonText := 0
	total := 0
	for i := 0; i < len(sample); {
		r, size := utf8.DecodeRuneInString(sample[i:])
		total++
		if r == utf8.RuneError && size == 1 {
			nonText++
		}
		i += size
	}

	// If more than 10% of runes are invalid, treat as binary.
	return float64(nonText)/float64(total) > 0.1
}
