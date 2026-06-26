package main

import (
	"bufio"
	"fmt"
	"image"
	"os"
	"sort"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// ocrModelID is the logical model name reported on /healthz.
const ocrModelID = "PaddlePaddle/PP-OCRv5_mobile"

// DB detection postprocess params, read from the PP-OCRv5 det inference.yml (not
// guesses). Rec geometry comes from the rec inference.yml.
const (
	detLongSide    = 960
	detThresh      = 0.3 // probability-map binarization
	detBoxThresh   = 0.6 // min mean score to keep a box
	detUnclipRatio = 1.5 // box dilation
	detMinSize     = 3   // drop boxes smaller than this (resized px)

	recHeight = 48
	recWidth  = 320 // fixed batch width; PP-OCRv5 pads to 320
)

// ocrEngine holds the long-lived detection + recognition sessions and the CTC
// charset. PP-OCRv5 mobile det finds text boxes; rec reads each crop. Both ORT
// sessions are reused across requests.
//
// onnxruntime sessions are not guaranteed safe for concurrent Run calls when the
// graph is shared, so a single mutex serializes inference. The server's extract
// lane already bounds concurrency; OCR is CPU-bound and this keeps it simple and
// correct. // single lock; per-session pool if extract throughput matters.
type ocrEngine struct {
	mu      sync.Mutex
	det     *ort.DynamicAdvancedSession
	rec     *ort.DynamicAdvancedSession
	detOpts *ort.SessionOptions
	recOpts *ort.SessionOptions
	charset []string
}

func newOCREngine(detPath, recPath, keysPath string, intraOp, interOp int) (*ocrEngine, error) {
	cs, err := loadCharset(keysPath)
	if err != nil {
		return nil, fmt.Errorf("load ocr charset: %w", err)
	}
	detOpts, err := newSessionOptions(intraOp, interOp)
	if err != nil {
		return nil, err
	}
	det, err := ort.NewDynamicAdvancedSession(detPath, []string{"x"}, []string{"fetch_name_0"}, detOpts)
	if err != nil {
		detOpts.Destroy()
		return nil, fmt.Errorf("det session: %w", err)
	}
	recOpts, err := newSessionOptions(intraOp, interOp)
	if err != nil {
		det.Destroy()
		detOpts.Destroy()
		return nil, err
	}
	rec, err := ort.NewDynamicAdvancedSession(recPath, []string{"x"}, []string{"fetch_name_0"}, recOpts)
	if err != nil {
		det.Destroy()
		detOpts.Destroy()
		recOpts.Destroy()
		return nil, fmt.Errorf("rec session: %w", err)
	}
	return &ocrEngine{det: det, rec: rec, detOpts: detOpts, recOpts: recOpts, charset: cs}, nil
}

func (e *ocrEngine) close() {
	if e.det != nil {
		_ = e.det.Destroy()
	}
	if e.rec != nil {
		_ = e.rec.Destroy()
	}
	if e.detOpts != nil {
		e.detOpts.Destroy()
	}
	if e.recOpts != nil {
		e.recOpts.Destroy()
	}
}

// Recognize runs the full detect -> crop -> recognize pipeline and returns the
// page text, one detected line per output line, ordered top-to-bottom then
// left-to-right. Empty result means no text was found (not an error).
func (e *ocrEngine) Recognize(img image.Image) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	rgba := toRGBA(img)
	data, rw, rh, sx, sy := preprocessDet(rgba)
	prob, shape, err := runSingle(e.det, ort.NewShape(1, 3, int64(rh), int64(rw)), data)
	if err != nil {
		return "", fmt.Errorf("det run: %w", err)
	}
	if len(shape) != 4 {
		return "", fmt.Errorf("unexpected det output rank %v", shape)
	}
	ph, pw := int(shape[2]), int(shape[3])
	boxes := dbPostProcess(prob, pw, ph)

	ow, oh := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	for i := range boxes {
		boxes[i].minX = clampI(int(float64(boxes[i].minX)*sx), 0, ow-1)
		boxes[i].maxX = clampI(int(float64(boxes[i].maxX)*sx), 0, ow)
		boxes[i].minY = clampI(int(float64(boxes[i].minY)*sy), 0, oh-1)
		boxes[i].maxY = clampI(int(float64(boxes[i].maxY)*sy), 0, oh)
	}
	sortBoxes(boxes)

	var lines []string
	for _, b := range boxes {
		text, _, err := e.recognize(cropRGBA(rgba, b))
		if err != nil {
			return "", fmt.Errorf("rec run: %w", err)
		}
		if t := strings.TrimSpace(text); t != "" {
			lines = append(lines, t)
		}
	}
	return strings.Join(lines, "\n"), nil
}

