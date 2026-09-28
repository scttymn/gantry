package images

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
)

// Widths are the widths copies come in, about 1.5× apart. Every photo on
// every page picks from these, so there's no list per kind of photo: a page
// says how wide a photo draws (a srcset's sizes) and the browser picks the
// copy. A copy is never wider than its original, so an 800px photo stops at
// 720, and a page asking for more gets that.
var Widths = []int{320, 480, 720, 1080, 1600, 2400}

// Pipeline makes and keeps an app's copies. The zero value, with Dir set,
// makes WebP at quality 80 in this process.
type Pipeline struct {
	// Dir is where copies are kept: Dir/<key>/<width>w-q<quality><ext>.
	Dir string
	// Encoder writes the copies. WebP when nil.
	Encoder Encoder
	// Widths are the widths copies come in, smallest first. images.Widths
	// when nil.
	Widths []int
	// Quality is the copies' quality when a call gives none. 80 when zero.
	Quality int
	// Decoders read formats beyond JPEG, PNG, GIF and WebP.
	Decoders []Decoder
	// Maker makes a copy: Child in a server, InProcess in tests. InProcess
	// when nil.
	Maker Maker
	// PlaceholderWidth and PlaceholderQuality are the placeholder's: its
	// own quality, so changing the copies' doesn't remake it. 32 and 50
	// when zero.
	PlaceholderWidth, PlaceholderQuality int

	// One copy is made at a time, so at most one child's memory is in use,
	// and a request for a copy another is making waits for it, then finds it.
	one          sync.Mutex
	placeholders sync.Map // key → data URI, once read
}

func (p *Pipeline) encoder() Encoder {
	if p.Encoder != nil {
		return p.Encoder
	}
	return WebP{}
}

func (p *Pipeline) widths() []int {
	if p.Widths != nil {
		return p.Widths
	}
	return Widths
}

// WidthsFor is the widths a photo original pixels wide comes in: each of the
// pipeline's up to its own. A photo narrower than the smallest comes in that
// one, at its own size (Resize never enlarges); one whose width isn't known
// (0) comes in all of them.
func (p *Pipeline) WidthsFor(original int) []int {
	all := p.widths()
	if original <= 0 {
		return all
	}
	n := 1
	for n < len(all) && all[n] <= original {
		n++
	}
	return all[:n:n]
}

// Fit is the copy to serve for a request for width of a photo original
// pixels wide: that width, or the photo's largest when width is above it.
// ok is false when width isn't one of the pipeline's, so a request can't
// have the server make copies at any width it likes.
func (p *Pipeline) Fit(width, original int) (fitted int, ok bool) {
	if !slices.Contains(p.widths(), width) {
		return 0, false
	}
	own := p.WidthsFor(original)
	return min(width, own[len(own)-1]), true
}

// QualityOr is quality, or the pipeline's when quality is zero.
func (p *Pipeline) QualityOr(quality int) int {
	switch {
	case quality > 0:
		return quality
	case p.Quality > 0:
		return p.Quality
	}
	return 80
}

func (p *Pipeline) placeholderSize() (width, quality int) {
	width, quality = p.PlaceholderWidth, p.PlaceholderQuality
	if width == 0 {
		width = 32
	}
	if quality == 0 {
		quality = 50
	}
	return
}

// ContentType is the copies' type: "image/webp".
func (p *Pipeline) ContentType() string { return p.encoder().ContentType() }

// Name is a copy's file name: "1400w-q80.webp".
func (p *Pipeline) Name(width, quality int) string {
	return fmt.Sprintf("%dw-q%d%s", width, quality, p.encoder().Ext())
}

var nameParts = regexp.MustCompile(`\A(\d+)w-q(\d+)(\.[a-z0-9]+)\z`)

// ParseName reads a copy's name back, for a request for one: a name in
// another format, or with numbers written oddly ("080"), isn't one of ours.
func (p *Pipeline) ParseName(name string) (width, quality int, ok bool) {
	m := nameParts.FindStringSubmatch(name)
	if m == nil || m[3] != p.encoder().Ext() {
		return 0, 0, false
	}
	width, _ = strconv.Atoi(m[1])
	quality, _ = strconv.Atoi(m[2])
	if strconv.Itoa(width) != m[1] || strconv.Itoa(quality) != m[2] {
		return 0, 0, false
	}
	return width, quality, true
}

// Path is where the copy of key at width and quality is kept.
func (p *Pipeline) Path(key string, width, quality int) string {
	return filepath.Join(p.Dir, key, p.Name(width, quality))
}

// Resizable reports whether the pipeline can make copies of this type.
func (p *Pipeline) Resizable(contentType string) bool {
	switch contentType {
	case "image/jpeg", "image/pjpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	for _, d := range p.Decoders {
		if d.ContentType() == contentType {
			return true
		}
	}
	return false
}

