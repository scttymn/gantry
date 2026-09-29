package assets

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/scttymn/gantry/images"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pictures(t *testing.T) (*Assets, *Images) {
	t.Helper()
	a, err := New(fstest.MapFS{
		"images/hero.png":     {Data: pngOf(t, 500, 300)},
		"images/maps/pin.png": {Data: pngOf(t, 100, 100)},
		"images/logo.svg":     {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
		"css/site.css":        {Data: []byte(`body{color:red}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, a.Images(&images.Pipeline{Dir: t.TempDir()})
}

func draw(t *testing.T, im *Images, name string, o images.Img) string {
	t.Helper()
	var b strings.Builder
	if err := im.Img(name, o).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestImagesImg(t *testing.T) {
	_, im := pictures(t)
	tag := draw(t, im, "hero.png", images.Img{Alt: "A hero", Sizes: "100vw", Priority: true})
	// Each of the widths up to the picture's own (as uploaded photos'),
	// under its fingerprinted name.
	srcset := regexp.MustCompile(`srcset="([^"]+)"`).FindStringSubmatch(tag)
	want := regexp.MustCompile(`^/assets/resized/hero-[0-9a-f]{8}/160w-q80\.webp 160w, .*/240w-q80\.webp 240w, .*/320w-q80\.webp 320w, .*/480w-q80\.webp 480w$`)
	if srcset == nil || !want.MatchString(srcset[1]) {
		t.Errorf("srcset: %s", tag)
	}
	for _, part := range []string{`alt="A hero"`, `sizes="100vw"`, `width="500" height="300"`, `fetchpriority="high"`} {
		if !strings.Contains(tag, part) {
			t.Errorf("no %s: %s", part, tag)
		}
	}
	// A picture in a folder: its key has no slash, so the server can read it.
	if tag := draw(t, im, "maps/pin.png", images.Img{}); !strings.Contains(tag, "/assets/resized/maps--pin-") {
		t.Errorf("in a folder: %s", tag)
	}
	for _, name := range []string{"logo.svg", "site.css", "missing.png"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s drawn as a picture", name)
				}
			}()
			draw(t, im, name, images.Img{})
		}()
	}
}

// The build's copies: made once (a second build that changed no picture
// makes none), a picture that's gone takes its copies with it.
func TestImagesPrecompile(t *testing.T) {
	_, im := pictures(t)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "old-1a2b3c4d"), 0o755) // a picture since changed
	made, err := im.Precompile(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if made != 4+1 { // hero: 160 to 480; pin: 160 (at its own 100)
		t.Errorf("made %d", made)
	}
	if _, err := os.Stat(filepath.Join(dir, "old-1a2b3c4d")); err == nil {
		t.Error("a gone picture's copies kept")
	}
	hero := im.byName["hero.png"]
	copy480, err := os.ReadFile(filepath.Join(dir, hero.key, "480w-q80.webp"))
	if err != nil {
		t.Fatal(err)
	}
	if w, h, ct, err := im.pipeline.Dimensions(copy480); err != nil || w != 480 || h != 288 || ct != "image/webp" {
		t.Errorf("the 480 copy: %dx%d %s %v", w, h, ct, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, hero.key)); len(entries) != 4 {
		t.Errorf("%d files beside the copies", len(entries))
	}
	_, again := pictures(t)
	if made, err := again.Precompile(context.Background(), dir); err != nil || made != 0 {
		t.Errorf("second build made %d (%v)", made, err)
	}
}