type box struct {
	minX, minY, maxX, maxY int
	score                  float32
}

// preprocessDet resizes so the longer side is detLongSide, snaps both dims to
// multiples of 32 (DBNet requirement), normalizes with ImageNet mean/std applied
// in BGR channel order (a training quirk), and returns a CHW BGR buffer plus the
// resized dims and the original/resized scale factors.
func preprocessDet(src *image.RGBA) (buf []float32, rw, rh int, sx, sy float64) {
	ow, oh := src.Bounds().Dx(), src.Bounds().Dy()
	ratio := float64(detLongSide) / float64(max(ow, oh))
	rw = round32(float64(ow) * ratio)
	rh = round32(float64(oh) * ratio)
	resized := bilinearResize(src, rw, rh)
	sx = float64(ow) / float64(rw)
	sy = float64(oh) / float64(rh)

	mean := [3]float32{0.485, 0.456, 0.406}
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

// dbPostProcess thresholds the probability map and extracts axis-aligned boxes via
// connected components, scoring each by mean probability and dilating by the
// unclip ratio. Simplified vs PaddleOCR (no contour/minAreaRect/polygon): rotated
// or skewed text is cropped as its bounding box. // bounding-rect MVP; rotated
// crop (minAreaRect + perspective warp) is Phase 4b.
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
		hi := b[i].maxY - b[i].minY
		if absInt(b[i].minY-b[j].minY) > hi/2 {
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

// recognize runs a single cropped text-line image and returns text + mean score.
func (e *ocrEngine) recognize(img image.Image) (string, float32, error) {
	data := preprocessRec(toRGBA(img))
	out, outShape, err := runSingle(e.rec, ort.NewShape(1, 3, recHeight, recWidth), data)
	if err != nil {
		return "", 0, err
	}
	if len(outShape) != 3 {
		return "", 0, fmt.Errorf("unexpected rec output rank %v", outShape)
	}
	return e.ctcDecode(out, int(outShape[1]), int(outShape[2]))
}

// preprocessRec resizes to height 48 keeping aspect ratio (capped at width 320),
// normalizes BGR to [-1,1] via (x/255-0.5)/0.5, and writes a zero-padded CHW
// float32 buffer of shape [3,48,320].
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
			buf[0*plane+y*recWidth+x] = (b/255 - 0.5) / 0.5
			buf[1*plane+y*recWidth+x] = (g/255 - 0.5) / 0.5
			buf[2*plane+y*recWidth+x] = (r/255 - 0.5) / 0.5
		}
	}
	return buf
}

// ctcDecode performs greedy CTC decoding: argmax per timestep, collapse repeats,
// drop the blank (index 0). Returns the string and mean confidence of kept steps.
func (e *ocrEngine) ctcDecode(logits []float32, steps, classes int) (string, float32, error) {
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

// loadCharset reads the PP-OCR character dictionary (one entry per line) and
// builds the CTC index->string table: index 0 is the blank, 1..N map to dict
// lines 0..N-1, and the final index is the space character.
func loadCharset(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var dict []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		dict = append(dict, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(dict) == 0 {
		return nil, fmt.Errorf("empty charset %s", path)
	}

	table := make([]string, 0, len(dict)+2)
	table = append(table, "")      // 0: blank
	table = append(table, dict...) // 1..N
	table = append(table, " ")     // N+1: space
	return table, nil
}

// runSingle feeds one NCHW float32 input through a dynamic single-input/output
// session and returns the output data and shape. PP-OCR det and rec both have one
// input and one output.
func runSingle(sess *ort.DynamicAdvancedSession, shape ort.Shape, data []float32) ([]float32, []int64, error) {
	in, err := ort.NewTensor(shape, data)
	if err != nil {
		return nil, nil, fmt.Errorf("new input tensor: %w", err)
	}
	defer in.Destroy()

	outputs := []ort.Value{nil}
	if err := sess.Run([]ort.Value{in}, outputs); err != nil {
		return nil, nil, fmt.Errorf("run: %w", err)
	}
	out, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, nil, fmt.Errorf("unexpected output type %T", outputs[0])
	}
	defer out.Destroy()
	return out.GetData(), out.GetShape(), nil
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
