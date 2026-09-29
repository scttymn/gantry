package assets

import (
	"context"
	"fmt"
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
// page may want, as WebP: the images package's copies, which uploaded photos
// get too, for the files shipped with the app. A page draws one with Img,
// an <img> whose srcset offers each width, so a phone fetches a phone's
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
	a        *Assets
	pipeline *images.Pipeline
	server   *images.Server
	byName   map[string]*picture
	byKey    map[string]*picture
}

type picture struct {
	name, key, ext, contentType string
	body                        []byte
	width, height               int
}

// ImagesPrefix is where the copies are served: /assets/resized/<key>/720w-q80.webp.
const ImagesPrefix = "/assets/resized/"

// Images is a's pictures at every width, made by p (WebP at quality 80, at
// images.Widths, when nil). Routes serves them once it's called.
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
	im.server = &images.Server{Pipeline: p, Prefix: ImagesPrefix, Find: im.find}
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
	q := im.pipeline
	p := &images.Pipeline{Dir: dir, Encoder: q.Encoder, Widths: q.Widths, Quality: q.Quality, Decoders: q.Decoders, Maker: q.Maker}
	originals := map[string]images.Original{}
	for key, pic := range im.byKey {
		path, err := im.original(pic)
		if err != nil {
			return 0, err
		}
		originals[key] = images.Original{Path: path, ContentType: pic.contentType, Width: pic.width}
	}
	return p.Prepare(ctx, originals, 0)
}

// Precompiled reports whether the build made every picture's copies, so no
// request makes one.
func (im *Images) Precompiled() bool {
	for key, pic := range im.byKey {
		for _, w := range im.pipeline.WidthsFor(pic.width) {
			if _, ok := im.a.copies[key][im.pipeline.Name(w, im.pipeline.QualityOr(0))]; !ok {
				return false
			}
		}
	}
	return true
}

// ServeHTTP serves a copy: the build's, from memory, else one made on
// request.
func (im *Images) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key, name, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, ImagesPrefix), "/")
	if body, ok := im.a.copies[key][name]; ok && im.byKey[key] != nil {
		w.Header().Set("Content-Type", im.pipeline.ContentType())
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(Year)+", immutable")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method != http.MethodHead {
			w.Write(body)
		}
		return
	}
	im.server.ServeHTTP(w, r)
}

// Img is the picture's <img>: a srcset of its copies, the browser picking by
// o.Sizes, with its width and height so the page doesn't shift as it loads.
// An unknown name is a programming error, so it panics: the page that uses
// it fails in its tests.
func (im *Images) Img(name string, o images.Img) templ.Component {
	pic, ok := im.byName[name]
	if !ok {
		panic("no picture " + name + " in the assets")
	}
	return im.server.Img(images.Photo{Key: pic.key, ContentType: pic.contentType, Width: pic.width, Height: pic.height}, o)
}
