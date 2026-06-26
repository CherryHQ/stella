package main

import (
	"fmt"
	"image"
	"path/filepath"
	"sort"

	ort "github.com/yalue/onnxruntime_go"
)

// DB postprocess params come from the PP-OCRv5 det inference.yml.
const (
	detLongSide    = 960
	detThresh      = 0.3 // probability map binarization
	detBoxThresh   = 0.6 // min mean score to keep a box
	detUnclipRatio = 1.5 // box dilation
	detMinSize     = 3   // drop boxes smaller than this (resized px)
)

type box struct {
	minX, minY, maxX, maxY int
	score                  float32
}

// runOCR detects text regions, crops each, recognizes it, and prints results
// top-to-bottom. This is the simplified POC pipeline: axis-aligned boxes only
// (no rotation), pure-Go connected-component DB postprocess.
func runOCR(models string, img image.Image) error {
	detSess, err := ort.NewDynamicAdvancedSession(filepath.Join(models, "det.onnx"),
		[]string{"x"}, []string{"fetch_name_0"}, nil)
	if err != nil {
		return fmt.Errorf("det session: %w", err)
	}
	defer detSess.Destroy()

	rec, err := newRecEngine(filepath.Join(models, "rec.onnx"), filepath.Join(models, "rec_keys.txt"))
	if err != nil {
		return err
	}
	defer rec.close()

	rgba := toRGBA(img)
	data, rw, rh, sx, sy := preprocessDet(rgba)
	prob, shape, err := runModel(detSess, ort.NewShape(1, 3, int64(rh), int64(rw)), data)
	if err != nil {
		return err
	}
	// shape: [1,1,H,W]
	ph, pw := int(shape[2]), int(shape[3])
	boxes := dbPostProcess(prob, pw, ph)

	// Scale boxes from resized space back to original image pixels.
	ow, oh := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	for i := range boxes {
		boxes[i].minX = clampI(int(float64(boxes[i].minX)*sx), 0, ow-1)
		boxes[i].maxX = clampI(int(float64(boxes[i].maxX)*sx), 0, ow)
		boxes[i].minY = clampI(int(float64(boxes[i].minY)*sy), 0, oh-1)
		boxes[i].maxY = clampI(int(float64(boxes[i].maxY)*sy), 0, oh)
	}
	sortBoxes(boxes)

	fmt.Printf("detected %d text regions\n", len(boxes))
	for i, b := range boxes {
		crop := cropRGBA(rgba, b)
		text, score, err := rec.recognize(crop)
		if err != nil {
			return err
		}
		fmt.Printf("[%2d] (%4d,%4d)-(%4d,%4d) det=%.2f rec=%.2f  %s\n",
			i, b.minX, b.minY, b.maxX, b.maxY, b.score, score, text)
	}
	return nil
}

// preprocessDet resizes so the longer side is detLongSide, snaps both dims to
// multiples of 32 (DBNet requirement), normalizes with ImageNet mean/std, and
// returns a CHW BGR buffer plus the resized dims and original/resized scale.
func preprocessDet(src *image.RGBA) (buf []float32, rw, rh int, sx, sy float64) {
	ow, oh := src.Bounds().Dx(), src.Bounds().Dy()
	ratio := float64(detLongSide) / float64(max(ow, oh))
	rw = round32(float64(ow) * ratio)
	rh = round32(float64(oh) * ratio)
	resized := bilinearResize(src, rw, rh)
	sx = float64(ow) / float64(rw)
	sy = float64(oh) / float64(rh)

	mean := [3]float32{0.485, 0.456, 0.406} // applied in BGR channel order (training quirk)
	std := [3]float32{0.229, 0.224, 0.225}
	buf = make([]float32, 3*rh*rw)
	plane := rh * rw
	for y := 0; y < rh; y++ {
		for x := 0; x < rw; x++ {
			off := y*resized.Stride + x*4
			r := float32(resized.Pix[off+0]) / 255
			g := float32(resized.Pix[off+1]) / 255
			b := float32(resized.Pix[off+2]) / 255
			buf[0*plane+y*rw+x] = (b - mean[0]) / std[0]
			buf[1*plane+y*rw+x] = (g - mean[1]) / std[1]
			buf[2*plane+y*rw+x] = (r - mean[2]) / std[2]
		}
	}
	return buf, rw, rh, sx, sy
}

