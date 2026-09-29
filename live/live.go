// Package live sends a page's changes to it as they happen (turbo-rails'
// broadcasts, over Server-Sent Events rather than Action Cable): a page
// subscribes to streams by name, and the app broadcasts Turbo stream
// actions to them.
//
//	hub := live.New(signer, live.Options{})
//	rt.Handle("GET /live", hub.Serve) // behind the filters that decide who may listen
//	server.RegisterOnShutdown(hub.Close)
//
//	@hub.Source("project:" + id)       // in the page: it subscribes
//	hub.Broadcast(ctx, "project:"+id, turbo.Append("deploys", views.Deploy(d)))
//	hub.Refresh("project:"+id, turbo.RequestID(r)) // or: the page fetches itself again
//
// A page can only listen to the streams it was given: Source signs their
// names (as turbo_stream_from does), and Serve refuses a name that isn't
// signed. The hub is in this process's memory; a gantry app is one process
// (a hub shared through the database waits for an app that runs several).
package live

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/sign"
	"github.com/scttymn/gantry/turbo"
	"github.com/scttymn/gantry/web"
)

// Options are a hub's settings; each zero value takes its default.
type Options struct {
	Path      string        // where Serve is routed, for Source's URL: "/live"
	KeepAlive time.Duration // how often an idle stream gets a comment, so proxies keep it open: 30 s
	Debounce  time.Duration // refreshes to a stream this close together are one: 0.5 s (turbo-rails')
	Buffer    int           // messages a subscriber may fall behind before it's dropped: 64
	Log       *slog.Logger
}

// Hub is the app's streams and who's listening.
type Hub struct {
	signer sign.Signer
	o      Options

	mu      sync.Mutex
	streams map[string]map[*subscriber]struct{}
	pending map[refreshKey]*time.Timer
	closed  bool
}

type subscriber struct {
	streams []string
	ch      chan string
	gone    chan struct{} // closed when the hub drops it
	once    sync.Once
}

func (s *subscriber) drop() { s.once.Do(func() { close(s.gone) }) }

type refreshKey struct{ stream, requestID string }

const purpose = "stream"

// New is an empty hub, signing stream names with signer.
func New(signer sign.Signer, o Options) *Hub {
	if o.Path == "" {
		o.Path = "/live"
	}
	if o.KeepAlive == 0 {
		o.KeepAlive = 30 * time.Second
	}
	if o.Debounce == 0 {
		o.Debounce = 500 * time.Millisecond
	}
	if o.Buffer == 0 {
		o.Buffer = 64
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Hub{signer: signer, o: o, streams: map[string]map[*subscriber]struct{}{}, pending: map[refreshKey]*time.Timer{}}
}

// URL is where a page listens to streams: Serve's path, with their names
// signed.
func (h *Hub) URL(streams ...string) string {
	names, _ := json.Marshal(streams)
	return h.o.Path + "?s=" + url.QueryEscape(h.signer.Sign(purpose, string(names)))
}

// Source is the element a page draws to listen to streams (one connection
// for them all): Turbo's <turbo-stream-source>, which applies each action
// it's sent.
func (h *Hub) Source(streams ...string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, `<turbo-stream-source src="`+templ.EscapeString(h.URL(streams...))+`"></turbo-stream-source>`)
		return err
	})
}

// ErrNotSigned is a request to listen to streams the page wasn't given.
var ErrNotSigned = errors.New("live: those streams weren't signed for this app")

// Serve streams the signed streams' broadcasts to the request as events,
// until the client leaves, falls too far behind, or the hub closes. Route it
// behind the filters that decide who may listen.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request) error {
	value, ok := h.signer.Verify(purpose, r.URL.Query().Get("s"))
	var streams []string
	if !ok || json.Unmarshal([]byte(value), &streams) != nil || len(streams) == 0 {
		return web.Status(http.StatusForbidden, ErrNotSigned)
	}
	sub := h.subscribe(streams)
	if sub == nil {
		return web.Status(http.StatusServiceUnavailable, errors.New("live: shutting down"))
	}
	defer h.unsubscribe(sub)

	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{}) // a stream outlives the server's write timeout
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: pass it on as it comes
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return err
	}
	keepAlive := time.NewTicker(h.o.KeepAlive)
	defer keepAlive.Stop()
	for {
		var out string
		select {
		case msg := <-sub.ch:
			out = event(msg)
		case <-keepAlive.C:
			out = ": keep-alive\n\n"
		case <-sub.gone:
			return nil // behind, or the hub closed: the browser reconnects
		case <-r.Context().Done():
			return nil
		}
		if _, err := io.WriteString(w, out); err != nil {
			return nil // gone
		}
		if err := rc.Flush(); err != nil {
			return nil
		}
	}
}

