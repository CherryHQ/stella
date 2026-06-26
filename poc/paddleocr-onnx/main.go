package main

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage:\n  poc rec <image>   recognize a single cropped text line\n  poc ocr <image>   detect + recognize a full image")
		os.Exit(2)
	}
	cmd, imgPath := os.Args[1], os.Args[2]

	base, _ := os.Getwd()
	if err := initORT(base); err != nil {
		fail(err)
	}
	defer shutdownORT()

	models := filepath.Join(base, "models")
	img, err := loadImage(imgPath)
	if err != nil {
		fail(err)
	}

	switch cmd {
	case "rec":
		eng, err := newRecEngine(filepath.Join(models, "rec.onnx"), filepath.Join(models, "rec_keys.txt"))
		if err != nil {
			fail(err)
		}
		defer eng.close()
		text, score, err := eng.recognize(img)
		if err != nil {
			fail(err)
		}
		fmt.Printf("text:  %s\nscore: %.3f\n", text, score)

	case "ocr":
		if err := runOCR(models, img); err != nil {
			fail(err)
		}

	default:
		fail(fmt.Errorf("unknown command %q", cmd))
	}
}

func loadImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return img, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
