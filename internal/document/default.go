//go:build !xberg || !cgo

package document

func newBaseExtractor() Extractor {
	return cliExtractor{}
}