// event is msg as a server-sent event: each line a data line.
func event(msg string) string {
	var b strings.Builder
	for line := range strings.Lines(msg) {
		b.WriteString("data: " + strings.TrimRight(line, "\r\n") + "\n")
	}
	if msg == "" {
		b.WriteString("data: \n")
	}
	b.WriteString("\n")
	return b.String()
}

func (h *Hub) subscribe(streams []string) *subscriber {
	s := &subscriber{streams: streams, ch: make(chan string, h.o.Buffer), gone: make(chan struct{})}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	for _, name := range streams {
		if h.streams[name] == nil {
			h.streams[name] = map[*subscriber]struct{}{}
		}
		h.streams[name][s] = struct{}{}
	}
	return s
}

func (h *Hub) unsubscribe(s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remove(s)
}

// remove takes s off its streams; the caller holds mu.
func (h *Hub) remove(s *subscriber) {
	for _, name := range s.streams {
		delete(h.streams[name], s)
		if len(h.streams[name]) == 0 {
			delete(h.streams, name)
		}
	}
	s.drop()
}

// Subscribers is how many are listening to stream.
func (h *Hub) Subscribers(stream string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.streams[stream])
}

// Broadcast sends actions to everyone listening to stream. A subscriber too
// far behind to take it is dropped rather than buffered for, so memory
// stays flat; its browser reconnects and catches up with the page.
func (h *Hub) Broadcast(ctx context.Context, stream string, actions ...templ.Component) error {
	msg, err := render(ctx, actions)
	if err != nil {
		return err
	}
	h.send(stream, msg)
	return nil
}

// BroadcastTx is Broadcast once tx commits, and never if it doesn't: a page
// never shows a change that was rolled back. The actions are drawn now.
func (h *Hub) BroadcastTx(ctx context.Context, tx *db.Tx, stream string, actions ...templ.Component) error {
	msg, err := render(ctx, actions)
	if err != nil {
		return err
	}
	tx.AfterCommit(func() { h.send(stream, msg) })
	return nil
}

func render(ctx context.Context, actions []templ.Component) (string, error) {
	var b strings.Builder
	for _, a := range actions {
		if err := a.Render(ctx, &b); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

func (h *Hub) send(stream, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.streams[stream] {
		select {
		case s.ch <- msg:
		default:
			h.o.Log.Warn("live: a subscriber fell behind and was dropped", "stream", stream)
			h.remove(s)
		}
	}
}

// Refresh has every page listening to stream fetch itself again (Turbo 8's
// page refresh), except the one whose request made the change (requestID,
// turbo.RequestID(r); "" for none). Refreshes to a stream within Debounce
// of each other are sent once, after the last (turbo-rails').
func (h *Hub) Refresh(stream, requestID string) {
	key := refreshKey{stream, requestID}
	h.mu.Lock()
	defer h.mu.Unlock()
	if t, ok := h.pending[key]; ok {
		t.Reset(h.o.Debounce)
		return
	}
	h.pending[key] = time.AfterFunc(h.o.Debounce, func() {
		h.mu.Lock()
		delete(h.pending, key)
		h.mu.Unlock()
		msg, _ := render(context.Background(), []templ.Component{turbo.Action{Name: "refresh", RequestID: requestID}})
		h.send(stream, msg)
	})
}

// RefreshTx is Refresh once tx commits.
func (h *Hub) RefreshTx(tx *db.Tx, stream, requestID string) {
	tx.AfterCommit(func() { h.Refresh(stream, requestID) })
}

// Close ends every stream, for shutting down (server.RegisterOnShutdown):
// http.Server.Shutdown waits for requests to finish, and a stream doesn't.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, subs := range h.streams {
		for s := range subs {
			h.remove(s)
		}
	}
	for key, t := range h.pending {
		t.Stop()
		delete(h.pending, key)
	}
}
