package library

import (
	"fmt"
	"slices"
	"strings"
)

const (
	MediaTypeMarkdown = "text/markdown"
	MediaTypeText     = "text/plain"
	MediaTypePDF      = "application/pdf"
	MediaTypeDOC      = "application/msword"
	MediaTypeDOCX     = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	MediaTypeODT      = "application/vnd.oasis.opendocument.text"
	MediaTypeRTF      = "application/rtf"
	MediaTypeXLS      = "application/vnd.ms-excel"
	MediaTypeXLSX     = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	MediaTypeODS      = "application/vnd.oasis.opendocument.spreadsheet"
	MediaTypeCSV      = "text/csv"
	MediaTypeTSV      = "text/tab-separated-values"
	MediaTypePPTX     = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	MediaTypeHTML     = "text/html"
	MediaTypeXHTML    = "application/xhtml+xml"
	MediaTypeEPUB     = "application/epub+zip"
	MediaTypeFB2      = "application/x-fictionbook+xml"
	MediaTypeMDX      = "text/mdx"
	MediaTypeRST      = "text/x-rst"
	MediaTypeORG      = "text/org"
	MediaTypeJSON     = "application/json"
	MediaTypeYAML     = "application/yaml"
	MediaTypeTOML     = "application/toml"
	MediaTypeXML      = "application/xml"
)

type parserKind uint8

const (
	parserKindText parserKind = iota
	parserKindXberg
)

// formatSpec is the one in-process source of truth for upload admission,
// parser routing, and the canonical suffix used to stage immutable snapshots.
// It is parser configuration rather than a persisted domain entity.
type formatSpec struct {
	extensions []string
	mediaType  string
	suffix     string
	parser     parserKind
	validate   func(string) error
}

var formatSpecs = []formatSpec{
	{[]string{".txt"}, MediaTypeText, ".txt", parserKindText, validateUTF8File},
	{[]string{".md", ".markdown"}, MediaTypeMarkdown, ".md", parserKindText, validateUTF8File},
	{[]string{".pdf"}, MediaTypePDF, ".pdf", parserKindXberg, validatePDFFile},
	{[]string{".doc"}, MediaTypeDOC, ".doc", parserKindXberg, validateDOCFile},
	{[]string{".docx"}, MediaTypeDOCX, ".docx", parserKindXberg, validateDOCXFile},
	{[]string{".odt"}, MediaTypeODT, ".odt", parserKindXberg, validateODTFile},
	{[]string{".rtf"}, MediaTypeRTF, ".rtf", parserKindXberg, validateRTFFile},
	{[]string{".xls"}, MediaTypeXLS, ".xls", parserKindXberg, validateXLSFile},
	{[]string{".xlsx"}, MediaTypeXLSX, ".xlsx", parserKindXberg, validateXLSXFile},
	{[]string{".ods"}, MediaTypeODS, ".ods", parserKindXberg, validateODSFile},
	{[]string{".csv"}, MediaTypeCSV, ".csv", parserKindXberg, validateCSVFile},
	{[]string{".tsv"}, MediaTypeTSV, ".tsv", parserKindXberg, validateTSVFile},
	{[]string{".pptx"}, MediaTypePPTX, ".pptx", parserKindXberg, validatePPTXFile},
	{[]string{".html", ".htm"}, MediaTypeHTML, ".html", parserKindXberg, validateHTMLFile},
	{[]string{".xhtml"}, MediaTypeXHTML, ".xhtml", parserKindXberg, validateXHTMLFile},
	{[]string{".epub"}, MediaTypeEPUB, ".epub", parserKindXberg, validateEPUBFile},
	{[]string{".fb2"}, MediaTypeFB2, ".fb2", parserKindXberg, validateFB2File},
	{[]string{".mdx"}, MediaTypeMDX, ".mdx", parserKindXberg, validateUTF8File},
	{[]string{".rst"}, MediaTypeRST, ".rst", parserKindXberg, validateUTF8File},
	{[]string{".org"}, MediaTypeORG, ".org", parserKindXberg, validateUTF8File},
	{[]string{".json"}, MediaTypeJSON, ".json", parserKindXberg, validateJSONFile},
	{[]string{".yaml", ".yml"}, MediaTypeYAML, ".yaml", parserKindXberg, validateYAMLFile},
	{[]string{".toml"}, MediaTypeTOML, ".toml", parserKindXberg, validateTOMLFile},
	{[]string{".xml"}, MediaTypeXML, ".xml", parserKindXberg, validateXMLFile},
}

func formatByExtension(extension string) (formatSpec, bool) {
	extension = strings.ToLower(extension)
	for _, spec := range formatSpecs {
		if slices.Contains(spec.extensions, extension) {
			return spec, true
		}
	}
	return formatSpec{}, false
}

func formatByMediaType(mediaType string) (formatSpec, bool) {
	for _, spec := range formatSpecs {
		if spec.mediaType == mediaType {
			return spec, true
		}
	}
	return formatSpec{}, false
}

func isSupportedMediaType(mediaType string) bool {
	_, ok := formatByMediaType(mediaType)
	return ok
}

// SupportedMediaTypes returns a stable copy used by reconciliation so every
// admitted format participates in parser-profile upgrades and recovery.
func SupportedMediaTypes() []string {
	mediaTypes := make([]string, 0, len(formatSpecs))
	for _, spec := range formatSpecs {
		mediaTypes = append(mediaTypes, spec.mediaType)
	}
	return mediaTypes
}

// XbergMediaTypes returns a stable copy used by the composition root to bind
// the single Xberg adapter to every format assigned to that runtime.
func XbergMediaTypes() []string {
	mediaTypes := make([]string, 0, len(formatSpecs))
	for _, spec := range formatSpecs {
		if spec.parser == parserKindXberg {
			mediaTypes = append(mediaTypes, spec.mediaType)
		}
	}
	return mediaTypes
}

func isXbergMediaType(mediaType string) bool {
	spec, ok := formatByMediaType(mediaType)
	return ok && spec.parser == parserKindXberg
}

func isSpreadsheetMediaType(mediaType string) bool {
	switch mediaType {
	case MediaTypeXLS, MediaTypeXLSX, MediaTypeODS, MediaTypeCSV, MediaTypeTSV:
		return true
	default:
		return false
	}
}

func isPresentationMediaType(mediaType string) bool {
	return mediaType == MediaTypePPTX
}

func canonicalExtension(mediaType string) (string, error) {
	spec, ok := formatByMediaType(mediaType)
	if !ok {
		return "", fmt.Errorf("%w: media type %q", ErrUnsupportedFileType, mediaType)
	}
	return spec.suffix, nil
}

func supportedExtensionMessage() string {
	return strings.Join(SupportedExtensions(), ", ")
}

// SupportedExtensions returns the canonical, sorted upload allowlist for
// transport errors and other non-parser callers.
func SupportedExtensions() []string {
	extensions := make([]string, 0, len(formatSpecs)+4)
	for _, spec := range formatSpecs {
		extensions = append(extensions, spec.extensions...)
	}
	slices.Sort(extensions)
	return extensions
}
