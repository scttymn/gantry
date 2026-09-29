package assets

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"github.com/scttymn/gantry/images"
)

// Images is the assets' pictures (JPEG, PNG, GIF, WebP) at every width a
// page may want, as AVIF and WebP: the images package's copies, which
// uploaded photos get too, for the files shipped with the app. A page draws
// one with Img, a <picture> offering each width in AVIF (about half WebP's
// size) and, for a browser without it, WebP, so a phone fetches a phone's
// copy:
//
//	var Images = All.Images(nil)
//	@assets.Images.Img("hero.jpg", images.Img{Alt: "…", Sizes: "100vw", Priority: true})
//
// The build makes the copies, as Rails' assets:precompile does: the app's
// `assets` command runs Assets.Precompile into assets/built/ before go
// build, which embeds them with the rest, so the server serves them from memory
// and never runs the encoder. Precompile makes only what's missing, and a
// changed picture is a new one (its key is its fingerprint). Without the
// build's copies (in development, in tests), a copy is made when it's first
// asked for.
type Images struct {
	a          *Assets
	pipeline   *images.Pipeline // WebP, what every browser takes
	avif       *images.Pipeline // AVIF, offered first
	server     *images.Server
	avifServer *images.Server
	byName     map[string]*picture
	byKey      map[string]*picture
}

type picture struct {
	name, key, ext, contentType string
	body                        []byte
	width, height               int
}

// avifSlow is images.AVIFSlow; tests set it.
var avifSlow = images.AVIFSlow

// withAVIF reports whether a picture comes in AVIF: its AVIF copies are in
// the build, or, with nothing built for it (development, tests), this
// machine makes AVIF at a compiled speed. A build on a machine that
// doesn't (Precompile skips AVIF there) leaves the picture WebP alone, so
// no request waits minutes for a copy.
func (im *Images) withAVIF(key string) bool {
	built := im.a.copies[key]
	for name := range built {
		if strings.HasSuffix(name, im.avif.Encoder.Ext()) {
			return true
		}
	}
	return len(built) == 0 && avifSlow() == ""
}

// ImagesPrefix is where the copies are served: /assets/resized/<key>/720w-q80.webp.
const ImagesPrefix = "/assets/resized/"

// Images is a's pictures at every width, the WebP copies made by p (quality
// 80, at images.Widths, when nil) and the AVIF ones at p's widths and
// images.AVIFQuality. Routes serves them once it's called.
func (a *Assets) Images(p *images.Pipeline) *Images {
	if p == nil {
		p = &images.Pipeline{}
	}
	if p.Dir == "" {
		// Copies made on request, without the build's: shared by the
		// processes on this machine (tests), each written whole and renamed in.
		p.Dir = filepath.Join(os.TempDir(), "gantry-asset-images")
	}
	im := &Images{a: a, pipeline: p, byName: map[string]*picture{}, byKey: map[string]*picture{}}
	for name, f := range a.byName {
		width, height, contentType, err := p.Dimensions(f.body)
		if err != nil || !p.Resizable(contentType) {
			continue // not a picture (a stylesheet, an SVG)
		}
		ext := path.Ext(f.digested)
		key := strings.ReplaceAll(strings.TrimSuffix(f.digested, ext), "/", "--")
		pic := &picture{name: name, key: key, ext: ext, contentType: contentType, body: f.body, width: width, height: height}
		im.byName[name], im.byKey[key] = pic, pic
	}
	im.avif = &images.Pipeline{Dir: p.Dir, Encoder: images.AVIF{}, Quality: images.AVIFQuality, Widths: p.Widths, Decoders: p.Decoders, Maker: p.Maker}
	im.server = &images.Server{Pipeline: p, Prefix: ImagesPrefix, Find: im.find}
	im.avifServer = &images.Server{Pipeline: im.avif, Prefix: ImagesPrefix, Find: im.find}
	a.images = im
	return im
}

