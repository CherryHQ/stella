package library

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	MediaTypeMarkdown = "text/markdown"
	MediaTypeText     = "text/plain"
)

// validateUploadName determines the canonical media type without trusting a
// client-provided Content-Type. It runs before the request body is consumed.
func validateUploadName(fileName string) (string, string, error) {
	safeName := safeFileName(fileName)
	if safeName == "" || safeName == "." {
		return "", "", fmt.Errorf("%w: file name is empty", ErrInvalidFile)
	}

	var mediaType string
	switch strings.ToLower(path.Ext(safeName)) {
	case ".md", ".markdown":
		mediaType = MediaTypeMarkdown
	case ".txt":
		mediaType = MediaTypeText
	default:
		return "", "", fmt.Errorf(
			"%w: supported extensions are .md, .markdown, and .txt",
			ErrUnsupportedFileType,
		)
	}
	return safeName, mediaType, nil
}

func validateUploadFile(filePath, mediaType string) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("inspect upload spool: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%w: content is empty", ErrInvalidFile)
	}

	switch mediaType {
	case MediaTypeMarkdown, MediaTypeText:
		return validateUTF8File(filePath)
	default:
		return fmt.Errorf("%w: media type %q", ErrUnsupportedFileType, mediaType)
	}
}

func validateUTF8File(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open text upload spool: %w", err)
	}
	defer func() { _ = file.Close() }()

	reader := bufio.NewReader(file)
	for {
		r, size, err := reader.ReadRune()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read text upload spool: %w", err)
		}
		if r == '\x00' || r == utf8.RuneError && size == 1 {
			return fmt.Errorf("%w: text must be valid UTF-8 without NUL bytes", ErrInvalidFile)
		}
	}
}

func safeFileName(value string) string {
	// Browsers normally send a basename, but older clients can send a Windows
	// path even when the server runs on Linux.
	value = strings.ReplaceAll(value, `\`, "/")
	return strings.TrimSpace(path.Base(value))
}
