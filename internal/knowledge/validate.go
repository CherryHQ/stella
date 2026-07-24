package knowledge

import (
	"archive/zip"
	"bytes"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	MediaTypePDF      = "application/pdf"
	MediaTypeDOCX     = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	MediaTypeMarkdown = "text/markdown"
	MediaTypeText     = "text/plain"
)

// Upload is a bounded, validated file ready to enter the creation transaction.
type Upload struct {
	FileName  string
	MediaType string
	Content   []byte
}

// ValidateUpload determines the canonical media type from the file name and
// bytes. A client-provided Content-Type is deliberately not trusted.
func ValidateUpload(fileName string, content []byte) (Upload, error) {
	if len(content) > MaxFileBytes {
		return Upload{}, fmt.Errorf("%w: maximum is %d bytes", ErrFileTooLarge, MaxFileBytes)
	}

	safeName := safeFileName(fileName)
	if safeName == "" || safeName == "." {
		return Upload{}, fmt.Errorf("%w: file name is empty", ErrInvalidFile)
	}

	var mediaType string
	switch strings.ToLower(path.Ext(safeName)) {
	case ".pdf":
		mediaType = MediaTypePDF
		if !validPDF(content) {
			return Upload{}, fmt.Errorf("%w: PDF signature is missing", ErrInvalidFile)
		}
	case ".docx":
		mediaType = MediaTypeDOCX
		if !validDOCX(content) {
			return Upload{}, fmt.Errorf("%w: DOCX container is incomplete", ErrInvalidFile)
		}
	case ".md", ".markdown":
		mediaType = MediaTypeMarkdown
		if !validUTF8Text(content) {
			return Upload{}, fmt.Errorf("%w: Markdown must be UTF-8 text", ErrInvalidFile)
		}
	case ".txt":
		mediaType = MediaTypeText
		if !validUTF8Text(content) {
			return Upload{}, fmt.Errorf("%w: TXT must be UTF-8 text", ErrInvalidFile)
		}
	default:
		return Upload{}, fmt.Errorf("%w: supported extensions are .pdf, .docx, .md, .markdown, and .txt", ErrUnsupportedFileType)
	}

	// Isolate the persisted value from a caller that reuses its upload buffer.
	stored := append([]byte(nil), content...)
	return Upload{FileName: safeName, MediaType: mediaType, Content: stored}, nil
}

func safeFileName(value string) string {
	// Browsers normally send a basename, but older clients can send a Windows
	// path even when the server runs on Linux.
	value = strings.ReplaceAll(value, `\`, "/")
	return strings.TrimSpace(path.Base(value))
}

func validPDF(content []byte) bool {
	sample := content
	if len(sample) > 1024 {
		sample = sample[:1024]
	}
	return bytes.Contains(sample, []byte("%PDF-"))
}

func validDOCX(content []byte) bool {
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return false
	}
	var hasContentTypes, hasDocument bool
	for _, file := range reader.File {
		switch file.Name {
		case "[Content_Types].xml":
			hasContentTypes = true
		case "word/document.xml":
			hasDocument = true
		}
	}
	return hasContentTypes && hasDocument
}

func validUTF8Text(content []byte) bool {
	return utf8.Valid(content) && !bytes.ContainsRune(content, '\x00')
}
