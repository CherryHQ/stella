package library

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/richardlehane/mscfb"
	"golang.org/x/net/html"
	"gopkg.in/yaml.v3"
)

const (
	maxContainerEntries                = 10_000
	maxContainerEntryUncompressedBytes = 32 << 20
	maxContainerTotalUncompressedBytes = 64 << 20
	maxContainerCompressionRatio       = 200
	minContainerRatioCheckBytes        = 100 << 10

	// Keep the historical names available to focused DOCX boundary tests.
	maxDOCXEntryUncompressedBytes = maxContainerEntryUncompressedBytes
	minDOCXRatioCheckBytes        = minContainerRatioCheckBytes
)

// validateUploadName determines the canonical media type without trusting a
// client-provided Content-Type. It runs before the request body is consumed.
func validateUploadName(fileName string) (string, string, error) {
	safeName := safeFileName(fileName)
	if safeName == "" || safeName == "." {
		return "", "", fmt.Errorf("%w: file name is empty", ErrInvalidFile)
	}
	spec, ok := formatByExtension(path.Ext(safeName))
	if !ok {
		return "", "", fmt.Errorf(
			"%w: supported extensions are %s",
			ErrUnsupportedFileType,
			supportedExtensionMessage(),
		)
	}
	return safeName, spec.mediaType, nil
}

func validateUploadFile(filePath, mediaType string) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("inspect upload spool: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%w: content is empty", ErrInvalidFile)
	}
	spec, ok := formatByMediaType(mediaType)
	if !ok {
		return fmt.Errorf("%w: media type %q", ErrUnsupportedFileType, mediaType)
	}
	return spec.validate(filePath)
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

func validateDOCFile(filePath string) error {
	return validateCFBFile(filePath, "DOC", "WordDocument")
}

func validateXLSFile(filePath string) error {
	return validateCFBFile(filePath, "XLS", "Workbook", "Book")
}

func validatePPTFile(filePath string) error {
	return validateCFBFile(filePath, "PPT", "PowerPoint Document")
}

func validateCFBFile(filePath, format string, requiredStreams ...string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open %s upload spool: %w", format, err)
	}
	defer func() { _ = file.Close() }()
	reader, err := mscfb.New(file)
	if err != nil {
		return fmt.Errorf("%w: %s compound document is invalid", ErrInvalidFile, format)
	}
	if len(reader.File) > maxContainerEntries {
		return fmt.Errorf("%w: %s has too many compound entries", ErrInvalidFile, format)
	}
	found := false
	var total int64
	for _, entry := range reader.File {
		if entry == nil || entry.FileInfo().IsDir() {
			continue
		}
		if entry.Size < 0 || entry.Size > maxContainerEntryUncompressedBytes {
			return fmt.Errorf("%w: %s compound stream is too large", ErrInvalidFile, format)
		}
		total += entry.Size
		if total > maxContainerTotalUncompressedBytes {
			return fmt.Errorf("%w: %s compound content is too large", ErrInvalidFile, format)
		}
		if slicesContainFold(requiredStreams, entry.Name) {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%w: %s compound document is missing its required stream", ErrInvalidFile, format)
	}
	return nil
}

func slicesContainFold(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}

func validateDOCXFile(filePath string) error {
	return validateZIPPackage(filePath, zipPackageRules{
		format: "DOCX", requiredEntries: []string{"[Content_Types].xml", "word/document.xml"},
	})
}

func validateXLSXFile(filePath string) error {
	return validateZIPPackage(filePath, zipPackageRules{
		format: "XLSX", requiredEntries: []string{"[Content_Types].xml", "xl/workbook.xml"},
	})
}

func validatePPTXFile(filePath string) error {
	return validateZIPPackage(filePath, zipPackageRules{
		format: "PPTX", requiredEntries: []string{"[Content_Types].xml", "ppt/presentation.xml"},
	})
}

func validateODPFile(filePath string) error {
	return validateZIPPackage(filePath, zipPackageRules{
		format: "ODP", requiredEntries: []string{"content.xml"},
		mimetype: "application/vnd.oasis.opendocument.presentation",
	})
}

func validateODTFile(filePath string) error {
	return validateZIPPackage(filePath, zipPackageRules{
		format: "ODT", requiredEntries: []string{"content.xml"},
		mimetype: "application/vnd.oasis.opendocument.text",
	})
}

func validateODSFile(filePath string) error {
	return validateZIPPackage(filePath, zipPackageRules{
		format: "ODS", requiredEntries: []string{"content.xml"},
		mimetype: "application/vnd.oasis.opendocument.spreadsheet",
	})
}

type zipPackageRules struct {
	format          string
	requiredEntries []string
	mimetype        string
	inspect         func(map[string][]byte, map[string]struct{}) error
}

