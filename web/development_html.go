package web

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"golang.org/x/net/html"
)

const developmentBufferLimit = 2 << 20
const developmentPageOpen = `<div data-gantry-development-layout><div data-gantry-page>`

const (
	developmentStyles     = "/_gantry/development.css"
	developmentStylesheet = `<link rel="stylesheet" href="` + developmentStyles + `">`
	developmentCSS        = `[data-gantry-development-layout]{position:fixed;inset:0;display:flex;flex-direction:column;background:inherit}
[data-gantry-page]{flex:1;min-height:0;overflow:auto;position:relative;contain:layout}
[data-gantry-development-warnings]{flex:none;width:100%;box-sizing:border-box;background:#fff8e6;color:#332600;border-top:1px solid #bb8500;font:14px/1.5 system-ui,sans-serif;text-align:left}
[data-gantry-development-warnings] summary{cursor:pointer;padding:12px 16px;font-weight:700}
[data-gantry-development-warnings] details>div{padding:0 16px 12px;max-height:40vh;overflow:auto}
[data-gantry-development-warnings] ul{padding-left:20px}
[data-gantry-development-warnings] li{margin:12px 0;overflow-wrap:anywhere}
`
)

func serveDevelopmentPage(next http.Handler, w http.ResponseWriter, r *http.Request, inject func(context.Context, []byte) ([]byte, bool)) {
	if r.Method == http.MethodHead || r.Header.Get("Upgrade") != "" || r.Header.Get("Range") != "" {
		next.ServeHTTP(w, r)
		return
	}
	dw := &developmentWriter{ResponseWriter: w}
	next.ServeHTTP(dw, r)
	if dw.sent {
		return
	}
	if dw.status == 0 {
		dw.status = http.StatusOK
	}
	body := dw.body.Bytes()
	if developmentHTML(dw.Header()) && dw.status != http.StatusNoContent && dw.status != http.StatusNotModified {
		if result, changed := inject(r.Context(), body); changed {
			body = result
			for _, key := range []string{"Content-Length", "ETag", "Last-Modified", "Content-MD5", "Digest"} {
				dw.Header().Del(key)
			}
			dw.Header().Set("Cache-Control", "no-store")
		}
	}
	w.WriteHeader(dw.status)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
}
func developmentHTML(h http.Header) bool {
	return strings.HasPrefix(strings.ToLower(h.Get("Content-Type")), "text/html") && h.Get("Content-Encoding") == "" && h.Get("Content-Range") == ""
}

type developmentWriter struct {
	http.ResponseWriter
	body   bytes.Buffer
	status int
	sent   bool
}

func (w *developmentWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *developmentWriter) WriteHeader(status int) {
	if w.sent {
		return
	}
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status == 0 {
		w.status = status
	}
}
func (w *developmentWriter) Write(p []byte) (int, error) {
	if w.sent {
		return w.ResponseWriter.Write(p)
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", http.DetectContentType(p))
	}
	if !developmentHTML(w.Header()) || w.body.Len()+len(p) > developmentBufferLimit {
		if err := w.passThrough(); err != nil {
			return 0, err
		}
		return w.ResponseWriter.Write(p)
	}
	return w.body.Write(p)
}
func (w *developmentWriter) passThrough() error {
	if w.sent {
		return nil
	}
	w.sent = true
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(w.status)
	_, err := io.Copy(w.ResponseWriter, &w.body)
	return err
}
func (w *developmentWriter) FlushError() error {
	if err := w.passThrough(); err != nil {
		return err
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func injectDevelopmentHTML(ctx context.Context, body []byte) ([]byte, bool) {
	var panel bytes.Buffer
	if err := DevelopmentWarnings().Render(ctx, &panel); err != nil || panel.Len() == 0 {
		return body, false
	}
	z := html.NewTokenizer(bytes.NewReader(body))
	offset, bodyStart, bodyEnd := 0, -1, -1
	for {
		kind := z.Next()
		start := offset
		offset += len(z.Raw())
		if kind == html.ErrorToken {
			break
		}
		if kind != html.StartTagToken && kind != html.EndTagToken {
			continue
		}
		token := z.Token()
		for _, attr := range token.Attr {
			if attr.Key == "data-gantry-development-layout" {
				return body, false
			}
		}
		if token.Data == "body" {
			if kind == html.StartTagToken {
				bodyStart = offset
			} else {
				bodyEnd = start
			}
		}
	}
	if bodyStart < 0 || bodyEnd < bodyStart {
		return body, false
	}
	var result bytes.Buffer
	result.Write(body[:bodyStart])
	result.WriteString(developmentPageOpen)
	result.Write(body[bodyStart:bodyEnd])
	result.WriteString("</div>")
	result.Write(panel.Bytes())
	result.WriteString("</div>")
	result.Write(body[bodyEnd:])
	return result.Bytes(), true
}
