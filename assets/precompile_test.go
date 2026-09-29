package assets

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const script = `// Says hello.
export function greet ( name ) {
	const greeting = "Hello, " + name + "!"
	return greeting
}
`

// built is fsys with what Precompile wrote into dir, as the build embeds it.
func built(t *testing.T, fsys fstest.MapFS, dir string) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	for k, v := range fsys {
		out[k] = v
	}
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			b, _ := os.ReadFile(path)
			out["built/"+filepath.ToSlash(rel)] = &fstest.MapFile{Data: b}
		}
		return err
	})
	return out
}

func served(t *testing.T, a *Assets, name string) string {
	t.Helper()
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, a.Path(name), nil))
	if w.Code != 200 {
		t.Fatalf("%s: %d", name, w.Code)
	}
	return w.Body.String()
}

// The build minifies each script; the app serves the minified one, under
// its own fingerprint, and the import map names it.
func TestPrecompileScripts(t *testing.T) {
	src := fstest.MapFS{"js/greet.js": {Data: []byte(script)}, "css/site.css": {Data: []byte("body{}")}}
	a, err := New(src)
	if err != nil {
		t.Fatal(err)
	}
	if got := served(t, a, "greet.js"); got != script {
		t.Errorf("before a build, served %q", got)
	}
	plain := a.Path("greet.js")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "js"), 0o755)
	os.WriteFile(filepath.Join(dir, "js", "0123456789abcdef.js"), []byte("old"), 0o644) // a gone source's
	if _, err := a.Precompile(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "js", "0123456789abcdef.js")); err == nil {
		t.Error("a gone source's minified copy kept")
	}
	b, err := New(built(t, src, dir))
	if err != nil {
		t.Fatal(err)
	}
	got := served(t, b, "greet.js")
	if len(got) >= len(script) || strings.Contains(got, "Says hello") || !strings.Contains(got, `"Hello, "`) || !strings.Contains(got, "export function greet") {
		t.Errorf("minified: %q", got)
	}
	if b.Path("greet.js") == plain {
		t.Error("the minified script kept the source's fingerprint")
	}
	if m := b.ImportMap(Pin("greet", "greet.js")); m.Imports()["greet"] != b.Path("greet.js") {
		t.Errorf("the import map: %v", m.Imports())
	}
	if len(b.byName) != 2 {
		t.Errorf("the build's files became assets: %d", len(b.byName))
	}

	// A script edited since the build is served as it is now.
	edited := built(t, src, dir)
	edited["js/greet.js"] = &fstest.MapFile{Data: []byte(script + "export const version = 2\n")}
	c, err := New(edited)
	if err != nil {
		t.Fatal(err)
	}
	if got := served(t, c, "greet.js"); !strings.Contains(got, "version = 2") || !strings.Contains(got, "Says hello") {
		t.Errorf("an edited script: %q", got)
	}
}

// With Images, the build makes the pictures' copies too, under images/.
func TestPrecompilePictures(t *testing.T) {
	a, im := pictures(t)
	dir := t.TempDir()
	made, err := a.Precompile(context.Background(), dir)
	if err != nil || made != 4+1 {
		t.Fatalf("made %d (%v)", made, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "images", im.byName["hero.png"].key, "160w-q80.webp")); err != nil {
		t.Error(err)
	}
	b, err := New(built(t, fstest.MapFS{
		"images/hero.png":     {Data: pngOf(t, 500, 300)},
		"images/maps/pin.png": {Data: pngOf(t, 100, 100)},
	}, dir))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Images(nil).Precompiled() {
		t.Error("the build's copies weren't found")
	}
}