func (p *Pipeline) decoderFor(data []byte) Decoder {
	for _, d := range p.Decoders {
		if d.Match(data) {
			return d
		}
	}
	return nil
}

// Dimensions are an image's width and height as it's shown: a photo a phone
// saved on its side, with an EXIF orientation of 5 to 8, has them swapped.
// The content type comes from the bytes, not the name.
func (p *Pipeline) Dimensions(data []byte) (width, height int, contentType string, err error) {
	var cfg image.Config
	if d := p.decoderFor(data); d != nil {
		cfg, err = d.DecodeConfig(bytes.NewReader(data))
		contentType = d.ContentType()
	} else {
		var format string
		cfg, format, err = image.DecodeConfig(bytes.NewReader(data))
		contentType = "image/" + format
	}
	if err != nil {
		return 0, 0, "", err
	}
	width, height = cfg.Width, cfg.Height
	if o := orientation(data); o >= 5 && o <= 8 {
		width, height = height, width
	}
	return width, height, contentType, nil
}

// Resize fits the image within [width, 2 × width] (Rails' resize_to_limit:
// it never enlarges), turns it upright, and encodes it at quality.
func (p *Pipeline) Resize(data []byte, width, quality int) ([]byte, error) {
	// The pipeline's formats decide, not what happens to be registered with
	// image.Decode: a decoder package can register itself just by being
	// imported anywhere in the program.
	if _, _, contentType, err := p.Dimensions(data); err != nil {
		return nil, err
	} else if !p.Resizable(contentType) {
		return nil, fmt.Errorf("images: this pipeline doesn't read %s", contentType)
	}
	var img image.Image
	var err error
	if d := p.decoderFor(data); d != nil {
		img, err = d.Decode(bytes.NewReader(data))
	} else {
		img, _, err = image.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, err
	}
	img = orient(img, orientation(data))
	b := img.Bounds()
	scale := math.Min(1, math.Min(float64(width)/float64(b.Dx()), float64(2*width)/float64(b.Dy())))
	w, h := max(1, int(math.Round(float64(b.Dx())*scale))), max(1, int(math.Round(float64(b.Dy())*scale)))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == b.Dx() && h == b.Dy() {
		draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	} else {
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	}
	var out bytes.Buffer
	if err := p.encoder().Encode(&out, dst, p.QualityOr(quality)); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ResizeFile resizes src into dst: what the child process runs.
func (p *Pipeline) ResizeFile(src, dst string, width, quality int) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	out, err := p.Resize(data, width, quality)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, out, 0o644)
}

// Copy is the path of key's copy at width and quality, made from src now if
// it isn't there yet. A copy that's there is served without waiting on any
// make; two callers asking for the same new one get one make between them.
func (p *Pipeline) Copy(ctx context.Context, key, src string, width, quality int) (string, error) {
	quality = p.QualityOr(quality)
	path := p.Path(key, width, quality)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	return path, p.make(ctx, src, path, width, quality)
}

// Has reports whether key's copy at width and quality is made.
func (p *Pipeline) Has(key string, width, quality int) bool {
	_, err := os.Stat(p.Path(key, width, p.QualityOr(quality)))
	return err == nil
}

// make writes the copy beside its place and renames it in, so a failed or
// interrupted make leaves nothing a request could serve.
func (p *Pipeline) make(ctx context.Context, src, path string, width, quality int) error {
	p.one.Lock()
	defer p.one.Unlock()
	if _, err := os.Stat(path); err == nil { // made while this waited
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".tmp-%dw-q%d-%d", width, quality, time.Now().UnixNano()))
	defer os.Remove(tmp)
	maker := p.Maker
	if maker == nil {
		maker = InProcess{Pipeline: p}
	}
	if err := maker.Make(ctx, src, tmp, width, quality); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// MakePlaceholder makes key's placeholder from src, if it isn't made yet.
func (p *Pipeline) MakePlaceholder(ctx context.Context, key, src string) error {
	width, quality := p.placeholderSize()
	_, err := p.Copy(ctx, key, src, width, quality)
	return err
}

// HasPlaceholder reports whether key's placeholder is made.
func (p *Pipeline) HasPlaceholder(key string) bool {
	width, quality := p.placeholderSize()
	return p.Has(key, width, quality)
}

// Placeholder is key's placeholder as a data URI (a few hundred bytes, to
// carry in the page), or "" when it isn't made yet: a page never waits for
// one.
func (p *Pipeline) Placeholder(key string) string {
	if uri, ok := p.placeholders.Load(key); ok {
		return uri.(string)
	}
	width, quality := p.placeholderSize()
	data, err := os.ReadFile(p.Path(key, width, quality))
	if err != nil {
		return ""
	}
	uri := "data:" + p.ContentType() + ";base64," + base64.StdEncoding.EncodeToString(data)
	p.placeholders.Store(key, uri)
	return uri
}

