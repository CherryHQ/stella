package main

import (
	"fmt"
	"image"
	"strings"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	recHeight = 48
	recWidth  = 320 // fixed batch width; PP-OCRv5 trains/pads to 320
)

// recEngine wraps the recognition model plus its CTC charset.
type recEngine struct {
	sess    *ort.DynamicAdvancedSession
	charset []string
}

func newRecEngine(modelPath, charsetPath string) (*recEngine, error) {
	cs, err := loadCharset(charsetPath)
	if err != nil {
		return nil, err
	}
	sess, err := ort.NewDynamicAdvancedSession(modelPath,
		[]string{"x"}, []string{"fetch_name_0"}, nil)
	if err != nil {
		return nil, fmt.Errorf("rec session: %w", err)
	}
	return &recEngine{sess: sess, charset: cs}, nil
}

func (e *recEngine) close() {
	if e.sess != nil {
		_ = e.sess.Destroy()
	}
}

// recognize runs a single cropped text-line image and returns text + mean score.
func (e *recEngine) recognize(img image.Image) (string, float32, error) {
	data := preprocessRec(toRGBA(img))
	shape := ort.NewShape(1, 3, recHeight, recWidth)
	out, outShape, err := runModel(e.sess, shape, data)
	if err != nil {
		return "", 0, err
	}
	// outShape: [1, T, classes]
	if len(outShape) != 3 {
		return "", 0, fmt.Errorf("unexpected rec output rank %v", outShape)
	}
	return e.ctcDecode(out, int(outShape[1]), int(outShape[2]))
}

// preprocessRec resizes to height 48 keeping aspect ratio (capped at width
// 320), normalizes BGR to [-1,1] via (x/255-0.5)/0.5, and writes a zero-padded
// CHW float32 buffer of shape [3,48,320].
func preprocessRec(src *image.RGBA) []float32 {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	ratio := float64(sw) / float64(sh)
	w := int(float64(recHeight)*ratio + 0.5)
	if w > recWidth {
		w = recWidth
	}
	if w < 1 {
		w = 1
	}
	resized := bilinearResize(src, w, recHeight)

	buf := make([]float32, 3*recHeight*recWidth)
	plane := recHeight * recWidth
	for y := 0; y < recHeight; y++ {
		for x := 0; x < w; x++ {
			off := y*resized.Stride + x*4
			r := float32(resized.Pix[off+0])
			g := float32(resized.Pix[off+1])
			b := float32(resized.Pix[off+2])
			// channel order BGR to match training (img_mode: BGR)
			buf[0*plane+y*recWidth+x] = (b/255 - 0.5) / 0.5
			buf[1*plane+y*recWidth+x] = (g/255 - 0.5) / 0.5
			buf[2*plane+y*recWidth+x] = (r/255 - 0.5) / 0.5
		}
	}
	return buf
}

// ctcDecode performs greedy CTC decoding: argmax per timestep, collapse repeats,
// drop the blank (index 0). Returns the string and mean confidence of kept steps.
func (e *recEngine) ctcDecode(logits []float32, steps, classes int) (string, float32, error) {
	var sb strings.Builder
	var sum float32
	var n int
	last := -1
	for t := 0; t < steps; t++ {
		off := t * classes
		best := 0
		bestV := logits[off]
		for c := 1; c < classes; c++ {
			if v := logits[off+c]; v > bestV {
				bestV = v
				best = c
			}
		}
		if best != 0 && best != last {
			if best < len(e.charset) {
				sb.WriteString(e.charset[best])
				sum += bestV
				n++
			}
		}
		last = best
	}
	var score float32
	if n > 0 {
		score = sum / float32(n)
	}
	return sb.String(), score, nil
}
