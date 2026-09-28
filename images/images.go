// Package images turns the pictures people upload into the copies pages
// show: fitted to a few fixed widths (never wider than the upload), upright, in one format at one
// quality (WebP at 80 unless the app says otherwise), each made once and
// kept, with a tiny blurred placeholder to show while the photo loads.
//
// Formats are adapters. An Encoder writes the copies (WebP and JPEG ship
// here); a Decoder reads what Go's image package can't (images/heic reads an
// iPhone's HEIC). An adapter that needs cgo, a native AVIF encoder say,
// belongs in a module of its own, so an app that doesn't use it keeps a
// static binary.
//
// Making a copy takes far more memory than serving one (a 24-megapixel photo
// takes a few hundred megabytes), so the server has a short-lived copy of
// itself do it (Child): the memory goes when the child exits.
package images

import (
	"image"
	"image/jpeg"
	"io"

	"github.com/gen2brain/webp" // registers WebP with image.Decode; WASM, no cgo
)

// An Encoder writes images in one format.
type Encoder interface {
	ContentType() string // "image/webp"
	Ext() string         // ".webp"
	Encode(w io.Writer, img image.Image, quality int) error
}

// WebP is the default: about 40% smaller than JPEG at the same quality
// setting, read by every current browser, and made in under a second at
// half a CPU (docs/plans/gantry.md, Evidence).
type WebP struct{}

func (WebP) ContentType() string { return "image/webp" }
func (WebP) Ext() string         { return ".webp" }
func (WebP) Encode(w io.Writer, img image.Image, quality int) error {
	return webp.Encode(w, img, webp.Options{Quality: quality})
}

// JPEG is for where WebP won't do (an email, an old device).
type JPEG struct{}

func (JPEG) ContentType() string { return "image/jpeg" }
func (JPEG) Ext() string         { return ".jpg" }
func (JPEG) Encode(w io.Writer, img image.Image, quality int) error {
	return jpeg.Encode(w, img, &jpeg.Options{Quality: quality})
}

// A Decoder reads a format Go's image package doesn't.
type Decoder interface {
	ContentType() string    // "image/heic"
	Match(data []byte) bool // the bytes are in this format
	Decode(r io.Reader) (image.Image, error)
	DecodeConfig(r io.Reader) (image.Config, error)
}
