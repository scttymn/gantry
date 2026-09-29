// Package assets serves an app's stylesheets, scripts, fonts and images
// under names that carry a fingerprint of their content (site-1a2b3c4d.css),
// cached for a year: a changed file is a new name. Stylesheets' url()s are
// rewritten to the fingerprinted names, so a new font is a new stylesheet
// too, and stylesheets are minified. Text files are gzipped once, at start.
//
// An app embeds its assets folder and hands it to New:
//
//	assets/
//	  css/site.css         → Path("site.css")
//	  fonts/oswald.woff2   → Path("oswald.woff2")
//	  images/maps/a.png    → Path("maps/a.png")
//	  public/robots.txt    → served at /robots.txt
//
// A file is named by its path under its top folder, as Rails' Propshaft
// names them, so a stylesheet refers to a font as url("oswald.woff2").
// Files in public/ are served at the site's root instead, for the names
// browsers and crawlers ask for (robots.txt, app icons).
package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tdewolff/minify/v2"
	cssmin "github.com/tdewolff/minify/v2/css"

	"github.com/scttymn/gantry/compress"
)

// Year is how long fingerprinted files are cached: Rails' 1.year.
const Year = 31556952

type asset struct {
	body        []byte
	gzipped     []byte // text files, compressed once; nil otherwise
	digested    string // "site-1a2b3c4d.css"
	contentType string
}

// Assets is an app's files, digested.
type Assets struct {
	byName   map[string]*asset // logical name → asset
	byDigest map[string]*asset // digested name → asset
	public   map[string]*asset // root name → asset ("robots.txt")
}

var cssURL = regexp.MustCompile(`url\(\s*["']?([^"')]+?)["']?\s*\)`)

// New reads every file in fsys (see the package doc). Two files with the
// same name under different top folders are an error.
func New(fsys fs.FS) (*Assets, error) {
	a := &Assets{byName: map[string]*asset{}, byDigest: map[string]*asset{}, public: map[string]*asset{}}
	var css []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(path.Base(p), ".") {
			return err
		}
		top, name, ok := strings.Cut(p, "/")
		if !ok {
			return nil // a file at the root isn't an asset (assets.go, say)
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if top == "public" {
			a.public[name] = a.digest(name, body)
			return nil
		}
		if _, dup := a.byName[name]; dup {
			return fmt.Errorf("assets: two files named %s", name)
		}
		a.byName[name] = &asset{body: body}
		if path.Ext(name) == ".css" {
			css = append(css, name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for name, f := range a.byName {
		if path.Ext(name) != ".css" {
			*f = *a.digest(name, f.body)
		}
	}
	// Stylesheets last, once everything they point at has its name.
	sort.Strings(css)
	minifier := minify.New()
	minifier.AddFunc("text/css", cssmin.Minify)
	for _, name := range css {
		f := a.byName[name]
		body := cssURL.ReplaceAllFunc(f.body, func(m []byte) []byte {
			ref := string(cssURL.FindSubmatch(m)[1])
			if target, ok := a.byName[ref]; ok && target.digested != "" {
				return []byte(`url("/assets/` + target.digested + `")`)
			}
			return m
		})
		min, err := minifier.Bytes("text/css", body)
		if err != nil {
			return nil, fmt.Errorf("assets: minify %s: %w", name, err)
		}
		*f = *a.digest(name, min)
	}
	return a, nil
}

// MustNew is New for a package-level var: a broken asset stops the app at
// start.
func MustNew(fsys fs.FS) *Assets {
	a, err := New(fsys)
	if err != nil {
		panic(err)
	}
	return a
}

func (a *Assets) digest(name string, body []byte) *asset {
	sum := sha256.Sum256(body)
	ext := path.Ext(name)
	f := &asset{body: body, digested: strings.TrimSuffix(name, ext) + "-" + hex.EncodeToString(sum[:])[:8] + ext}
	f.contentType = mime.TypeByExtension(ext)
	if ext == ".woff2" {
		f.contentType = "font/woff2"
	}
	if compress.Compressible(f.contentType) {
		f.gzipped = compress.Bytes(body)
	}
	a.byDigest[f.digested] = f
	return f
}

// Path is an asset's URL, "/assets/site-1a2b3c4d.css". An unknown name is a
// programming error, so it panics: the page that uses it fails in its tests.
func (a *Assets) Path(name string) string {
	f, ok := a.byName[name]
	if !ok {
		panic("no asset " + name)
	}
	return "/assets/" + f.digested
}

// Read is an asset's contents (a stylesheet's after rewriting), to put
// inside a page.
func (a *Assets) Read(name string) []byte {
	f, ok := a.byName[name]
	if !ok {
		panic("no asset " + name)
	}
	return f.body
}

// PublicPath is a root file's URL with a version of its content:
// "/app-icon.png?v=1b4fbb56".
func (a *Assets) PublicPath(name string) string {
	f, ok := a.public[name]
	if !ok {
		panic("no public file " + name)
	}
	ext := path.Ext(name)
	return "/" + name + "?v=" + strings.TrimSuffix(strings.TrimPrefix(f.digested, strings.TrimSuffix(name, ext)+"-"), ext)
}

// Public is a root file's contents (public/404.html is "404.html"), and
// whether there's one: what the router reads its error pages from
// (web.Router.Public).
func (a *Assets) Public(name string) ([]byte, bool) {
	f, ok := a.public[name]
	if !ok {
		return nil, false
	}
	return f.body, true
}

// Handler serves /assets/<digested name>. Anything else is 404.
func (a *Assets) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := a.byDigest[strings.TrimPrefix(r.URL.Path, "/assets/")]
		if !ok || !strings.HasPrefix(r.URL.Path, "/assets/") {
			http.NotFound(w, r)
			return
		}
		serve(w, r, f)
	})
}

// PublicHandler serves the root files, cached for a year as Rails' public
// file server does: pages link them with ?v=.
func (a *Assets) PublicHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := a.public[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		serve(w, r, f)
	})
}

// PublicNames are the root files, for routing each one.
func (a *Assets) PublicNames() []string {
	var out []string
	for name := range a.public {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Routes is what an app mounts: /assets/, and each root file.
func (a *Assets) Routes(mount func(pattern string, h http.Handler)) {
	mount("GET /assets/", a.Handler())
	for _, name := range a.PublicNames() {
		mount("GET /"+name, a.PublicHandler())
	}
}

// serve writes a file: its gzipped bytes for a client that takes them, so a
// text file is compressed once rather than on every request.
func serve(w http.ResponseWriter, r *http.Request, f *asset) {
	h := w.Header()
	h.Set("Content-Type", f.contentType)
	h.Set("Cache-Control", "public, max-age="+strconv.Itoa(Year)+", immutable")
	body := f.body
	if f.gzipped != nil {
		h.Add("Vary", "Accept-Encoding")
		if compress.Accepts(r) && r.Header.Get("Range") == "" {
			h.Set("Content-Encoding", "gzip")
			body = f.gzipped
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(body)
}
