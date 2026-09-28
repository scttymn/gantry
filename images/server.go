package images

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/a-h/templ"
)

// Server serves an app's copies at Prefix<key>/<name> (/photos/<key>/720w-q80.webp,
// and /photos/<key>/original for a type the pipeline can't resize), and
// writes the tags that ask for them. Pages and the server share it, so a
// page can only ask for a copy the server makes.
//
// It serves a copy only at one of the pipeline's widths and the current
// quality, of an original Find knows: anything else is not found, so an
// address can't make the server build files. A width above the original's
// own is served its largest copy.
type Server struct {
	Pipeline *Pipeline
	// Prefix is where it's mounted, with its slashes: "/photos/".
	Prefix string
	// Find is the original with this key, or ok false for a key that isn't
	// one the app shows.
	Find func(ctx context.Context, key string) (o Original, ok bool, err error)
	// Quality is the quality copies are made at now (an admin's setting,
	// say). The pipeline's when nil.
	Quality func(ctx context.Context) (int, error)
	// Log gets the copies that fail to make. slog's default when nil.
	Log *slog.Logger
}

// Original is an uploaded file the server makes copies of.
type Original struct {
	Path        string // on disk
	ContentType string
	Width       int // upright; 0 when unknown (every width is served)
}

// year is how long a copy is cached: its address changes with its width,
// quality and format, so it never goes stale.
const year = 31556952

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Server) quality(ctx context.Context) (int, error) {
	if s.Quality == nil {
		return s.Pipeline.QualityOr(0), nil
	}
	q, err := s.Quality(ctx)
	return s.Pipeline.QualityOr(q), err
}

// ServeHTTP serves a copy, or an original the pipeline can't resize.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, s.Prefix)
	key, name, cut := strings.Cut(rest, "/")
	if !ok || !cut || key == "" || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	o, ok, err := s.Find(ctx, key)
	if err != nil {
		s.log().Error("images: find", "key", key, "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	var path, contentType string
	if name == "original" && !s.Pipeline.Resizable(o.ContentType) {
		path, contentType = o.Path, o.ContentType
	} else {
		width, quality, ok := s.Pipeline.ParseName(name)
		if ok {
			width, ok = s.Pipeline.Fit(width, o.Width)
		}
		if !ok || !s.Pipeline.Resizable(o.ContentType) {
			http.NotFound(w, r)
			return
		}
		current, err := s.quality(ctx)
		if err != nil {
			s.log().Error("images: quality", "err", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if quality != current {
			http.NotFound(w, r)
			return
		}
		if path, err = s.Pipeline.Copy(ctx, key, o.Path, width, quality); err != nil {
			s.log().Error("images: copy", "key", key, "width", width, "err", err)
			http.NotFound(w, r)
			return
		}
		contentType = s.Pipeline.ContentType()
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(year)+", immutable")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// URL is a copy's address.
func (s *Server) URL(key string, width, quality int) string {
	return s.Prefix + key + "/" + s.Pipeline.Name(width, s.Pipeline.QualityOr(quality))
}

// OriginalURL is the address of an original the pipeline can't resize.
func (s *Server) OriginalURL(key string) string { return s.Prefix + key + "/original" }

// Photo is an original as a page shows it.
type Photo struct {
	Key, ContentType string
	Width, Height    int    // the original's, upright; 0 when unknown
	Quality          int    // the copies'; the pipeline's when 0
	Placeholder      string // a data URI (Pipeline.Placeholder); "" when there's none
}

// Img is how a page shows a photo.
type Img struct {
	Alt string
	// Sizes is how wide the photo draws, as a srcset's sizes ("(max-width:
	// 640px) 100vw, 50vw"). The browser picks the copy that fits from the
	// widths the photo comes in.
	Sizes string
	// Priority is for the photo on screen when the page opens (a hero):
	// fetched first. Every other loads as the visitor scrolls toward it.
	Priority bool
	// PlaceholderWhen is a media query ("(max-width: 640px)") where the
	// page shows only the placeholder and downloads no copy: for a photo
	// that's just a faded background there. With no placeholder yet, it
	// shows a transparent pixel.
	PlaceholderWhen string
}

// transparent is a 1×1 GIF.
const transparent = "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"

// Sources are a resizable photo's src (its largest copy, for a browser that
// ignores srcset) and srcset (every width it comes in).
func (s *Server) Sources(p Photo) (src, srcset string) {
	widths := s.Pipeline.WidthsFor(p.Width)
	url := func(w int) string { return s.URL(p.Key, w, p.Quality) }
	return url(widths[len(widths)-1]), Srcset(widths, url)
}

// Img is the photo's tag: an <img> offering every width it comes in, with
// its size when it's known (so the page doesn't shift as it loads), or the
// original for a type the pipeline can't resize.
func (s *Server) Img(p Photo, o Img) templ.Component {
	return templ.Raw(s.imgHTML(p, o))
}

func (s *Server) imgHTML(p Photo, o Img) string {
	esc := templ.EscapeString[string]
	loading := `loading="lazy"`
	if o.Priority {
		loading = `loading="eager" fetchpriority="high"`
	}
	if !s.Pipeline.Resizable(p.ContentType) {
		return fmt.Sprintf(`<img alt="%s" %s src="%s">`, esc(o.Alt), loading, esc(s.OriginalURL(p.Key)))
	}
	src, srcset := s.Sources(p)
	dimensions := ""
	if p.Width > 0 && p.Height > 0 {
		dimensions = fmt.Sprintf(` width="%d" height="%d"`, p.Width, p.Height)
	}
	img := fmt.Sprintf(`<img alt="%s" sizes="%s" %s%s srcset="%s" src="%s">`,
		esc(o.Alt), esc(o.Sizes), loading, dimensions, esc(srcset), esc(src))
	if o.PlaceholderWhen == "" {
		return img
	}
	placeholder := p.Placeholder
	if placeholder == "" {
		placeholder = transparent
	}
	return `<picture><source media="` + esc(o.PlaceholderWhen) + `" srcset="` + esc(placeholder) + `">` + img + `</picture>`
}

// Blurred is a photo's placeholder as a CSS image, for a background under
// the photo while it loads: the tiny copy inside an SVG that blurs it, so
// it stays smooth stretched to any size (a bare one shows its pixels). The
// alpha step keeps the blur from fading at the edges. "" when the photo has
// no placeholder.
func (s *Server) Blurred(p Photo) string {
	if p.Placeholder == "" {
		return ""
	}
	w, _ := s.Pipeline.placeholderSize()
	h := w
	if p.Width > 0 && p.Height > 0 {
		h = max(1, int(math.Round(float64(w)*float64(p.Height)/float64(p.Width))))
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d">`+
		`<filter id="b" color-interpolation-filters="sRGB"><feGaussianBlur stdDeviation="1.2"/>`+
		`<feComponentTransfer><feFuncA type="discrete" tableValues="1 1"/></feComponentTransfer></filter>`+
		`<image width="%d" height="%d" preserveAspectRatio="none" filter="url(#b)" href="%s"/></svg>`, w, h, w, h, p.Placeholder)
	return `url("data:image/svg+xml,` + url.PathEscape(svg) + `")`
}
