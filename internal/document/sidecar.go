package document

import (
	"context"
	gomime "mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// OCR is the local image-OCR capability the composite extractor falls back to —
// satisfied by an adapter over the ML sidecar client. It is defined here, in terms
// of document types, so this package keeps no dependency on internal/ml.
type OCR interface {
	// Extract runs OCR over raw image bytes. forceOCR is a hint to skip any
	// text-layer fast path; the sidecar's image endpoint always OCRs regardless.
	Extract(ctx context.Context, mime string, data []byte, forceOCR bool) (*Result, error)
}

// localOCR holds the process-wide OCR backend, installed once at boot via
// SetLocalOCR before any tool is built. It is a global because NewExtractor is the
// single extractor factory and is called from many tool-construction sites that do
// not thread a client through; an atomic pointer keeps the read race-free.
// global injection point; per-call wiring if a request ever needs its own backend.
var localOCR atomic.Pointer[ocrHolder]

type ocrHolder struct{ ocr OCR }

// SetLocalOCR enables sidecar OCR fallback for every subsequently built extractor.
// Passing nil disables it again. Safe to call before or after extractors exist;
// existing extractors read the global per request.
func SetLocalOCR(o OCR) {
	if o == nil {
		localOCR.Store(nil)
		return
	}
	localOCR.Store(&ocrHolder{ocr: o})
}

func currentOCR() OCR {
	if h := localOCR.Load(); h != nil {
		return h.ocr
	}
	return nil
}

// NewExtractor returns the platform base extractor, transparently wrapped with
// sidecar OCR fallback when one has been installed via SetLocalOCR. The wrapper
// reads the global per call, so enabling OCR after construction still takes effect.
func NewExtractor() Extractor {
	return compositeExtractor{base: newBaseExtractor()}
}

// compositeExtractor tries the base (text-layer) extractor first and falls back to
// local OCR for image inputs the base could not turn into text. The fallback only
// fires for images: text documents that legitimately extract to empty are not
// re-run through OCR.
type compositeExtractor struct {
	base Extractor
}

func (c compositeExtractor) ExtractFile(ctx context.Context, path string, opts Options) (*Result, error) {
	res, err := c.base.ExtractFile(ctx, path, opts)
	if ocrSatisfied(res, err) {
		return res, err
	}
	ocr := currentOCR()
	if ocr == nil {
		return res, err
	}
	data, mime, readErr := readForOCR(path)
	if readErr != nil || !isImageMime(mime) {
		return res, err
	}
	return runOCR(ctx, ocr, data, mime, res, err)
}

func (c compositeExtractor) ExtractBytes(ctx context.Context, data []byte, mime string, opts Options) (*Result, error) {
	res, err := c.base.ExtractBytes(ctx, data, mime, opts)
	if ocrSatisfied(res, err) {
		return res, err
	}
	ocr := currentOCR()
	if ocr == nil {
		return res, err
	}
	effMime := mime
	if !isImageMime(effMime) {
		effMime = http.DetectContentType(data)
	}
	if !isImageMime(effMime) {
		return res, err
	}
	return runOCR(ctx, ocr, data, effMime, res, err)
}

// ocrSatisfied reports whether the base extractor already produced usable text, in
// which case OCR is skipped.
func ocrSatisfied(res *Result, err error) bool {
	return err == nil && res != nil && strings.TrimSpace(res.Content) != ""
}

// runOCR invokes the OCR backend and normalizes the result, preferring the base
// extractor's error over the OCR error when both fail so the original failure
// reason is not masked.
func runOCR(ctx context.Context, ocr OCR, data []byte, mime string, baseRes *Result, baseErr error) (*Result, error) {
	ores, oerr := ocr.Extract(ctx, mime, data, false)
	if oerr != nil {
		if baseErr != nil {
			return baseRes, baseErr
		}
		return nil, oerr
	}
	return NormalizeResult(ores)
}

func isImageMime(mime string) bool {
	return strings.HasPrefix(mime, "image/")
}

// readForOCR reads a file and resolves its mime: extension first (authoritative
// for the file's intent), content sniffing as a fallback. It does not error on an
// unknown mime — the caller's isImageMime gate decides whether OCR applies.
func readForOCR(path string) (data []byte, mime string, err error) {
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	if ext := filepath.Ext(path); ext != "" {
		mime = gomime.TypeByExtension(ext)
	}
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	// TypeByExtension may append "; charset=..."; keep just the media type.
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	return data, mime, nil
}
