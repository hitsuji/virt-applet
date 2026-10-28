package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

var (
	colorRunning = color.RGBA{0x3f, 0xb9, 0x50, 0xff}
	colorIdle    = color.RGBA{0x8b, 0x94, 0x9e, 0xff}
	colorError   = color.RGBA{0xe5, 0x53, 0x4b, 0xff}
	colorFrame   = color.RGBA{0xe6, 0xed, 0xf3, 0xff}
)

// makeIcon draws a small monitor whose screen is filled with the given colour.
func makeIcon(screen color.RGBA) []byte {
	const n = 64
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	fill := func(x0, y0, x1, y1 int, c color.RGBA) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				img.SetRGBA(x, y, c)
			}
		}
	}
	fill(4, 8, 60, 46, colorFrame)   // bezel
	fill(9, 13, 55, 41, screen)      // screen
	fill(28, 46, 36, 53, colorFrame) // neck
	fill(18, 53, 46, 58, colorFrame) // foot

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

var (
	iconRunning = makeIcon(colorRunning)
	iconIdle    = makeIcon(colorIdle)
	iconError   = makeIcon(colorError)
)
