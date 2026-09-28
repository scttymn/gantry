package compress

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var page = strings.Repeat("<p>Come as you are. Build from here.</p>\n", 400)

func serve(h http.Handler, method, acceptEncoding string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/", nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func text(contentType string, parts ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("Content-Length", "999999") // wrong once compressed; must go
		for _, p := range parts {
			io.WriteString(w, p)
		}
	})
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestAccepts(t *testing.T) {
	for ae, want := range map[string]bool{
		"gzip": true, "gzip, deflate, br, zstd": true, "br;q=1.0, gzip;q=0.8": true, "*": true, "GZIP": true,
		"": false, "identity": false, "br": false, "gzip;q=0": false, "gzip; q=0.0": false, "deflate, gzip;q=0": false, "*;q=0": false,
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Encoding", ae)
		if got := Accepts(req); got != want {
			t.Errorf("%q: %v", ae, got)
		}
	}
}

func TestCompressible(t *testing.T) {
	for typ, want := range map[string]bool{
		"text/html; charset=utf-8": true, "text/css": true, "text/javascript": true, "application/javascript": true,
		"application/json": true, "application/manifest+json": true, "image/svg+xml": true, "text/plain": true, "application/xml": true,
		"image/webp": false, "image/png": false, "font/woff2": false, "application/octet-stream": false, "": false,
	} {
		if got := Compressible(typ); got != want {
			t.Errorf("%q: %v", typ, got)
		}
	}
}

func TestHandler(t *testing.T) {
	t.Run("text leaves gzipped for a client that takes it, reads the same, and says so", func(t *testing.T) {
		rec := serve(Handler(text("text/html; charset=utf-8", page[:100], page[100:])), "GET", "gzip, br")
		h := rec.Header()
		if h.Get("Content-Encoding") != "gzip" || h.Get("Content-Length") != "" || !strings.Contains(h.Get("Vary"), "Accept-Encoding") {
			t.Fatalf("%v", h)
		}
		if got := gunzip(t, rec.Body.Bytes()); got != page {
			t.Errorf("%d bytes back, %d sent", len(got), len(page))
		}
		if rec.Body.Len()*10 > len(page) {
			t.Errorf("%d bytes from %d", rec.Body.Len(), len(page))
		}
	})

	t.Run("a client that doesn't take gzip gets it as it is, still told it varies", func(t *testing.T) {
		rec := serve(Handler(text("text/html", page)), "GET", "br")
		if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != page || !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
			t.Errorf("%v", rec.Header())
		}
	})

	t.Run("what's compressed already, or not text, goes as it is", func(t *testing.T) {
		for _, typ := range []string{"image/webp", "font/woff2"} {
			if rec := serve(Handler(text(typ, page)), "GET", "gzip"); rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != page {
				t.Errorf("%s: %q", typ, rec.Header().Get("Content-Encoding"))
			}
		}
		encoded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Encoding", "gzip")
			io.WriteString(w, "already packed")
		})
		if rec := serve(Handler(encoded), "GET", "gzip"); rec.Body.String() != "already packed" {
			t.Error("compressed twice")
		}
	})

	t.Run("a response with no type is sniffed, as net/http would", func(t *testing.T) {
		rec := serve(Handler(text("", "<!doctype html><html>"+page)), "GET", "gzip")
		if rec.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%v", rec.Header())
		}
	})

	t.Run("HEAD, ranges and bodiless answers aren't compressed", func(t *testing.T) {
		if rec := serve(Handler(text("text/html", page)), "HEAD", "gzip"); rec.Header().Get("Content-Encoding") != "" {
			t.Error("HEAD")
		}
		if rec := serve(Handler(text("text/html", page)), "GET", "gzip", "Range", "bytes=0-9"); rec.Header().Get("Content-Encoding") != "" {
			t.Error("range")
		}
		for _, code := range []int{http.StatusNoContent, http.StatusNotModified} {
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(code)
			})
			if rec := serve(Handler(h), "GET", "gzip"); rec.Header().Get("Content-Encoding") != "" || rec.Body.Len() != 0 || rec.Code != code {
				t.Errorf("%d: %q, %d bytes", code, rec.Header().Get("Content-Encoding"), rec.Body.Len())
			}
		}
	})

	t.Run("a status and headers set by the handler survive", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, page)
		})
		rec := serve(Handler(h), "GET", "gzip")
		if rec.Code != 404 || rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" || gunzip(t, rec.Body.Bytes()) != page {
			t.Errorf("%d %v", rec.Code, rec.Header())
		}
	})

	t.Run("a flush sends what's been written so far", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, page)
			http.NewResponseController(w).Flush()
		})
		rec := serve(Handler(h), "GET", "gzip")
		if !rec.Flushed || rec.Body.Len() == 0 {
			t.Errorf("flushed %v, %d bytes", rec.Flushed, rec.Body.Len())
		}
	})

	t.Run("many requests at once each get their own whole page", func(t *testing.T) {
		h := Handler(text("text/html", page))
		done := make(chan string, 50)
		for range 50 {
			go func() { done <- gunzipOr(serve(h, "GET", "gzip").Body.Bytes()) }()
		}
		for range 50 {
			if got := <-done; got != page {
				t.Fatalf("%d bytes", len(got))
			}
		}
	})
}

func gunzipOr(b []byte) string {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return err.Error()
	}
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestBytes(t *testing.T) {
	t.Run("compresses once, at best compression, for serving as is", func(t *testing.T) {
		packed := Bytes([]byte(page))
		if gunzip(t, packed) != page || len(packed)*10 > len(page) {
			t.Errorf("%d bytes", len(packed))
		}
	})
}
