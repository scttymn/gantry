package images

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func FuzzDimensionsAndResize(f *testing.F) {
	var valid bytes.Buffer
	_ = png.Encode(&valid, image.NewRGBA(image.Rect(0, 0, 8, 6)))
	f.Add(valid.Bytes())
	f.Add([]byte("GIF89a\xff\xff\xff\xff\x00\x00\x00"))
	f.Add([]byte{})
	f.Add([]byte("not an image"))
	p := &Pipeline{MaxPixels: 10_000}
	f.Fuzz(func(t *testing.T, data []byte) {
		w, h, _, err := p.Dimensions(data)
		if err == nil && w > 0 && h > 0 && int64(w)*int64(h) > int64(p.MaxPixels) {
			t.Fatalf("Dimensions accepted %dx%d exceeding MaxPixels %d", w, h, p.MaxPixels)
		}
		_, _ = p.Resize(data, 160, 80)
	})
}
