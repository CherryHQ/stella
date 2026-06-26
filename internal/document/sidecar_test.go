package document

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeBase is a base extractor with scripted output for both methods.
type fakeBase struct {
	res *Result
	err error
}

func (f fakeBase) ExtractFile(context.Context, string, Options) (*Result, error) {
	return f.res, f.err
}

func (f fakeBase) ExtractBytes(context.Context, []byte, string, Options) (*Result, error) {
	return f.res, f.err
}

// fakeOCR records its calls and returns scripted text.
type fakeOCR struct {
	called bool
	text   string
	err    error
}

func (o *fakeOCR) Extract(_ context.Context, _ string, _ []byte, _ bool) (*Result, error) {
	o.called = true
	if o.err != nil {
		return nil, o.err
	}
	return &Result{Content: o.text}, nil
}

func TestCompositeSkipsOCRWhenBaseHasText(t *testing.T) {
	t.Cleanup(func() { SetLocalOCR(nil) })
	ocr := &fakeOCR{text: "ocr text"}
	SetLocalOCR(ocr)

	c := compositeExtractor{base: fakeBase{res: &Result{Content: "real text"}}}
	res, err := c.ExtractBytes(context.Background(), pngBytes(), "image/png", Options{})
	if err != nil {
		t.Fatalf("ExtractBytes: %v", err)
	}
	if res.Content != "real text" {
		t.Fatalf("content = %q, want base text", res.Content)
	}
	if ocr.called {
		t.Fatal("OCR ran even though the base produced text")
	}
}

func TestCompositeFallsBackToOCRForImage(t *testing.T) {
	t.Cleanup(func() { SetLocalOCR(nil) })
	ocr := &fakeOCR{text: "scanned words"}
	SetLocalOCR(ocr)

	// Base returns empty for an image (no text layer) -> OCR should run.
	c := compositeExtractor{base: fakeBase{res: &Result{Content: "  "}}}
	res, err := c.ExtractBytes(context.Background(), pngBytes(), "image/png", Options{})
	if err != nil {
		t.Fatalf("ExtractBytes: %v", err)
	}
	if !ocr.called {
		t.Fatal("OCR did not run for an empty-text image")
	}
	if res.Content != "scanned words" {
		t.Fatalf("content = %q, want OCR text", res.Content)
	}
}

func TestCompositeDoesNotOCRNonImage(t *testing.T) {
	t.Cleanup(func() { SetLocalOCR(nil) })
	ocr := &fakeOCR{text: "should not appear"}
	SetLocalOCR(ocr)

	// A text document that extracts to empty must not be re-run through OCR.
	c := compositeExtractor{base: fakeBase{res: nil, err: ErrEmptyContent}}
	_, err := c.ExtractBytes(context.Background(), []byte("plain text, not an image"), "text/plain", Options{})
	if !errors.Is(err, ErrEmptyContent) {
		t.Fatalf("err = %v, want ErrEmptyContent passthrough", err)
	}
	if ocr.called {
		t.Fatal("OCR ran for a non-image input")
	}
}

func TestCompositeNoOCRInstalled(t *testing.T) {
	SetLocalOCR(nil) // explicit: nothing installed
	c := compositeExtractor{base: fakeBase{res: nil, err: ErrUnavailable}}
	_, err := c.ExtractBytes(context.Background(), pngBytes(), "image/png", Options{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want base error passthrough when no OCR", err)
	}
}

func TestCompositeExtractFileOCR(t *testing.T) {
	t.Cleanup(func() { SetLocalOCR(nil) })
	ocr := &fakeOCR{text: "from file"}
	SetLocalOCR(ocr)

	path := filepath.Join(t.TempDir(), "scan.png")
	if err := os.WriteFile(path, pngBytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	c := compositeExtractor{base: fakeBase{res: &Result{Content: ""}}}
	res, err := c.ExtractFile(context.Background(), path, Options{})
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	if !ocr.called || res.Content != "from file" {
		t.Fatalf("OCR fallback failed: called=%v content=%q", ocr.called, res.Content)
	}
}

// pngBytes returns a minimal valid 1x1 PNG so http.DetectContentType and the
// image/* mime gate treat the payload as an image.
func pngBytes() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
}
