// Package vision turns verified image bytes into bounded text.
package vision

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register webp decoder for image.Decode

	"github.com/CherryHQ/stella/pkg/ai"
)

const (
	// MaxRendererPayloadBytes is the hard provider image payload ceiling.
	MaxRendererPayloadBytes = 5 * 1024 * 1024
	// MaxImageInputBytes aliases the canonical session-ingress ceiling. Together
	// with MaxImagePixels, it protects model and Xberg paths from compressed-byte
	// and decompression-bomb inputs.
	MaxImageInputBytes = ai.MaxImageInputBytes
	MaxImagePixels     = 50_000_000
)

// ValidateBudget rejects unsafe image bytes before full decode for callers
// that do not have a declared MIME to compare.
func ValidateBudget(data []byte) (image.Config, error) {
	cfg, _, err := decodeConfig(data)
	return cfg, err
}

// ValidateImage validates the security budget and requires the declared MIME to
// match the decoder's actual format. Canonical media never trusts an extension
// or provider-supplied content type.
func ValidateImage(data []byte, declaredMIME string) (image.Config, string, error) {
	cfg, format, err := decodeConfig(data)
	if err != nil {
		return image.Config{}, "", err
	}
	detected, ok := mimeForFormat(format)
	if !ok {
		return image.Config{}, "", fmt.Errorf("unsupported decoded image format %q", format)
	}
	if strings.TrimSpace(strings.ToLower(declaredMIME)) != detected {
		return image.Config{}, "", fmt.Errorf("image MIME %q does not match detected %q", declaredMIME, detected)
	}
	return cfg, detected, nil
}

func decodeConfig(data []byte) (image.Config, string, error) {
	if len(data) == 0 {
		return image.Config{}, "", fmt.Errorf("empty image")
	}
	if len(data) > MaxImageInputBytes {
		return image.Config{}, "", fmt.Errorf("image input too large: %d bytes exceeds %d", len(data), MaxImageInputBytes)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Config{}, "", err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > MaxImagePixels {
		return image.Config{}, "", fmt.Errorf("image too large to decode: %dx%d exceeds %d pixels", cfg.Width, cfg.Height, MaxImagePixels)
	}
	return cfg, format, nil
}

func mimeForFormat(format string) (string, bool) {
	switch format {
	case "png":
		return "image/png", true
	case "jpeg":
		return "image/jpeg", true
	case "gif":
		return "image/gif", true
	case "webp":
		return "image/webp", true
	default:
		return "", false
	}
}

// PrepareRendererPayloadContext keeps original pixels and dimensions untouched
// whenever they fit the renderer payload ceiling. Both baseline rendering and
// active provider hydration use this adaptive payload preparation. Only a hard
// payload overflow causes reduction; no fixed dimension ceiling or tiling is
// applied. The standard-library decode/encode calls themselves are not
// interruptible, so it checks ctx around each potentially expensive step.
func PrepareRendererPayloadContext(ctx context.Context, data []byte, cfg image.Config, mime string) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if len(data) <= MaxRendererPayloadBytes {
		return data, mime, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	maxDim := max(cfg.Width, cfg.Height)
	for range 8 {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		fitted := fitImage(img, maxDim)
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		encoded, outMIME, err := encodeImage(fitted, mime)
		if err != nil {
			return nil, "", err
		}
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if len(encoded) <= MaxRendererPayloadBytes {
			return encoded, outMIME, nil
		}
		scale := math.Sqrt(float64(MaxRendererPayloadBytes)/float64(len(encoded))) * 0.9
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		next := max(int(math.Floor(float64(maxDim)*scale)), 1)
		if next >= maxDim {
			next = maxDim - 1
		}
		if next < 1 {
			break
		}
		maxDim = next
	}
	return nil, "", fmt.Errorf("image cannot fit renderer payload ceiling of %d bytes", MaxRendererPayloadBytes)
}

func encodeImage(img image.Image, mime string) ([]byte, string, error) {
	var buf bytes.Buffer
	outMIME := "image/png"
	var err error
	switch mime {
	case "image/jpeg":
		outMIME = "image/jpeg"
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	case "image/gif":
		outMIME = "image/gif"
		err = gif.Encode(&buf, img, nil)
	default:
		err = png.Encode(&buf, img)
	}
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), outMIME, nil
}

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
