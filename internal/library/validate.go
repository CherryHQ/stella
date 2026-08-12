package library

import (
	"archive/zip"
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
	MediaTypePDF      = "application/pdf"
	MediaTypeDOCX     = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

	maxDOCXEntries                = 10_000
	maxDOCXEntryUncompressedBytes = 32 << 20
	maxDOCXTotalUncompressedBytes = 64 << 20
	maxDOCXCompressionRatio       = 200
	minDOCXRatioCheckBytes        = 100 << 10
)

func isSupportedMediaType(mediaType string) bool {
	switch mediaType {
	case MediaTypeMarkdown, MediaTypeText, MediaTypePDF, MediaTypeDOCX:
		return true
	default:
		return false
	}
}

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
	case ".pdf":
		mediaType = MediaTypePDF
	case ".docx":
		mediaType = MediaTypeDOCX
	default:
		return "", "", fmt.Errorf(
			"%w: supported extensions are .pdf, .docx, .md, .markdown, and .txt",
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
	case MediaTypePDF:
		return validatePDFFile(filePath)
	case MediaTypeDOCX:
		return validateDOCXFile(filePath)
	default:
		return fmt.Errorf("%w: media type %q", ErrUnsupportedFileType, mediaType)
	}
}

func validatePDFFile(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open PDF upload spool: %w", err)
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, 5)
	if _, err := io.ReadFull(file, header); err != nil || string(header) != "%PDF-" {
		return fmt.Errorf("%w: PDF signature is invalid", ErrInvalidFile)
	}
	return nil
}

func validateDOCXFile(filePath string) error {
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		return fmt.Errorf("%w: DOCX ZIP is invalid", ErrInvalidFile)
	}
	defer func() { _ = reader.Close() }()
	if len(reader.File) > maxDOCXEntries {
		return fmt.Errorf("%w: DOCX has too many ZIP entries", ErrInvalidFile)
	}
	foundTypes, foundDocument := false, false
	var declaredTotal uint64
	var expandedTotal uint64
	for _, entry := range reader.File {
		if len(entry.Name) > 4_096 {
			return fmt.Errorf("%w: DOCX ZIP entry name is too long", ErrInvalidFile)
		}
		if entry.UncompressedSize64 > maxDOCXEntryUncompressedBytes {
			return fmt.Errorf("%w: DOCX ZIP entry is too large", ErrInvalidFile)
		}
		if declaredTotal > maxDOCXTotalUncompressedBytes-entry.UncompressedSize64 {
			return fmt.Errorf("%w: DOCX uncompressed content is too large", ErrInvalidFile)
		}
		declaredTotal += entry.UncompressedSize64
		switch entry.Name {
		case "[Content_Types].xml":
			foundTypes = true
		case "word/document.xml":
			foundDocument = true
		}

		content, err := entry.Open()
		if err != nil {
			return fmt.Errorf("%w: open DOCX ZIP entry", ErrInvalidFile)
		}
		expanded, copyErr := io.Copy(io.Discard, io.LimitReader(content, maxDOCXEntryUncompressedBytes+1))
		closeErr := content.Close()
		if copyErr != nil || closeErr != nil {
			return fmt.Errorf("%w: read DOCX ZIP entry", ErrInvalidFile)
		}
		if expanded > maxDOCXEntryUncompressedBytes {
			return fmt.Errorf("%w: DOCX ZIP entry is too large", ErrInvalidFile)
		}
		expandedBytes := uint64(expanded)
		if expandedTotal > maxDOCXTotalUncompressedBytes-expandedBytes {
			return fmt.Errorf("%w: DOCX uncompressed content is too large", ErrInvalidFile)
		}
		expandedTotal += expandedBytes
		if expandedBytes > minDOCXRatioCheckBytes && (entry.CompressedSize64 == 0 ||
			entry.CompressedSize64 < expandedBytes &&
				expandedBytes > entry.CompressedSize64*maxDOCXCompressionRatio) {
			return fmt.Errorf("%w: DOCX ZIP compression ratio is too high", ErrInvalidFile)
		}
	}
	if !foundTypes || !foundDocument {
		return fmt.Errorf("%w: DOCX package is missing required entries", ErrInvalidFile)
	}
	return nil
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
