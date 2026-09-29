// Package images turns the pictures people upload into the copies pages
// show: fitted to a few fixed widths (never wider than the upload), upright, in one format at one
// quality (WebP at 80 unless the app says otherwise), each made once and
// kept, with a tiny blurred placeholder to show while the photo loads.
//
// Formats are adapters. An Encoder writes the copies (WebP, AVIF and JPEG
// ship here); a Decoder reads what Go's image package can't (images/heic
// reads an iPhone's HEIC). An adapter that needs cgo belongs in a module of
// its own, so an app that doesn't use it keeps a static binary.
//
// Making a copy takes far more memory than serving one (a 24-megapixel photo
// takes a few hundred megabytes), so the server has a short-lived copy of
// itself do it (Child): the memory goes when the child exits.
package images

import (
	"image"
	"image/jpeg"
	"io"
	"runtime"

	"golang.org/x/sys/cpu"

	"github.com/gen2brain/avif" // registers AVIF with image.Decode; WASM, no cgo
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

// AVIF is about half WebP's size for the same look, read by every current
// browser but older ones (a page offers WebP beside it), and slow to make:
// a second or so for a phone-sized copy, so it suits copies made ahead (the
// assets', in the build) over ones made on request. Its quality scale runs
// lower than WebP's: AVIFQuality, 50, looks like WebP's 80.
type AVIF struct{}

// AVIFQuality is AVIF's quality that looks like WebP at 80 (libvips' AVIF
// default, as Rails' image processing).
const AVIFQuality = 50

// AVIFSlow says why this machine would make AVIF many times slower than it
// should, "" when it wouldn't. The encoder is WebAssembly run by wazero,
// which compiles it only for a CPU with SSE4.1 (amd64) or LSE atomics
// (arm64, for the encoder's threads), and interprets it otherwise: a copy
// takes minutes, not a second. A virtual machine given a generic CPU model
// (Proxmox's kvm64, say) hides SSE4.1 from a CPU that has it.
func AVIFSlow() string {
	switch {
	case runtime.GOARCH == "amd64" && !cpu.X86.HasSSE41:
		return "this CPU has no SSE4.1 (a VM on a generic CPU model, kvm64 say: give it the host's)"
	case runtime.GOARCH == "arm64" && !cpu.ARM64.HasATOMICS:
		return "this CPU has no LSE atomics (ARMv8.0: a Raspberry Pi 4, say)"
	case runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64":
		return "this is " + runtime.GOARCH + ", where the encoder isn't compiled"
	}
	return ""
}

func (AVIF) ContentType() string { return "image/avif" }
func (AVIF) Ext() string         { return ".avif" }
func (AVIF) Encode(w io.Writer, img image.Image, quality int) error {
	return avif.Encode(w, img, avif.Options{Quality: quality, Speed: 6})
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
