package main

import "image"

// toRGBA copies any image into an *image.RGBA for fast, stride-addressed pixel
// access during OCR preprocessing.
func toRGBA(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return r
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// bilinearResize resizes an RGBA image to dstW x dstH using bilinear sampling.
// Good enough for OCR preprocessing and avoids pulling in an image-scaling library.
func bilinearResize(src *image.RGBA, dstW, dstH int) *image.RGBA {
	sb := src.Bounds()
	srcW, srcH := sb.Dx(), sb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	if srcW == 0 || srcH == 0 {
		return dst
	}
	sx := float64(srcW) / float64(dstW)
	sy := float64(srcH) / float64(dstH)
	for y := 0; y < dstH; y++ {
		fy := (float64(y)+0.5)*sy - 0.5
		y0 := int(fy)
		dy := fy - float64(y0)
		y1 := y0 + 1
		y0 = clampI(y0, 0, srcH-1)
		y1 = clampI(y1, 0, srcH-1)
		for x := 0; x < dstW; x++ {
			fx := (float64(x)+0.5)*sx - 0.5
			x0 := int(fx)
			dx := fx - float64(x0)
			x1 := x0 + 1
			x0 = clampI(x0, 0, srcW-1)
			x1 = clampI(x1, 0, srcW-1)
			for c := 0; c < 4; c++ {
				p00 := float64(src.Pix[(y0)*src.Stride+x0*4+c])
				p01 := float64(src.Pix[(y0)*src.Stride+x1*4+c])
				p10 := float64(src.Pix[(y1)*src.Stride+x0*4+c])
				p11 := float64(src.Pix[(y1)*src.Stride+x1*4+c])
				top := p00 + (p01-p00)*dx
				bot := p10 + (p11-p10)*dx
				dst.Pix[y*dst.Stride+x*4+c] = uint8(top + (bot-top)*dy + 0.5)
			}
		}
	}
	return dst
}

func clampI(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