func validateZIPPackage(filePath string, rules zipPackageRules) error {
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		return fmt.Errorf("%w: %s ZIP is invalid", ErrInvalidFile, rules.format)
	}
	defer func() { _ = reader.Close() }()
	if len(reader.File) > maxContainerEntries {
		return fmt.Errorf("%w: %s has too many ZIP entries", ErrInvalidFile, rules.format)
	}
	required := make(map[string]bool, len(rules.requiredEntries))
	for _, name := range rules.requiredEntries {
		required[name] = false
	}
	captured := make(map[string][]byte)
	allNames := make(map[string]struct{}, len(reader.File))
	var declaredTotal uint64
	var expandedTotal uint64
	for _, entry := range reader.File {
		if err := validateZIPEntryName(entry.Name); err != nil {
			return fmt.Errorf("%w: %s ZIP %w", ErrInvalidFile, rules.format, err)
		}
		if _, duplicate := allNames[entry.Name]; duplicate {
			return fmt.Errorf("%w: %s ZIP contains duplicate entry %q", ErrInvalidFile, rules.format, entry.Name)
		}
		if entry.Flags&0x1 != 0 {
			return fmt.Errorf("%w: encrypted %s ZIP entries are unsupported", ErrInvalidFile, rules.format)
		}
		if entry.UncompressedSize64 > maxContainerEntryUncompressedBytes {
			return fmt.Errorf("%w: %s ZIP entry is too large", ErrInvalidFile, rules.format)
		}
		if declaredTotal > maxContainerTotalUncompressedBytes-entry.UncompressedSize64 {
			return fmt.Errorf("%w: %s uncompressed content is too large", ErrInvalidFile, rules.format)
		}
		declaredTotal += entry.UncompressedSize64

		content, err := entry.Open()
		if err != nil {
			return fmt.Errorf("%w: open %s ZIP entry", ErrInvalidFile, rules.format)
		}
		data, readErr := io.ReadAll(io.LimitReader(content, maxContainerEntryUncompressedBytes+1))
		closeErr := content.Close()
		if readErr != nil || closeErr != nil {
			return fmt.Errorf("%w: read %s ZIP entry", ErrInvalidFile, rules.format)
		}
		if len(data) > maxContainerEntryUncompressedBytes {
			return fmt.Errorf("%w: %s ZIP entry is too large", ErrInvalidFile, rules.format)
		}
		expandedBytes := uint64(len(data))
		if expandedTotal > maxContainerTotalUncompressedBytes-expandedBytes {
			return fmt.Errorf("%w: %s uncompressed content is too large", ErrInvalidFile, rules.format)
		}
		expandedTotal += expandedBytes
		if expandedBytes > minContainerRatioCheckBytes && (entry.CompressedSize64 == 0 ||
			entry.CompressedSize64 < expandedBytes && expandedBytes > entry.CompressedSize64*maxContainerCompressionRatio) {
			return fmt.Errorf("%w: %s ZIP compression ratio is too high", ErrInvalidFile, rules.format)
		}
		allNames[entry.Name] = struct{}{}
		if _, ok := required[entry.Name]; ok {
			required[entry.Name] = true
			captured[entry.Name] = data
		}
		if entry.Name == "mimetype" || entry.Name == "META-INF/container.xml" {
			captured[entry.Name] = data
		}
	}
	for name, found := range required {
		if !found {
			return fmt.Errorf("%w: %s package is missing required entry %q", ErrInvalidFile, rules.format, name)
		}
	}
	if rules.mimetype != "" && string(captured["mimetype"]) != rules.mimetype {
		return fmt.Errorf("%w: %s package mimetype is invalid", ErrInvalidFile, rules.format)
	}
	if rules.inspect != nil {
		if err := rules.inspect(captured, allNames); err != nil {
			return fmt.Errorf("%w: %s package %w", ErrInvalidFile, rules.format, err)
		}
	}
	return nil
}