// The build's copies, embedded, are served as they are, and nothing's made.
func TestImagesPrecompiled(t *testing.T) {
	_, im := pictures(t)
	dir := t.TempDir()
	if _, err := im.Precompile(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if im.Precompiled() {
		t.Error("precompiled before the copies were embedded")
	}
	fsys := fstest.MapFS{
		"images/hero.png":     {Data: pngOf(t, 500, 300)},
		"images/maps/pin.png": {Data: pngOf(t, 100, 100)},
	}
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			b, _ := os.ReadFile(path)
			fsys["resized/"+filepath.ToSlash(rel)] = &fstest.MapFile{Data: b}
		}
		return err
	})
	key := im.byName["hero.png"].key
	fsys["resized/"+key+"/160w-q80.webp"] = &fstest.MapFile{Data: []byte("the build's")}
	a, err := New(fsys)
	if err != nil {
		t.Fatal(err)
	}
	built := a.Images(&images.Pipeline{Dir: t.TempDir()})
	if !built.Precompiled() {
		t.Error("not precompiled")
	}
	if _, ok := a.byName[key+"/160w-q80.webp"]; ok || len(a.byName) != 2 {
		t.Errorf("a copy became an asset: %d assets", len(a.byName))
	}
	mux := http.NewServeMux()
	a.Routes(mux.Handle)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/assets/resized/"+key+"/160w-q80.webp", nil))
	if w.Code != 200 || w.Body.String() != "the build's" || w.Header().Get("Content-Type") != "image/webp" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("%d %q %v", w.Code, w.Body, w.Header())
	}
	if entries, _ := os.ReadDir(built.pipeline.Dir); len(entries) != 0 {
		t.Error("a copy was made on request")
	}
	// A copy of a picture that isn't in the assets isn't served.
	fsys["resized/stale-12345678/160w-q80.webp"] = &fstest.MapFile{Data: []byte("x")}
	a2, _ := New(fsys)
	a2.Images(nil)
	mux2 := http.NewServeMux()
	a2.Routes(mux2.Handle)
	w = httptest.NewRecorder()
	mux2.ServeHTTP(w, httptest.NewRequest("GET", "/assets/resized/stale-12345678/160w-q80.webp", nil))
	if w.Code != 404 {
		t.Errorf("a stale copy: %d", w.Code)
	}
}

func TestImagesServed(t *testing.T) {
	a, im := pictures(t)
	mux := http.NewServeMux()
	a.Routes(mux.Handle)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	key := im.byName["hero.png"].key
	// Asked for before Prepare: made then.
	w := get("/assets/resized/" + key + "/320w-q80.webp")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/webp" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	if wd, _, _, _ := im.pipeline.Dimensions(w.Body.Bytes()); wd != 320 {
		t.Errorf("width %d", wd)
	}
	for _, path := range []string{
		"/assets/resized/" + key + "/333w-q80.webp", // not one of the widths
		"/assets/resized/" + key + "/320w-q50.webp", // not the quality
		"/assets/resized/nope-12345678/320w-q80.webp",
		"/assets/resized/" + key + "/original",
	} {
		if w := get(path); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	// The assets themselves are served as ever.
	if w := get(a.Path("hero.png")); w.Code != 200 {
		t.Errorf("the original: %d", w.Code)
	}
}

// A changed picture is a new one: its key is its content's, so the next
// build makes its copies, and removes the old version's.
func TestImagesChanged(t *testing.T) {
	dir := t.TempDir()
	_, before := pictures(t)
	if _, err := before.Precompile(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	oldKey := before.byName["hero.png"].key
	a, err := New(fstest.MapFS{"images/hero.png": {Data: pngOf(t, 400, 200)}})
	if err != nil {
		t.Fatal(err)
	}
	after := a.Images(nil)
	newKey := after.byName["hero.png"].key
	if newKey == oldKey {
		t.Fatal("a changed picture kept its key")
	}
	made, err := after.Precompile(context.Background(), dir)
	if err != nil || made != 3 { // 160, 240 and 320: the widths up to its 400
		t.Errorf("made %d (%v)", made, err)
	}
	if _, err := os.Stat(filepath.Join(dir, newKey, "160w-q80.webp")); err != nil {
		t.Error("the new version has no copies")
	}
	for _, gone := range []string{oldKey, before.byName["maps/pin.png"].key} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s's copies kept", gone)
		}
	}
	if tag := draw(t, after, "hero.png", images.Img{}); !strings.Contains(tag, newKey) || strings.Contains(tag, oldKey) {
		t.Errorf("%s", tag)
	}
}
