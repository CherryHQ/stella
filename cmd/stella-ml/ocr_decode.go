package main

import (
	"bytes"
	"fmt"
	"image"

	// Register the decoders image.Decode dispatches to. JPEG/PNG/GIF are stdlib;
	// WebP/BMP/TIFF come from golang.org/x/image. All are pure Go.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// decodeImage decodes raw image bytes. It relies on image.Decode sniffing the
// format from the magic bytes; the mime hint is only used to improve the error
// message, since callers occasionally send a mislabeled or empty mime.
func decodeImage(data []byte, mime string) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		if mime != "" {
			return nil, fmt.Errorf("%w (mime %q)", err, mime)
		}
		return nil, err
	}
	return img, nil
}
