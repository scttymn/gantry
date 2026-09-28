package assets

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/scttymn/gantry/compress"
)

var files = fstest.MapFS{
	"assets.go":                    {Data: []byte("package assets")},
	"css/site.css":                 {Data: []byte("/* the site */\nbody {\n  color: red;\n  font-family: Oswald;\n}\n\n.logo { background: url('logo.svg'); }\n")},
	"css/fonts.css":                {Data: []byte("@font-face {\n  font-family: Oswald;\n  src: url(\"oswald.woff2\") format('woff2');\n}\n.x { background: url(https://example.com/a.png); }\n")},
	"fonts/oswald.woff2":           {Data: []byte("wOF2 not really")},
	"images/logo.svg":              {Data: []byte("<svg/>")},
	"images/maps/apple.png":        {Data: []byte("\x89PNG")},
	"js/site.js":                   {Data: []byte("console.log('hi')\n" + strings.Repeat("// padding\n", 50))},
	"public/robots.txt":            {Data: []byte("User-agent: *\n")},
	"public/app-icon.png":          {Data: []byte("\x89PNG icon")},
	"images/.DS_Store":             {Data: []byte("junk")},
	"css/.hidden.css":              {Data: []byte("x{}")},
	"public/nested/not-served.txt": {Data: []byte("x")},
}

