// Package vision turns image bytes into text.
//
// Two callers need that: the read tool, when the agent reads an image file and
// the running model cannot see, and the request-time materializer, when inline
// images already in the transcript must be rendered for a text-only model.
// Both get the same three-step degradation — configured vision model, then
// Xberg text extraction, then a hard error — and the same decode budget, so an
// oversized or hostile image cannot reach a decoder through either door.
package vision

import (
	"bytes"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register webp decoder for image.Decode
)

const (
	// MaxImageDim is the longest edge (px) an inlined image is resized to fit.
	MaxImageDim = 2000
	// MaxInlineBytes caps the encoded image size sent to the model,
	// staying under provider inline-image limits (Anthropic allows ~5MB).
	MaxInlineBytes = 5 * 1024 * 1024
	// maxImageInputBytes caps the raw file size we are willing to decode,
	// rejecting oversized inputs before allocating any pixel buffer.
	maxImageInputBytes = 30 * 1024 * 1024
	// maxImagePixels bounds total pixels (width*height) decoded, guarding
	// against decompression bombs whose header is tiny but expand enormously.
	maxImagePixels = 50_000_000
)

// ValidateBudget rejects oversized inputs before any full decode allocates a
// pixel buffer: first by raw byte size, then by the decoded dimensions read from
// the header alone. It returns the parsed config so callers can reuse it without
// decoding the header twice. Runs on every image path (vision inline, the
// understanding service, and the Xberg text fallback) so a decompression bomb
// cannot reach any decoder.
func ValidateBudget(data []byte) (image.Config, error) {
	if len(data) > maxImageInputBytes {
		return image.Config{}, fmt.Errorf("image input too large: %d bytes exceeds %d", len(data), maxImageInputBytes)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Config{}, err
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return image.Config{}, fmt.Errorf("image too large to decode: %dx%d exceeds %d pixels", cfg.Width, cfg.Height, maxImagePixels)
	}
	return cfg, nil
}

// PrepareInline downsizes an image to fit MaxImageDim on its longest edge,
// re-encoding only when a resize is needed. Images already within bounds are
// returned untouched. WebP is re-encoded as PNG since the standard library
// cannot encode it. The caller must pass the config from a prior
// ValidateBudget check.
func PrepareInline(data []byte, cfg image.Config, mime string) ([]byte, string, error) {
	if cfg.Width <= MaxImageDim && cfg.Height <= MaxImageDim && mime != "image/webp" {
		return data, mime, nil
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	fitted := fitImage(img, MaxImageDim)

	var buf bytes.Buffer
	outMime := "image/png"
	switch mime {
	case "image/jpeg":
		outMime = "image/jpeg"
		err = jpeg.Encode(&buf, fitted, &jpeg.Options{Quality: 90})
	case "image/gif":
		outMime = "image/gif"
		err = gif.Encode(&buf, fitted, nil)
	default:
		err = png.Encode(&buf, fitted)
	}
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), outMime, nil
}

// fitImage scales src down so its longest edge is at most maxDim, preserving
// aspect ratio. Images already within bounds are returned unchanged; src is
// never upscaled.
func fitImage(src image.Image, maxDim int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return src
	}
	scale := math.Min(float64(maxDim)/float64(w), float64(maxDim)/float64(h))
	dstW := max(int(math.Round(float64(w)*scale)), 1)
	dstH := max(int(math.Round(float64(h)*scale)), 1)
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// extensionForMime returns the file extension Xberg needs to pick a decoder,
// defaulting to .png for anything unrecognized.
func extensionForMime(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}
