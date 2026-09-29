package assets

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
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
// Copies are kept on disk by the image's content, so they're made once:
// Prepare, at start, makes the ones not made yet (a deploy that changed no
// image makes none) and removes those of images that are gone. A copy asked
// for before Prepare made it is made then.
type Images struct {
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
// images.Widths, when nil). Routes serves them once it's called. A picture
// that doesn't decode is a broken asset, so it panics at start.
func (a *Assets) Images(p *images.Pipeline) *Images {
	if p == nil {
		p = &images.Pipeline{}
	}
	if p.Dir == "" {
		// Until Prepare names the app's own: shared by the processes on this
		// machine (tests), each copy written whole and renamed in.
		p.Dir = filepath.Join(os.TempDir(), "gantry-asset-images")
	}
	im := &Images{pipeline: p, byName: map[string]*picture{}, byKey: map[string]*picture{}}
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
// which reads files.
func (im *Images) original(pic *picture) (string, error) {
	path := filepath.Join(im.pipeline.Dir, pic.key, "original"+pic.ext)
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

// Prepare keeps the copies in dir (the app's data volume, so they outlast a
// deploy) and has the pipeline make those not made yet (Pipeline.Prepare):
// a picture unchanged since the last start has its copies, and a changed
// one is a new key. Copies of pictures no longer in the assets are removed.
// Call it before serving; dir is the copies' alone. made is how many it
// made.
func (im *Images) Prepare(ctx context.Context, dir string) (made int, err error) {
	im.pipeline.Dir = dir
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
	return im.pipeline.Prepare(ctx, originals, 0)
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