// original is where the picture's own bytes are written for the pipeline,
// which reads files: beside the on-request copies, never among the build's.
func (im *Images) original(pic *picture) (string, error) {
	path := filepath.Join(os.TempDir(), "gantry-asset-originals", pic.key+pic.ext)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp := fmt.Sprintf("%s.tmp-%d", path, os.Getpid())
	if err := os.WriteFile(tmp, pic.body, 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

func (im *Images) find(_ context.Context, key string) (images.Original, bool, error) {
	pic, ok := im.byKey[key]
	if !ok {
		return images.Original{}, false, nil
	}
	path, err := im.original(pic)
	return images.Original{Path: path, ContentType: pic.contentType, Width: pic.width}, err == nil, err
}

// Precompile writes every picture's copies into dir (Assets.Precompile's
// images/, for the build to embed), making only those missing, and removes the copies
// of pictures no longer in the assets: Pipeline.Prepare. made is how many
// it made.
func (im *Images) Precompile(ctx context.Context, dir string) (made int, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	originals := map[string]images.Original{}
	for key, pic := range im.byKey {
		path, err := im.original(pic)
		if err != nil {
			return 0, err
		}
		originals[key] = images.Original{Path: path, ContentType: pic.contentType, Width: pic.width}
	}
	formats := []*images.Pipeline{im.pipeline, im.avif}
	if avifSlow() != "" {
		formats = formats[:1] // WebP alone: see images.AVIFSlow
	}
	for _, q := range formats {
		p := &images.Pipeline{Dir: dir, Encoder: q.Encoder, Widths: q.Widths, Quality: q.Quality, Decoders: q.Decoders, Maker: q.Maker}
		n, err := p.Prepare(ctx, originals, 0)
		made += n
		if err != nil {
			return made, err
		}
	}
	return made, nil
}

// Precompiled reports whether the build made every picture's copies, so no
// request makes one.
func (im *Images) Precompiled() bool {
	formats := []*images.Pipeline{im.pipeline, im.avif}
	if avifSlow() != "" {
		formats = formats[:1]
	}
	for key, pic := range im.byKey {
		for _, p := range formats {
			for _, w := range p.WidthsFor(pic.width) {
				if _, ok := im.a.copies[key][p.Name(w, p.QualityOr(0))]; !ok {
					return false
				}
			}
		}
	}
	return true
}

// ServeHTTP serves a copy: the build's, from memory, else one made on
// request.
func (im *Images) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key, name, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, ImagesPrefix), "/")
	p, server := im.pipeline, im.server
	if strings.HasSuffix(name, im.avif.Encoder.Ext()) {
		p, server = im.avif, im.avifServer
		if _, built := im.a.copies[key][name]; !built && !im.withAVIF(key) {
			http.NotFound(w, r)
			return
		}
	}
	if body, ok := im.a.copies[key][name]; ok && im.byKey[key] != nil {
		w.Header().Set("Content-Type", p.ContentType())
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(Year)+", immutable")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method != http.MethodHead {
			w.Write(body)
		}
		return
	}
	server.ServeHTTP(w, r)
}

// Img is the picture's <picture>: its AVIF copies (when it comes in AVIF:
// withAVIF), then an <img> of its WebP ones for a browser without AVIF, the browser picking by o.Sizes, and
// the <img> sized so the page doesn't shift as it loads. (A picture with
// o.PlaceholderWhen is its WebP <img> alone, which is a <picture> already.)
// An unknown name is a programming error, so it panics: the page that uses
// it fails in its tests.
func (im *Images) Img(name string, o images.Img) templ.Component {
	pic, ok := im.byName[name]
	if !ok {
		panic("no picture " + name + " in the assets")
	}
	photo := images.Photo{Key: pic.key, ContentType: pic.contentType, Width: pic.width, Height: pic.height}
	img := im.server.Img(photo, o)
	if o.PlaceholderWhen != "" || !im.withAVIF(pic.key) {
		return img
	}
	_, srcset := im.avifServer.Sources(photo)
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		esc := templ.EscapeString[string]
		if _, err := io.WriteString(w, `<picture><source type="image/avif" sizes="`+esc(o.Sizes)+`" srcset="`+esc(srcset)+`">`); err != nil {
			return err
		}
		if err := img.Render(ctx, w); err != nil {
			return err
		}
		_, err := io.WriteString(w, "</picture>")
		return err
	})
}
