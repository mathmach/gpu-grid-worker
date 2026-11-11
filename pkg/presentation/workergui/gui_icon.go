package workergui

import (
	"image"
	"image/color"
	"sync/atomic"
)

func NewEventCoalescer(postFn func(func()), syncFn func()) func() {
	var pending atomic.Bool
	return func() {
		if pending.CompareAndSwap(false, true) {
			postFn(func() {
				pending.Store(false)
				syncFn()
			})
		}
	}
}

func buildWorkerIcon() image.Image {
	const sz = 32
	img := image.NewRGBA(image.Rect(0, 0, sz, sz))
	bg := color.RGBA{R: 18, G: 22, B: 31, A: 255}
	border := color.RGBA{R: 45, G: 55, B: 75, A: 255}
	accent := color.RGBA{R: 37, G: 99, B: 235, A: 255}
	green := color.RGBA{R: 34, G: 197, B: 94, A: 255}

	for y := range sz {
		for x := range sz {
			if isCornerPixel(x, y, sz) {
				continue
			}
			c := bg
			if x == 1 || y == 1 || x == sz-2 || y == sz-2 {
				c = border
			}
			img.SetRGBA(x, y, c)
		}
	}
	fillRect(img, 6, 6, 8, 8, accent)
	fillRect(img, 18, 6, 8, 8, accent)
	fillRect(img, 6, 18, 8, 8, accent)
	fillRect(img, 18, 18, 8, 8, green)
	fillRect(img, 14, 9, 4, 2, border)
	fillRect(img, 14, 21, 4, 2, border)
	fillRect(img, 9, 14, 2, 4, border)
	fillRect(img, 21, 14, 2, 4, border)
	return img
}

func isCornerPixel(x, y, sz int) bool {
	return (x < 2 && y < 2) || (x >= sz-2 && y < 2) || (x < 2 && y >= sz-2) || (x >= sz-2 && y >= sz-2)
}

func fillRect(img *image.RGBA, x0, y0, w, h int, col color.RGBA) {
	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			img.SetRGBA(x, y, col)
		}
	}
}
