package images

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func newServer(t *testing.T, quality int) (*Server, string) {
	t.Helper()
	src := source(t) // 1600×800 JPEG
	s := &Server{
		Pipeline: &Pipeline{Dir: t.TempDir()},
		Prefix:   "/photos/",
		Find: func(_ context.Context, key string) (Original, bool, error) {
			switch key {
			case "k":
				return Original{Path: src, ContentType: "image/jpeg", Width: 1600}, true, nil
			case "tiff":
				return Original{Path: src, ContentType: "image/tiff"}, true, nil
			case "broken":
				return Original{}, false, errors.New("the database is down")
			}
			return Original{}, false, nil
		},
		Quality: func(context.Context) (int, error) { return quality, nil },
		Log:     slog.New(slog.DiscardHandler),
	}
	return s, src
}

func get(s *Server, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestServer(t *testing.T) {
	t.Run("serves every URL its tags write, cached for a year", func(t *testing.T) {
		s, _ := newServer(t, 70)
		src, srcset := s.Sources(Photo{Key: "k", ContentType: "image/jpeg", Width: 1600, Quality: 70})
		urls := []string{src}
		for part := range strings.SplitSeq(srcset, ", ") {
			urls = append(urls, strings.Fields(part)[0])
		}
		if len(urls) != 10 { // src, and 160 to 1600
			t.Fatal(urls)
		}
		for _, u := range urls {
			rec := get(s, u)
			if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/webp" || rec.Header().Get("Cache-Control") != "public, max-age=31556952, immutable" {
				t.Errorf("%s: %d %v", u, rec.Code, rec.Header())
			}
		}
	})

	t.Run("a width above the original's gets its largest copy, made no wider", func(t *testing.T) {
		s, _ := newServer(t, 80)
		largest := get(s, "/photos/k/1600w-q80.webp").Body.Bytes()
		rec := get(s, "/photos/k/2400w-q80.webp")
		if rec.Code != 200 || string(rec.Body.Bytes()) != string(largest) {
			t.Errorf("%d, %d bytes", rec.Code, rec.Body.Len())
		}
		if s.Pipeline.Has("k", 2400, 80) {
			t.Error("made a 2400 copy")
		}
		if w := decodeWebP(t, largest).Bounds().Dx(); w != 1600 {
			t.Errorf("%d wide", w)
		}
	})

	t.Run("nothing else is served, so an address can't make it build files", func(t *testing.T) {
		s, _ := newServer(t, 80)
		for _, p := range []string{
			"/photos/k/800w-q80.webp",  // not one of the widths
			"/photos/k/720w-q60.webp",  // not the current quality
			"/photos/k/720w-q080.webp", // written oddly
			"/photos/k/720w-q80.jpg",   // another format
			"/photos/k/original",       // resizable: only copies
			"/photos/nope/720w-q80.webp",
			"/photos/k/../k/720w-q80.webp",
			"/photos/k/720w-q80.webp/x",
			"/photos//720w-q80.webp",
			"/photos/k",
			"/elsewhere/k/720w-q80.webp",
		} {
			if rec := get(s, p); rec.Code != 404 {
				t.Errorf("%s: %d", p, rec.Code)
			}
		}
		if rec := get(s, "/photos/broken/720w-q80.webp"); rec.Code != 500 {
			t.Errorf("a failed lookup: %d", rec.Code)
		}
	})

	t.Run("a type it can't resize is served as it is", func(t *testing.T) {
		s, _ := newServer(t, 80)
		rec := get(s, "/photos/tiff/original")
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/tiff" {
			t.Errorf("%d %s", rec.Code, rec.Header().Get("Content-Type"))
		}
		if rec := get(s, "/photos/tiff/720w-q80.webp"); rec.Code != 404 {
			t.Errorf("a copy of it: %d", rec.Code)
		}
	})

	t.Run("without a Quality, the pipeline's", func(t *testing.T) {
		s, _ := newServer(t, 0)
		s.Quality = nil
		if rec := get(s, "/photos/k/160w-q80.webp"); rec.Code != 200 {
			t.Errorf("%d", rec.Code)
		}
	})
}

func TestImg(t *testing.T) {
	s, _ := newServer(t, 80)
	render := func(p Photo, o Img) string {
		var b strings.Builder
		if err := s.Img(p, o).Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	photo := Photo{Key: "k", ContentType: "image/jpeg", Width: 800, Height: 600, Quality: 80}

	t.Run("offers every width up to the photo's own, with its size and the page's hint", func(t *testing.T) {
		got := render(photo, Img{Alt: `Jess & "Greg"`, Sizes: "(max-width: 640px) 100vw, 50vw"})
		want := `<img alt="Jess &amp; &#34;Greg&#34;" sizes="(max-width: 640px) 100vw, 50vw" loading="lazy" width="800" height="600" ` +
			`srcset="/photos/k/160w-q80.webp 160w, /photos/k/240w-q80.webp 240w, /photos/k/320w-q80.webp 320w, /photos/k/480w-q80.webp 480w, /photos/k/720w-q80.webp 720w" src="/photos/k/720w-q80.webp">`
		if got != want {
			t.Errorf("\n%s\n%s", got, want)
		}
	})

	t.Run("a priority photo is fetched first; a photo of unknown size has none", func(t *testing.T) {
		got := render(Photo{Key: "k", ContentType: "image/jpeg"}, Img{Priority: true})
		if !strings.Contains(got, `loading="eager" fetchpriority="high"`) || strings.Contains(got, "width=") || !strings.Contains(got, "2400w") {
			t.Error(got)
		}
	})

	t.Run("where the placeholder is enough, it's all that shows", func(t *testing.T) {
		got := render(Photo{Key: "k", ContentType: "image/jpeg", Width: 800, Placeholder: "data:image/webp;base64,AAAA"}, Img{PlaceholderWhen: "(max-width: 640px)"})
		if !strings.HasPrefix(got, `<picture><source media="(max-width: 640px)" srcset="data:image/webp;base64,AAAA"><img `) || !strings.HasSuffix(got, "></picture>") {
			t.Error(got)
		}
		if got := render(photo, Img{PlaceholderWhen: "(max-width: 640px)"}); !strings.Contains(got, `srcset="data:image/gif;base64,`) {
			t.Errorf("no placeholder yet: %s", got)
		}
	})

	t.Run("a type it can't resize is the original, as it is", func(t *testing.T) {
		if got := render(Photo{Key: "k", ContentType: "image/tiff"}, Img{Alt: "x"}); got != `<img alt="x" loading="lazy" src="/photos/k/original">` {
			t.Error(got)
		}
	})

	t.Run("the blurred placeholder keeps the photo's shape, escaped for a CSS url()", func(t *testing.T) {
		got := s.Blurred(Photo{Width: 800, Height: 600, Placeholder: "data:image/webp;base64,AAAA"})
		if !strings.HasPrefix(got, `url("data:image/svg+xml,`) || !strings.Contains(got, "viewBox=%220%200%2032%2024%22") || strings.ContainsAny(strings.TrimSuffix(strings.TrimPrefix(got, `url("`), `")`), ` "<>#`) {
			t.Error(got)
		}
		if s.Blurred(Photo{Width: 800}) != "" {
			t.Error("a blur with no placeholder")
		}
	})
}
