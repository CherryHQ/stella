package library

import (
	"archive/zip"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	MediaTypePDF      = "application/pdf"
	MediaTypeDOCX     = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	MediaTypeMarkdown = "text/markdown"
	MediaTypeText     = "text/plain"

	// DOCX is a ZIP container. Bound its declared expansion before Xberg opens
	// it; the parser process limits remain the second line of defense when ZIP
	// metadata is malformed or a PDF contains expensive compressed objects.
	maxDOCXEntries           = 4096
	maxDOCXEntryBytes        = 64 << 20
	maxDOCXUncompressedBytes = 256 << 20
	maxDOCXCompressionRatio  = 200
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
	case ".pdf":
		mediaType = MediaTypePDF
	case ".docx":
		mediaType = MediaTypeDOCX
	case ".md", ".markdown":
		mediaType = MediaTypeMarkdown
	case ".txt":
		mediaType = MediaTypeText
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
	case MediaTypePDF:
		file, err := os.Open(filePath)
		if err != nil {
			return fmt.Errorf("open PDF upload spool: %w", err)
		}
		defer func() { _ = file.Close() }()
		sample := make([]byte, 1024)
		n, readErr := io.ReadFull(file, sample)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return fmt.Errorf("read PDF signature: %w", readErr)
		}
		if !bytes.Contains(sample[:n], []byte("%PDF-")) {
			return fmt.Errorf("%w: PDF signature is missing", ErrInvalidFile)
		}
		return nil
	case MediaTypeDOCX:
		archive, err := zip.OpenReader(filePath)
		if err != nil {
			return fmt.Errorf("%w: DOCX container is invalid", ErrInvalidFile)
		}
		defer func() { _ = archive.Close() }()
		return validateDOCXArchive(archive.File)
	case MediaTypeMarkdown, MediaTypeText:
		return validateUTF8File(filePath)
	default:
		return fmt.Errorf("%w: media type %q", ErrUnsupportedFileType, mediaType)
	}
}

func validateDOCXArchive(files []*zip.File) error {
	if len(files) > maxDOCXEntries {
		return fmt.Errorf("%w: DOCX container has too many entries", ErrInvalidFile)
	}

	var totalUncompressed uint64
	var hasContentTypes, hasDocument bool
	for _, file := range files {
		uncompressed := file.UncompressedSize64
		if uncompressed > maxDOCXEntryBytes {
			return fmt.Errorf("%w: DOCX entry %q is too large", ErrInvalidFile, file.Name)
		}
		if uncompressed > maxDOCXUncompressedBytes-totalUncompressed {
			return fmt.Errorf("%w: DOCX expanded content is too large", ErrInvalidFile)
		}
		totalUncompressed += uncompressed

		if uncompressed > 0 {
			compressed := file.CompressedSize64
			ratio := uint64(maxDOCXCompressionRatio)
			if compressed == 0 ||
				uncompressed/compressed > ratio ||
				uncompressed/compressed == ratio && uncompressed%compressed != 0 {
				return fmt.Errorf("%w: DOCX entry %q has an unsafe compression ratio", ErrInvalidFile, file.Name)
			}
		}
		switch file.Name {
		case "[Content_Types].xml":
			hasContentTypes = true
		case "word/document.xml":
			hasDocument = true
		}
	}
	if !hasContentTypes || !hasDocument {
		return fmt.Errorf("%w: DOCX container is incomplete", ErrInvalidFile)
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
