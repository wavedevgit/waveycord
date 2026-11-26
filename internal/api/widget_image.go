package api

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
)

// widgetPNG renders a small placeholder widget card: a dark panel with an accent
// strip. Text is not rendered because the server ships no font; this satisfies the
// route's content type and shape without inventing a font dependency.
func widgetPNG(name string) ([]byte, error) {
	const w, h = 320, 80
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	dark := color.RGBA{0x2b, 0x2d, 0x31, 0xff}
	accent := color.RGBA{0x58, 0x65, 0xf2, 0xff}
	draw.Draw(img, img.Bounds(), &image.Uniform{dark}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, 0, 6, h), &image.Uniform{accent}, image.Point{}, draw.Src)
	// Presence dots derived from the guild name, purely decorative.
	for i := 0; i < 5; i++ {
		c := color.RGBA{0x3b, 0xa5, 0x5d, 0xff}
		if name != "" && int(name[0])%5 <= i {
			c = color.RGBA{0x4e, 0x50, 0x56, 0xff}
		}
		draw.Draw(img, image.Rect(24+i*20, 40, 36+i*20, 52), &image.Uniform{c}, image.Point{}, draw.Src)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