func validateZIPEntryName(name string) error {
	if len(name) == 0 || len(name) > 4_096 {
		return fmt.Errorf("entry name is invalid")
	}
	normalized := strings.ReplaceAll(name, `\`, "/")
	cleaned := path.Clean(normalized)
	canonical := cleaned
	if strings.HasSuffix(normalized, "/") {
		canonical += "/"
	}
	if normalized != name || normalized != canonical || cleaned == "." || strings.HasPrefix(normalized, "/") ||
		cleaned == ".." || strings.HasPrefix(cleaned, "../") || len(cleaned) >= 2 && cleaned[1] == ':' {
		return fmt.Errorf("entry path escapes the package")
	}
	return nil
}

func validateEPUBFile(filePath string) error {
	return validateZIPPackage(filePath, zipPackageRules{
		format: "EPUB", requiredEntries: []string{"META-INF/container.xml"}, mimetype: "application/epub+zip",
		inspect: func(entries map[string][]byte, names map[string]struct{}) error {
			var container struct {
				Rootfiles []struct {
					FullPath string `xml:"full-path,attr"`
				} `xml:"rootfiles>rootfile"`
			}
			if err := xml.Unmarshal(entries["META-INF/container.xml"], &container); err != nil || len(container.Rootfiles) == 0 {
				return fmt.Errorf("container.xml is invalid")
			}
			rootfile := path.Clean(strings.ReplaceAll(container.Rootfiles[0].FullPath, `\`, "/"))
			if rootfile == "." || rootfile == ".." || strings.HasPrefix(rootfile, "../") || strings.HasPrefix(rootfile, "/") {
				return fmt.Errorf("rootfile path is invalid")
			}
			if _, ok := names[rootfile]; !ok {
				return fmt.Errorf("rootfile entry is missing")
			}
			return nil
		},
	})
}

func validateRTFFile(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open RTF upload spool: %w", err)
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, 5)
	if _, err := io.ReadFull(file, header); err != nil || !bytes.Equal(header, []byte(`{\rtf`)) {
		return fmt.Errorf("%w: RTF signature is invalid", ErrInvalidFile)
	}
	return nil
}

func validateCSVFile(filePath string) error { return validateDelimitedFile(filePath, ',') }

func validateTSVFile(filePath string) error { return validateDelimitedFile(filePath, '\t') }

func validateDelimitedFile(filePath string, comma rune) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open delimited upload spool: %w", err)
	}
	defer func() { _ = file.Close() }()
	reader := csv.NewReader(file)
	reader.Comma = comma
	reader.FieldsPerRecord = -1
	records := 0
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: delimited text is invalid", ErrInvalidFile)
		}
		records++
		for _, field := range record {
			if !utf8.ValidString(field) || strings.ContainsRune(field, '\x00') {
				return fmt.Errorf("%w: text must be valid UTF-8 without NUL bytes", ErrInvalidFile)
			}
		}
	}
	if records == 0 {
		return fmt.Errorf("%w: delimited text has no records", ErrInvalidFile)
	}
	return nil
}

func validateHTMLFile(filePath string) error {
	data, err := readValidatedText(filePath)
	if err != nil {
		return err
	}
	tokenizer := html.NewTokenizer(bytes.NewReader(data))
	for {
		switch tokenizer.Next() {
		case html.StartTagToken, html.SelfClosingTagToken:
			return nil
		case html.ErrorToken:
			if errors.Is(tokenizer.Err(), io.EOF) {
				return fmt.Errorf("%w: HTML document contains no elements", ErrInvalidFile)
			}
			return fmt.Errorf("%w: HTML document is invalid", ErrInvalidFile)
		}
	}
}

func validateXHTMLFile(filePath string) error { return validateXMLRoot(filePath, "html") }

func validateFB2File(filePath string) error { return validateXMLRoot(filePath, "FictionBook") }

func validateXMLFile(filePath string) error { return validateXMLRoot(filePath, "") }

func validateXMLRoot(filePath, requiredRoot string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open XML upload spool: %w", err)
	}
	defer func() { _ = file.Close() }()
	decoder := xml.NewDecoder(file)
	foundRoot := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: XML document is invalid", ErrInvalidFile)
		}
		if start, ok := token.(xml.StartElement); ok && !foundRoot {
			foundRoot = true
			if requiredRoot != "" && !strings.EqualFold(start.Name.Local, requiredRoot) {
				return fmt.Errorf("%w: XML root element must be %s", ErrInvalidFile, requiredRoot)
			}
		}
	}
	if !foundRoot {
		return fmt.Errorf("%w: XML document has no root element", ErrInvalidFile)
	}
	return nil
}

func validateJSONFile(filePath string) error {
	data, err := readValidatedText(filePath)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: JSON document is invalid", ErrInvalidFile)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: JSON document has trailing content", ErrInvalidFile)
	}
	return nil
}

func validateYAMLFile(filePath string) error {
	data, err := readValidatedText(filePath)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	documents := 0
	for {
		var value yaml.Node
		err := decoder.Decode(&value)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: YAML document is invalid", ErrInvalidFile)
		}
		documents++
	}
	if documents == 0 {
		return fmt.Errorf("%w: YAML document is empty", ErrInvalidFile)
	}
	return nil
}

func validateTOMLFile(filePath string) error {
	data, err := readValidatedText(filePath)
	if err != nil {
		return err
	}
	var value map[string]any
	if err := toml.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("%w: TOML document is invalid", ErrInvalidFile)
	}
	return nil
}

func validateUTF8File(filePath string) error {
	_, err := readValidatedText(filePath)
	return err
}

func readValidatedText(filePath string) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open text upload spool: %w", err)
	}
	defer func() { _ = file.Close() }()
	var content bytes.Buffer
	reader := bufio.NewReader(file)
	for {
		r, size, err := reader.ReadRune()
		if errors.Is(err, io.EOF) {
			return content.Bytes(), nil
		}
		if err != nil {
			return nil, fmt.Errorf("read text upload spool: %w", err)
		}
		if r == '\x00' || r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("%w: text must be valid UTF-8 without NUL bytes", ErrInvalidFile)
		}
		content.WriteRune(r)
	}
}

func safeFileName(value string) string {
	// Browsers normally send a basename, but older clients can send a Windows
	// path even when the server runs on Linux.
	value = strings.ReplaceAll(value, `\`, "/")
	return strings.TrimSpace(path.Base(value))
}