func get(t *testing.T, h http.Handler, path string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAssets(t *testing.T) {
	a, err := New(files)
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()

	t.Run("a digested name is served for a year", func(t *testing.T) {
		p := a.Path("site.css")
		if !regexp.MustCompile(`\A/assets/site-[0-9a-f]{8}\.css\z`).MatchString(p) {
			t.Fatalf("path %s", p)
		}
		rec := get(t, h, p)
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=31556952, immutable" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
			t.Errorf("%d %v", rec.Code, rec.Header())
		}
		font := get(t, h, a.Path("oswald.woff2"))
		if font.Code != 200 || font.Header().Get("Content-Type") != "font/woff2" {
			t.Errorf("font: %d %s", font.Code, font.Header().Get("Content-Type"))
		}
		if get(t, h, a.Path("maps/apple.png")).Code != 200 {
			t.Error("a nested image")
		}
	})

	t.Run("a wrong digest or unknown name is 404", func(t *testing.T) {
		right := a.Path("site.css")
		for _, p := range []string{
			regexp.MustCompile(`-[0-9a-f]{8}\.`).ReplaceAllString(right, "-00000000."),
			"/assets/site.css",
			"/assets/nothing-12345678.css",
			"/assets/",
			"/assets/../assets/" + strings.TrimPrefix(right, "/assets/") + "x",
		} {
			if rec := get(t, h, p); rec.Code != 404 {
				t.Errorf("%s: %d", p, rec.Code)
			}
		}
	})

	t.Run("hidden files aren't assets", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("a hidden file got a path")
			}
		}()
		a.Path(".DS_Store")
	})

	t.Run("text files are compressed once, at startup, and served as those bytes to a client that takes gzip", func(t *testing.T) {
		for _, name := range []string{"site.js", "site.css"} {
			first := get(t, h, a.Path(name), "Accept-Encoding", "gzip, br")
			hd := first.Header()
			if hd.Get("Content-Encoding") != "gzip" || hd.Get("Content-Length") != strconv.Itoa(first.Body.Len()) || !strings.Contains(hd.Get("Vary"), "Accept-Encoding") {
				t.Errorf("%s: %v", name, hd)
				continue
			}
			r, err := gzip.NewReader(first.Body)
			if err != nil {
				t.Fatal(err)
			}
			if body, _ := io.ReadAll(r); !bytes.Equal(body, a.Read(name)) {
				t.Errorf("%s: unpacks to %d bytes, the file is %d", name, len(body), len(a.Read(name)))
			}
			if again := get(t, h, a.Path(name), "Accept-Encoding", "gzip"); !bytes.Equal(again.Body.Bytes(), compress.Bytes(a.Read(name))) {
				t.Errorf("%s: not the bytes made once at best compression", name)
			}
			if plain := get(t, h, a.Path(name)); plain.Header().Get("Content-Encoding") != "" || !bytes.Equal(plain.Body.Bytes(), a.Read(name)) {
				t.Errorf("%s: a client without gzip gets the file as it is", name)
			}
		}
		if rec := get(t, h, a.Path("oswald.woff2"), "Accept-Encoding", "gzip"); rec.Header().Get("Content-Encoding") != "" {
			t.Error("a font is compressed already")
		}
	})

	t.Run("stylesheets are served minified, and keep every rule", func(t *testing.T) {
		css := string(a.Read("site.css"))
		if strings.Contains(css, "/*") || strings.Contains(css, "\n") || !strings.Contains(css, "body{color:red;font-family:Oswald}") {
			t.Errorf("%q", css)
		}
	})

	t.Run("stylesheet urls point at digested files; others are left alone", func(t *testing.T) {
		css := string(a.Read("fonts.css"))
		if !strings.Contains(css, "url("+a.Path("oswald.woff2")+")") || !strings.Contains(string(a.Read("site.css")), a.Path("logo.svg")) {
			t.Errorf("%q", css)
		}
		if !strings.Contains(css, "https://example.com/a.png") {
			t.Error("an outside url was changed")
		}
		// The stylesheet's own fingerprint covers the rewritten fonts.
		sum := sha256.Sum256(a.Read("fonts.css"))
		if !strings.Contains(a.Path("fonts.css"), hex.EncodeToString(sum[:])[:8]) {
			t.Error("fonts.css's digest isn't of its rewritten content")
		}
	})

	t.Run("root files carry a version of their content", func(t *testing.T) {
		if got := a.PublicPath("app-icon.png"); !regexp.MustCompile(`\A/app-icon\.png\?v=[0-9a-f]{8}\z`).MatchString(got) {
			t.Errorf("%s", got)
		}
		body := get(t, a.PublicHandler(), "/app-icon.png").Body.Bytes()
		sum := sha256.Sum256(body)
		if !strings.HasSuffix(a.PublicPath("app-icon.png"), hex.EncodeToString(sum[:])[:8]) {
			t.Error("the version isn't of the file")
		}
		if get(t, a.PublicHandler(), "/nope.png").Code != 404 {
			t.Error("an unknown root file")
		}
		if got := strings.Join(a.PublicNames(), ","); got != "app-icon.png,nested/not-served.txt,robots.txt" {
			t.Errorf("names %s", got)
		}
	})

	t.Run("routes mount /assets/ and each root file", func(t *testing.T) {
		mux := http.NewServeMux()
		a.Routes(mux.Handle)
		if get(t, mux, a.Path("site.js")).Code != 200 || get(t, mux, "/robots.txt").Code != 200 {
			t.Error("not mounted")
		}
	})

	t.Run("stylesheets bundle in order, inline when small", func(t *testing.T) {
		st := a.Styles("fonts.css", "site.css")
		want := string(a.Read("fonts.css")) + string(a.Read("site.css"))
		var b strings.Builder
		st.Tag().Render(context.Background(), &b)
		if !st.Inline() || b.String() != "<style>"+want+"</style>" {
			t.Fatalf("%v %q", st.Inline(), b.String())
		}
		sum := sha256.Sum256([]byte(want))
		if st.Hash() != "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'" {
			t.Errorf("hash %s", st.Hash())
		}
		if rec := get(t, h, st.Path()); rec.Code != 200 || rec.Body.String() != want || rec.Header().Get("Cache-Control") != "public, max-age=31556952, immutable" {
			t.Errorf("the bundle isn't served: %d", rec.Code)
		}
	})

	t.Run("a bundle too big to inline is linked", func(t *testing.T) {
		big, err := New(fstest.MapFS{"css/big.css": {Data: []byte(randomCSS(40 << 10))}})
		if err != nil {
			t.Fatal(err)
		}
		st := big.Styles("big.css")
		var b strings.Builder
		st.Tag().Render(context.Background(), &b)
		if st.Inline() || b.String() != `<link rel="stylesheet" href="`+st.Path()+`">` {
			t.Fatalf("%v %q", st.Inline(), b.String())
		}
		if !a.InlineStyles("site.css").Inline() || a.LinkStyles("site.css").Inline() {
			t.Error("the forced ones")
		}
	})

	t.Run("two files with the same name are an error", func(t *testing.T) {
		if _, err := New(fstest.MapFS{"css/a.css": {Data: []byte("x{}")}, "other/a.css": {Data: []byte("y{}")}}); err == nil {
			t.Error("no error")
		}
	})
}

// randomCSS is n bytes of rules that don't compress, so a bundle of it is
// over the inline limit even gzipped.
func randomCSS(n int) string {
	var b strings.Builder
	seed := uint64(1)
	for b.Len() < n {
		seed = seed*6364136223846793005 + 1442695040888963407
		fmt.Fprintf(&b, ".c%x{color:#%06x}", seed>>20, seed>>40&0xffffff)
	}
	return b.String()
}
