// Package heic reads HEIC, the format iPhones save photos in, for the
// images pipeline: images.Pipeline{Decoders: []images.Decoder{heic.Decoder{}}}.
// It's libheif compiled to WASM (gen2brain/heic), so there's no cgo, but it
// adds a few megabytes to the binary: an app pays for it only by importing it.
package heic

import (
	"bytes"
	"image"
	"io"

	"github.com/gen2brain/heic"
)

// Decoder reads HEIC.
type Decoder struct{}

func (Decoder) ContentType() string { return "image/heic" }

// Match: an ISO media file ("ftyp" at byte 4) whose brand is one of HEIC's.
func (Decoder) Match(data []byte) bool {
	if len(data) < 12 || !bytes.Equal(data[4:8], []byte("ftyp")) {
		return false
	}
	switch string(data[8:12]) {
	case "heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1":
		return true
	}
	return false
}

func (Decoder) Decode(r io.Reader) (image.Image, error)        { return heic.Decode(r) }
func (Decoder) DecodeConfig(r io.Reader) (image.Config, error) { return heic.DecodeConfig(r) }