func round32(v float64) int {
	n := int((v + 16) / 32)
	if n < 1 {
		n = 1
	}
	return n * 32
}

// dbPostProcess thresholds the probability map and extracts axis-aligned boxes
// via connected components, scoring each by mean probability and dilating by
// the unclip ratio. Simplified vs PaddleOCR (no contour/minAreaRect/polygon).
func dbPostProcess(prob []float32, w, h int) []box {
	bin := make([]bool, w*h)
	for i, p := range prob {
		if p > detThresh {
			bin[i] = true
		}
	}

	visited := make([]bool, w*h)
	var boxes []box
	stack := make([]int, 0, 1024)
	for start := 0; start < w*h; start++ {
		if !bin[start] || visited[start] {
			continue
		}
		// flood fill one component, tracking its bounding box
		minX, minY, maxX, maxY := w, h, -1, -1
		stack = stack[:0]
		stack = append(stack, start)
		visited[start] = true
		for len(stack) > 0 {
			idx := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			cx, cy := idx%w, idx/w
			if cx < minX {
				minX = cx
			}
			if cx > maxX {
				maxX = cx
			}
			if cy < minY {
				minY = cy
			}
			if cy > maxY {
				maxY = cy
			}
			// 4-connectivity neighbours
			if cx > 0 && bin[idx-1] && !visited[idx-1] {
				visited[idx-1] = true
				stack = append(stack, idx-1)
			}
			if cx < w-1 && bin[idx+1] && !visited[idx+1] {
				visited[idx+1] = true
				stack = append(stack, idx+1)
			}
			if cy > 0 && bin[idx-w] && !visited[idx-w] {
				visited[idx-w] = true
				stack = append(stack, idx-w)
			}
			if cy < h-1 && bin[idx+w] && !visited[idx+w] {
				visited[idx+w] = true
				stack = append(stack, idx+w)
			}
		}
		if maxX-minX < detMinSize || maxY-minY < detMinSize {
			continue
		}
		// box_thresh: mean probability over the bounding rect
		var sum float32
		var n int
		for y := minY; y <= maxY; y++ {
			for x := minX; x <= maxX; x++ {
				sum += prob[y*w+x]
				n++
			}
		}
		score := sum / float32(n)
		if score < detBoxThresh {
			continue
		}
		// unclip: dilate the box outward
		bw, bh := float64(maxX-minX), float64(maxY-minY)
		dist := bw * bh * detUnclipRatio / (2 * (bw + bh))
		d := int(dist + 0.5)
		boxes = append(boxes, box{
			minX: clampI(minX-d, 0, w-1), minY: clampI(minY-d, 0, h-1),
			maxX: clampI(maxX+d, 0, w-1), maxY: clampI(maxY+d, 0, h-1),
			score: score,
		})
	}
	return boxes
}

// sortBoxes orders boxes top-to-bottom, then left-to-right, grouping lines that
// overlap vertically by more than half a box height.
func sortBoxes(b []box) {
	sort.Slice(b, func(i, j int) bool {
		hi := (b[i].maxY - b[i].minY)
		if abs(b[i].minY-b[j].minY) > hi/2 {
			return b[i].minY < b[j].minY
		}
		return b[i].minX < b[j].minX
	})
}

func cropRGBA(src *image.RGBA, b box) *image.RGBA {
	w, h := b.maxX-b.minX, b.maxY-b.minY
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			so := (b.minY+y)*src.Stride + (b.minX+x)*4
			do := y*dst.Stride + x*4
			copy(dst.Pix[do:do+4], src.Pix[so:so+4])
		}
	}
	return dst
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
