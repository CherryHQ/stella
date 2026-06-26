//go:build !xberg || !cgo

package document

func NewExtractor() Extractor {
	return cliExtractor{}
}
