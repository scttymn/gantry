package web

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	templruntime "github.com/a-h/templ/runtime"
	"golang.org/x/net/html"
)

const (
	reloadEvents = "/_gantry/reload"
	reloadScript = "/_gantry/reload.js"
)

//go:embed reload.js
var reloadJS []byte

var defaultReloads = &reloads{boot: newReloadBoot(), interval: 200 * time.Millisecond, files: templTextFiles}

// Reload makes pages reload themselves when the code changes. Router enables
// it in development unless GANTRY_DEV_RELOAD=false. Install manually ONLY in
// development.
//
// Every HTML page gets a script that knows the version of the code that
// rendered it, and follows the stream at /_gantry/reload, which says the
// current one. A version is this process's boot ID and a count of template
// text changes. templ's watcher restarts the server when Go code changes;
// the stream reconnects to the new process, and a page rendered by the old
// one reloads. A change to a template's text alone doesn't restart anything:
// templ rewrites the file it keeps the text in, and the count goes up.
func Reload(next http.Handler) http.Handler {
	return defaultReloads.handler(next)
}

func newReloadBoot() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type reloads struct {
	boot     string
	interval time.Duration
	files    func() []string // the files whose change is a reload

	baseline sync.Once
	mu       sync.Mutex
	changes  int    // template text changes seen
	last     string // the files' stamp when last polled
	streams  map[*reloadStream]struct{}
	watching bool
	servers  map[*http.Server]bool
}

type reloadStream struct {
	changed chan struct{}
	done    chan struct{}
}

// version is the code's: the process and its template text changes. Pages
// are told it before the files are first polled, so a change from then on
// is a new version.
func (h *reloads) version() string {
	h.baseline.Do(func() {
		var files []string
		if h.files != nil {
			files = h.files()
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.last = stamp(files)
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	return fmt.Sprintf("%s.%d", h.boot, h.changes)
}

func (h *reloads) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == reloadEvents && r.Method == http.MethodGet:
			h.serveEvents(w, r)
		case r.URL.Path == reloadScript && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(reloadJS)
		default:
			serveDevelopmentPage(next, w, r, h.inject)
		}
	})
}

func (h *reloads) serveEvents(w http.ResponseWriter, r *http.Request) {
	// A graceful shutdown waits on open requests; this stream would hold it
	// up until its timeout.
	if srv, ok := r.Context().Value(http.ServerContextKey).(*http.Server); ok {
		h.endOnShutdown(srv)
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	stream := h.subscribe()
	defer h.unsubscribe(stream)
	// retry: how soon the browser reconnects when a restart drops the stream.
	fmt.Fprintf(w, "retry: 250\nevent: version\ndata: %s\n\n", h.version())
	if rc.Flush() != nil {
		return
	}
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-stream.done:
			return
		case <-stream.changed:
			fmt.Fprintf(w, "event: version\ndata: %s\n\n", h.version())
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
		}
		if rc.Flush() != nil {
			return
		}
	}
}

func (h *reloads) subscribe() *reloadStream {
	s := &reloadStream{changed: make(chan struct{}, 1), done: make(chan struct{})}
	h.version() // the files' first stamp, before they're watched
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.streams == nil {
		h.streams = map[*reloadStream]struct{}{}
	}
	h.streams[s] = struct{}{}
	if !h.watching {
		h.watching = true
		go h.watch()
	}
	return s
}

func (h *reloads) unsubscribe(s *reloadStream) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.streams, s)
}

func (h *reloads) endOnShutdown(srv *http.Server) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.servers[srv] {
		return
	}
	if h.servers == nil {
		h.servers = map[*http.Server]bool{}
	}
	h.servers[srv] = true
	srv.RegisterOnShutdown(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		for s := range h.streams {
			close(s.done)
			delete(h.streams, s)
		}
	})
}

// change counts a template text change and tells every stream. Callers
// hold h.mu.
func (h *reloads) change() {
	h.changes++
	for s := range h.streams {
		select {
		case s.changed <- struct{}{}:
		default:
		}
	}
}

// watch polls the files while a stream is open. A change counts once a poll
// finds nothing further changing, so templ has finished writing. With no
// stream open nothing polls; the next stream's polls see what changed.
func (h *reloads) watch() {
	var files []string
	if h.files != nil {
		files = h.files()
	}
	pending := false
	tick := time.NewTicker(h.interval)
	defer tick.Stop()
	for range tick.C {
		now := stamp(files)
		h.mu.Lock()
		switch {
		case len(h.streams) == 0:
			if pending || now != h.last {
				h.last = now
				h.change()
			}
			h.watching = false
			h.mu.Unlock()
			return
		case now != h.last:
			h.last, pending = now, true
		case pending:
			pending = false
			h.change()
		}
		h.mu.Unlock()
	}
}

func stamp(files []string) string {
	var b strings.Builder
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, "%d %d\n", info.ModTime().UnixNano(), info.Size())
		} else {
			b.WriteString("-\n")
		}
	}
	return b.String()
}

// templTextFiles are the files templ's watcher (templ generate --watch)
// writes each template's text to, which the server reads on each render
// instead of restarting. None when templ isn't watching. Folders templ's
// watcher skips are skipped.
func templTextFiles() []string {
	root := os.Getenv("TEMPL_DEV_MODE_WATCH_ROOT")
	if os.Getenv("TEMPL_DEV_MODE") != "true" || root == "" {
		return nil
	}
	var files []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".templ") {
			files = append(files, templruntime.GetDevModeTextFileName(path))
		}
		return nil
	})
	return files
}

// inject puts the script at the end of a page's body. Fragments (no
// </body> or </html>) are left alone.
func (h *reloads) inject(_ context.Context, body []byte) ([]byte, bool) {
	z := html.NewTokenizer(bytes.NewReader(body))
	offset, bodyEnd, htmlEnd := 0, -1, -1
	for {
		kind := z.Next()
		start := offset
		offset += len(z.Raw())
		if kind == html.ErrorToken {
			break
		}
		if kind != html.StartTagToken && kind != html.EndTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		token := z.Token()
		for _, attr := range token.Attr {
			if attr.Key == "data-gantry-reload" {
				return body, false
			}
		}
		if kind == html.EndTagToken && token.Data == "body" {
			bodyEnd = start
		}
		if kind == html.EndTagToken && token.Data == "html" {
			htmlEnd = start
		}
	}
	at := bodyEnd
	if at < 0 {
		at = htmlEnd
	}
	if at < 0 {
		return body, false
	}
	tag := `<script src="` + reloadScript + `" data-gantry-reload data-version="` + h.version() + `" data-turbo-eval="false"></script>`
	var result bytes.Buffer
	result.Write(body[:at])
	result.WriteString(tag)
	result.Write(body[at:])
	return result.Bytes(), true
}
