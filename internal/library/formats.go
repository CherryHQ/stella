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
	MediaTypePPT      = "application/vnd.ms-powerpoint"
	MediaTypePPTX     = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	MediaTypeODP      = "application/vnd.oasis.opendocument.presentation"
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

// extractionMode selects the one normalization path used after a parser has
// extracted a validated source. It is deliberately independent from the
// parser binary: Xberg serves all three modes, while Stella owns the fidelity
// and citation contract applied to its output.
type extractionMode uint8

const (
	extractionModeNarrative extractionMode = iota
	extractionModeTable
	extractionModePaged
)

// citationPolicy records which source coordinates are reliable enough to
// expose. Unsupported coordinates are removed even if Xberg happens to emit
// values for a particular document.
type citationPolicy struct {
	headingPath         bool
	pageRange           bool
	sourceRowRange      bool
	enforcePageBoundary bool
}

// formatSpec is the one in-process source of truth for upload admission,
// parser routing, normalization, citations, and the canonical suffix used to
// stage immutable snapshots. It is parser configuration rather than a
// persisted domain entity.
type formatSpec struct {
	extensions            []string
	mediaType             string
	suffix                string
	parser                parserKind
	validate              func(string) error
	mode                  extractionMode
	citations             citationPolicy
	xbergMediaTypeAliases []string
}

var formatSpecs = []formatSpec{
	{extensions: []string{".txt"}, mediaType: MediaTypeText, suffix: ".txt", parser: parserKindText, validate: validateUTF8File, mode: extractionModeNarrative},
	{extensions: []string{".md", ".markdown"}, mediaType: MediaTypeMarkdown, suffix: ".md", parser: parserKindText, validate: validateUTF8File, mode: extractionModeNarrative},
	{extensions: []string{".pdf"}, mediaType: MediaTypePDF, suffix: ".pdf", parser: parserKindXberg, validate: validatePDFFile, mode: extractionModePaged, citations: citationPolicy{headingPath: true, pageRange: true}},
	{extensions: []string{".doc"}, mediaType: MediaTypeDOC, suffix: ".doc", parser: parserKindXberg, validate: validateDOCFile, mode: extractionModeNarrative},
	{extensions: []string{".docx"}, mediaType: MediaTypeDOCX, suffix: ".docx", parser: parserKindXberg, validate: validateDOCXFile, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
	{extensions: []string{".odt"}, mediaType: MediaTypeODT, suffix: ".odt", parser: parserKindXberg, validate: validateODTFile, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
	{extensions: []string{".rtf"}, mediaType: MediaTypeRTF, suffix: ".rtf", parser: parserKindXberg, validate: validateRTFFile, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
	{extensions: []string{".xls"}, mediaType: MediaTypeXLS, suffix: ".xls", parser: parserKindXberg, validate: validateXLSFile, mode: extractionModeTable, citations: citationPolicy{headingPath: true}},
	{extensions: []string{".xlsx"}, mediaType: MediaTypeXLSX, suffix: ".xlsx", parser: parserKindXberg, validate: validateXLSXFile, mode: extractionModeTable, citations: citationPolicy{headingPath: true}},
	{extensions: []string{".ods"}, mediaType: MediaTypeODS, suffix: ".ods", parser: parserKindXberg, validate: validateODSFile, mode: extractionModeTable, citations: citationPolicy{headingPath: true}},
	{extensions: []string{".csv"}, mediaType: MediaTypeCSV, suffix: ".csv", parser: parserKindXberg, validate: validateCSVFile, mode: extractionModeTable, citations: citationPolicy{headingPath: true, sourceRowRange: true}},
	{extensions: []string{".tsv"}, mediaType: MediaTypeTSV, suffix: ".tsv", parser: parserKindXberg, validate: validateTSVFile, mode: extractionModeTable, citations: citationPolicy{headingPath: true, sourceRowRange: true}},
	{extensions: []string{".ppt"}, mediaType: MediaTypePPT, suffix: ".ppt", parser: parserKindXberg, validate: validatePPTFile, mode: extractionModePaged},
	{extensions: []string{".pptx"}, mediaType: MediaTypePPTX, suffix: ".pptx", parser: parserKindXberg, validate: validatePPTXFile, mode: extractionModePaged, citations: citationPolicy{headingPath: true, pageRange: true, enforcePageBoundary: true}},
	{extensions: []string{".odp"}, mediaType: MediaTypeODP, suffix: ".odp", parser: parserKindXberg, validate: validateODPFile, mode: extractionModePaged},
	{extensions: []string{".html", ".htm"}, mediaType: MediaTypeHTML, suffix: ".html", parser: parserKindXberg, validate: validateHTMLFile, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
	{extensions: []string{".xhtml"}, mediaType: MediaTypeXHTML, suffix: ".xhtml", parser: parserKindXberg, validate: validateXHTMLFile, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}, xbergMediaTypeAliases: []string{MediaTypeHTML}},
	{extensions: []string{".epub"}, mediaType: MediaTypeEPUB, suffix: ".epub", parser: parserKindXberg, validate: validateEPUBFile, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
	{extensions: []string{".fb2"}, mediaType: MediaTypeFB2, suffix: ".fb2", parser: parserKindXberg, validate: validateFB2File, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
	{extensions: []string{".mdx"}, mediaType: MediaTypeMDX, suffix: ".mdx", parser: parserKindXberg, validate: validateUTF8File, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
	{extensions: []string{".rst"}, mediaType: MediaTypeRST, suffix: ".rst", parser: parserKindXberg, validate: validateUTF8File, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
	{extensions: []string{".org"}, mediaType: MediaTypeORG, suffix: ".org", parser: parserKindXberg, validate: validateUTF8File, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
	{extensions: []string{".json"}, mediaType: MediaTypeJSON, suffix: ".json", parser: parserKindXberg, validate: validateJSONFile, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
	{extensions: []string{".yaml", ".yml"}, mediaType: MediaTypeYAML, suffix: ".yaml", parser: parserKindXberg, validate: validateYAMLFile, mode: extractionModeNarrative},
	{extensions: []string{".toml"}, mediaType: MediaTypeTOML, suffix: ".toml", parser: parserKindXberg, validate: validateTOMLFile, mode: extractionModeNarrative},
	{extensions: []string{".xml"}, mediaType: MediaTypeXML, suffix: ".xml", parser: parserKindXberg, validate: validateXMLFile, mode: extractionModeNarrative, citations: citationPolicy{headingPath: true}},
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

func formatForParser(mediaType string, parser parserKind) (formatSpec, error) {
	spec, ok := formatByMediaType(mediaType)
	if !ok || spec.parser != parser {
		return formatSpec{}, fmt.Errorf("%w: media type %q", ErrUnsupportedFileType, mediaType)
	}
	return spec, nil
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
