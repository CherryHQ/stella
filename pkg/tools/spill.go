package tools

import (
	"crypto/sha256"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

// InlineResultBytes is the maximum complete external result returned inline.
// Larger content is projected into the active session's temporary filesystem so
// the Agent can page it with bash instead of spending its context window at once.
const InlineResultBytes = 16 * 1024

// SpilledResult is a bounded preview of a larger result and its sandbox-visible
// read-only path. Head and Tail retain the beginning and end of Content; the
// middle remains in Path for on-demand reads.
type SpilledResult struct {
	Path       string
	TotalBytes int
	Head       string
	Tail       string
}

// SpillResult projects content when it exceeds InlineResultBytes. The filename
// is content-addressed and ProjectTempFiles publishes it no-replace, so retries
// can safely reuse the same immutable result without clobbering another file.
func SpillResult(files sandbox.FileAccess, category, filename, content string) (*SpilledResult, error) {
	if len(content) <= InlineResultBytes {
		return nil, nil
	}
	if files == nil {
		return nil, fmt.Errorf("large result cannot be stored: no active sandbox filesystem")
	}

	sum := sha256.Sum256([]byte(content))
	root, err := files.ProjectTempFiles(
		path.Join("stella-web", category, fmt.Sprintf("%x", sum[:12])),
		[]sandbox.ProjectedFile{{Path: filename, Content: []byte(content), Mode: 0o444}},
	)
	if err != nil {
		return nil, fmt.Errorf("store large result: %w", err)
	}
	head, tail := splitPreview(content, InlineResultBytes)
	return &SpilledResult{
		Path:       path.Join(root, filename),
		TotalBytes: len(content),
		Head:       head,
		Tail:       tail,
	}, nil
}

func splitPreview(content string, budget int) (string, string) {
	if len(content) <= budget {
		return content, ""
	}
	headBytes := budget * 3 / 4
	head := utf8Prefix(content, headBytes)
	tail := utf8Suffix(content[len(head):], budget-len(head))
	return strings.TrimRight(head, "\n"), strings.TrimLeft(tail, "\n")
}

func utf8Prefix(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	for limit > 0 && !utf8.RuneStart(content[limit]) {
		limit--
	}
	return content[:limit]
}

func utf8Suffix(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	start := len(content) - limit
	for start < len(content) && !utf8.RuneStart(content[start]) {
		start++
	}
	return content[start:]
}