// Forget removes every copy of key, and its placeholder.
func (p *Pipeline) Forget(key string) error {
	p.placeholders.Delete(key)
	return os.RemoveAll(filepath.Join(p.Dir, key))
}

// Srcset lists a copy per width for a srcset attribute ("… 800w, … 1400w"),
// with url giving each copy's address.
func Srcset(widths []int, url func(width int) string) string {
	parts := make([]string, len(widths))
	for i, w := range widths {
		parts[i] = url(w) + " " + strconv.Itoa(w) + "w"
	}
	return strings.Join(parts, ", ")
}

// A Maker makes a resized copy of src at dst.
type Maker interface {
	Make(ctx context.Context, src, dst string, width, quality int) error
}

// InProcess resizes in this process: tests, and tools that don't serve.
type InProcess struct{ Pipeline *Pipeline }

func (m InProcess) Make(_ context.Context, src, dst string, width, quality int) error {
	p := m.Pipeline
	if p == nil {
		p = &Pipeline{}
	}
	return p.ResizeFile(src, dst, width, quality)
}

// ChildMemory is the Go memory limit a child runs with, so it collects
// early rather than growing to what it could use: with it a 24-megapixel
// photo resizes within a 384 MB container.
const ChildMemory = "160MiB"

// ChildCommand is the argument that makes the app's binary a resizing
// child: `<exe> resize SRC DST WIDTH QUALITY`.
const ChildCommand = "resize"

// Child resizes in a short-lived copy of this program. The app's main hands
// the arguments to RunChild before anything else:
//
//	if images.IsChild(os.Args) {
//		os.Exit(pipeline.RunChild(os.Args))
//	}
type Child struct{ Exe string }

func (c Child) Make(ctx context.Context, src, dst string, width, quality int) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Exe, ChildCommand, src, dst, strconv.Itoa(width), strconv.Itoa(quality))
	cmd.Env = append(os.Environ(), "GOMEMLIMIT="+ChildMemory)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("resize: %w: %s", err, out)
	}
	return nil
}

// IsChild reports whether the process was started as a resizing child.
func IsChild(args []string) bool { return len(args) > 1 && args[1] == ChildCommand }

// RunChild is the child's whole life: resize, report, exit code.
func (p *Pipeline) RunChild(args []string) int {
	if len(args) != 6 {
		fmt.Fprintln(os.Stderr, "usage: resize SRC DST WIDTH QUALITY")
		return 2
	}
	width, werr := strconv.Atoi(args[4])
	quality, qerr := strconv.Atoi(args[5])
	if werr != nil || qerr != nil {
		fmt.Fprintln(os.Stderr, "resize: WIDTH and QUALITY are numbers")
		return 2
	}
	if err := p.ResizeFile(args[2], args[3], width, quality); err != nil {
		fmt.Fprintln(os.Stderr, "resize:", err)
		return 1
	}
	return 0
}

// orientation is a JPEG's EXIF orientation, 1 (as stored) when it has none.
func orientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	r := bytes.NewReader(data[2:])
	for {
		var marker [2]byte
		if _, err := io.ReadFull(r, marker[:]); err != nil || marker[0] != 0xFF {
			return 1
		}
		if marker[1] == 0xDA || marker[1] == 0xD9 { // image data: no more headers
			return 1
		}
		var size uint16
		if err := binary.Read(r, binary.BigEndian, &size); err != nil || size < 2 {
			return 1
		}
		segment := make([]byte, size-2)
		if _, err := io.ReadFull(r, segment); err != nil {
			return 1
		}
		if marker[1] == 0xE1 && len(segment) > 14 && string(segment[:6]) == "Exif\x00\x00" {
			return exifOrientation(segment[6:])
		}
	}
}

func exifOrientation(tiff []byte) int {
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	ifd := int(order.Uint32(tiff[4:8]))
	if ifd+2 > len(tiff) {
		return 1
	}
	entries := int(order.Uint16(tiff[ifd : ifd+2]))
	for i := 0; i < entries; i++ {
		at := ifd + 2 + 12*i
		if at+12 > len(tiff) {
			return 1
		}
		if order.Uint16(tiff[at:at+2]) == 0x0112 {
			if o := int(order.Uint16(tiff[at+8 : at+10])); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}

// orient turns the stored pixels the way the EXIF orientation says.
func orient(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	outW, outH := w, h
	if o >= 5 {
		outW, outH = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
