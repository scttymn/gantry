package assets

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"strings"

	"github.com/a-h/templ"

	"github.com/scttymn/gantry/compress"
)

// InlineLimit is how small a bundle of stylesheets must be, gzipped, for
// Styles to draw it into the page rather than link it. Under it, the page
// paints a round trip sooner and the extra bytes are few; over it, a linked
// file cached for a year wins (docs/plans/gantry.md, "Inlined or linked
// stylesheets").
const InlineLimit = 10 << 10

// Styles are stylesheets bundled into one, in the order given: written as
// separate files, joined once at start. A page draws them with Tag.
type Styles struct {
	body   []byte
	inline bool
	path   string // the bundle's URL when it's linked
	hash   string // "sha256-…", for a Content-Security-Policy
}

// Styles bundles the named stylesheets, to be drawn into the page when the
// bundle is under InlineLimit gzipped, and linked otherwise. Declare it once,
// beside the assets:
//
//	var Styles = All.Styles("application.css", "site.css")
//
// An unknown name is a programming error, so it panics at start.
func (a *Assets) Styles(names ...string) *Styles {
	s := a.bundle(names)
	s.inline = len(compress.Bytes(s.body)) <= InlineLimit
	return s
}

// InlineStyles always draws the bundle into the page.
func (a *Assets) InlineStyles(names ...string) *Styles {
	s := a.bundle(names)
	s.inline = true
	return s
}

// LinkStyles always links the bundle.
func (a *Assets) LinkStyles(names ...string) *Styles {
	s := a.bundle(names)
	s.inline = false
	return s
}

func (a *Assets) bundle(names []string) *Styles {
	var b strings.Builder
	for _, name := range names {
		b.Write(a.Read(name))
	}
	s := &Styles{body: []byte(b.String())}
	sum := sha256.Sum256(s.body)
	s.hash = "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	// Served too, whichever way it's drawn: a linked bundle needs it, and an
	// inlined one costs only its bytes in memory.
	f := a.digest("styles.css", s.body)
	f.digested = "styles-" + hex.EncodeToString(sum[:])[:8] + ".css"
	a.byDigest[f.digested] = f
	s.path = "/assets/" + f.digested
	return s
}

// Tag is the bundle for a page's head: a <style> with it when it's inline,
// else a <link> to it.
func (s *Styles) Tag() templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		var err error
		if s.inline {
			_, err = io.WriteString(w, "<style>"+string(s.body)+"</style>")
		} else {
			_, err = io.WriteString(w, `<link rel="stylesheet" href="`+s.path+`">`)
		}
		return err
	})
}

// Inline reports whether Tag draws the bundle into the page.
func (s *Styles) Inline() bool { return s.inline }

// Path is the bundle's URL, served whichever way it's drawn.
func (s *Styles) Path() string { return s.path }

// Hash is the bundle's hash for a Content-Security-Policy's style-src
// ('sha256-…', quoted), which lets the inlined <style> in and nothing else
// inline.
func (s *Styles) Hash() string { return "'" + s.hash + "'" }
