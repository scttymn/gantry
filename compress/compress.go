// Package compress gzips a web app's text responses on the way out, as
// Rails' Thruster does in front of Puma, so a page crosses the network at a
// fraction of its size. Nothing in it knows about any one app.
//
// Handler compresses whatever a handler writes, per request, with gzip
// writers reused across requests. Bytes compresses once, at best
// compression, for files that never change (fingerprinted assets): serve
// those bytes as they are and there's no work per request at all.
package compress

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	// The standard library's API and format, at about twice its speed: gzip
	// is a third of a page's CPU otherwise (docs/plans/gantry.md, Evidence).
	"github.com/klauspost/compress/gzip"
)

// Accepts reports whether the request takes gzip: gzip or * in
// Accept-Encoding, with a q above zero.
func Accepts(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "gzip" && name != "*" {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.TrimSpace(k) == "q" {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		if q > 0 {
			return true
		}
	}
	return false
}

// Compressible reports whether a response of this type is worth gzipping:
// text of any kind. Images, fonts and archives are compressed already.
func Compressible(contentType string) bool {
	t, _, _ := strings.Cut(contentType, ";")
	t = strings.ToLower(strings.TrimSpace(t))
	switch {
	case t == "text/event-stream":
		// A stream of events arrives as it's sent: gzip would hold each
		// event back in its buffer, and proxies buffer compressed streams.
		return false
	case strings.HasPrefix(t, "text/"):
		return true
	case t == "application/javascript", t == "application/json", t == "application/xml",
		t == "image/svg+xml", strings.HasSuffix(t, "+json"), strings.HasSuffix(t, "+xml"):
		return true
	}
	return false
}

// Bytes is data gzipped at best compression, for serving again and again.
func Bytes(data []byte) []byte {
	var b bytes.Buffer
	w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	w.Write(data)
	w.Close()
	return b.Bytes()
}

var writers = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
	return w
}}

// Handler gzips next's text responses for clients that take gzip. It leaves
// alone a HEAD, a range request (its bytes count the file as it is), a
// response already encoded, and anything not text. Every response says it
// varies by Accept-Encoding, so a cache never hands gzip to a client that
// can't read it.
func Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" || !Accepts(r) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &writer{ResponseWriter: w}
		defer cw.close()
		next.ServeHTTP(cw, r)
	})
}

// writer decides at the first header or byte whether to compress.
type writer struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

func (w *writer) WriteHeader(code int) {
	if w.decided {
		return
	}
	w.decided = true
	h := w.Header()
	if code >= 200 && code != http.StatusNoContent && code != http.StatusNotModified &&
		h.Get("Content-Encoding") == "" && Compressible(h.Get("Content-Type")) {
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		w.gz = writers.Get().(*gzip.Writer)
		w.gz.Reset(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *writer) Write(b []byte) (int, error) {
	if !w.decided {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(b))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Flush sends what's been written so far, compressed as far as it goes.
func (w *writer) Flush() {
	if w.gz != nil {
		w.gz.Flush()
	}
	http.NewResponseController(w.ResponseWriter).Flush()
}

// Hijack is for handlers that take over the connection (websockets).
func (w *writer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *writer) close() {
	if w.gz == nil {
		return
	}
	w.gz.Close()
	w.gz.Reset(io.Discard)
	writers.Put(w.gz)
	w.gz = nil
}
