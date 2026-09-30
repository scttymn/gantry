package live

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/sign"
	"github.com/scttymn/gantry/testkit"
	"github.com/scttymn/gantry/turbo"
	"github.com/scttymn/gantry/web"
)

func text(s string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	})
}

var signer = sign.Signer{Key: []byte("test")}

// serve is a hub behind a router, as an app routes it, on a real server.
func serve(t *testing.T, o Options) (*Hub, *httptest.Server) {
	t.Helper()
	h := New(signer, o)
	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Handle("GET /live", h.Serve)
	srv := httptest.NewUnstartedServer(rt.Handler())
	srv.Config.RegisterOnShutdown(h.Close)
	srv.Start()
	t.Cleanup(func() { h.Close(); srv.Close() })
	return h, srv
}

// listener reads a stream's events.
type listener struct {
	resp   *http.Response
	events chan string
}

func listen(t *testing.T, srv *httptest.Server, h *Hub, streams ...string) *listener {
	t.Helper()
	resp, err := http.Get(srv.URL + h.URL(streams...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	l := &listener{resp: resp, events: make(chan string, 100)}
	go func() {
		defer close(l.events)
		sc := bufio.NewScanner(resp.Body)
		var ev strings.Builder
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				l.events <- ev.String()
				ev.Reset()
				continue
			}
			if ev.Len() > 0 {
				ev.WriteString("\n")
			}
			ev.WriteString(line)
		}
	}()
	return l
}

func (l *listener) next(t *testing.T) string {
	t.Helper()
	select {
	case ev, ok := <-l.events:
		if !ok {
			t.Fatal("the stream ended")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	return ""
}

func (l *listener) quiet(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case ev, ok := <-l.events:
		if ok {
			t.Fatalf("an event: %q", ev)
		}
	case <-time.After(d):
	}
}

func waitFor(t *testing.T, h *Hub, stream string, n int) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); h.Subscribers(stream) != n; {
		if time.Now().After(deadline) {
			t.Fatalf("%d listening to %s, want %d", h.Subscribers(stream), stream, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBroadcast(t *testing.T) {
	ctx := context.Background()
	h, srv := serve(t, Options{})
	a := listen(t, srv, h, "project:1")
	b := listen(t, srv, h, "project:1", "project:2")
	c := listen(t, srv, h, "project:2")
	waitFor(t, h, "project:1", 2)
	waitFor(t, h, "project:2", 2)

	h.Broadcast(ctx, "project:1", turbo.Append("deploys", text("<li>one</li>")), turbo.Remove("empty"))
	want := `data: <turbo-stream action="append" target="deploys"><template><li>one</li></template></turbo-stream><turbo-stream action="remove" target="empty"></turbo-stream>`
	if got := a.next(t); got != want {
		t.Errorf("a got %q", got)
	}
	if got := b.next(t); got != want {
		t.Errorf("b got %q", got)
	}
	c.quiet(t, 50*time.Millisecond)

	// Lines of a message are each a data line.
	h.Broadcast(ctx, "project:2", turbo.Update("log", text("line one\nline two")))
	if got := c.next(t); got != "data: <turbo-stream action=\"update\" target=\"log\"><template>line one\ndata: line two</template></turbo-stream>" {
		t.Errorf("c got %q", got)
	}
	b.next(t)

	// A broadcast to no one is fine.
	if err := h.Broadcast(ctx, "nobody", turbo.Refresh()); err != nil {
		t.Error(err)
	}

	// Leaving unsubscribes.
	a.resp.Body.Close()
	waitFor(t, h, "project:1", 1)
}

// Only names the app signed can be listened to.
func TestSigned(t *testing.T) {
	h, srv := serve(t, Options{})
	other := New(sign.Signer{Key: []byte("another app")}, Options{})
	for _, path := range []string{"/live", "/live?s=x", "/live?s=" + strings.TrimPrefix(other.URL("project:1"), "/live?s="), h.URL()} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
	// A name signed for another purpose isn't a stream's.
	resp, _ := http.Get(srv.URL + "/live?s=" + signer.Sign("flash", `["project:1"]`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("another purpose: %d", resp.StatusCode)
	}
}

func TestSource(t *testing.T) {
	h := New(signer, Options{Path: "/cable"})
	var b strings.Builder
	h.Source("project:1", "a&b").Render(context.Background(), &b)
	got := b.String()
	if !strings.HasPrefix(got, `<turbo-stream-source src="/cable?s=`) || !strings.HasSuffix(got, `"></turbo-stream-source>`) {
		t.Errorf("%s", got)
	}
	if strings.Contains(got, "a&b") {
		t.Errorf("names shown as they are: %s", got)
	}
}

// An idle stream gets a comment now and then, so proxies don't close it.
func TestKeepAlive(t *testing.T) {
	h, srv := serve(t, Options{KeepAlive: 30 * time.Millisecond})
	l := listen(t, srv, h, "s")
	if got := l.next(t); got != ": keep-alive" {
		t.Errorf("%q", got)
	}
	if New(signer, Options{}).o.KeepAlive != 30*time.Second {
		t.Error("the default isn't 30 s")
	}
}

// A subscriber too far behind is dropped, and the others carry on.
func TestSlowSubscriber(t *testing.T) {
	ctx := context.Background()
	h := New(signer, Options{Buffer: 2})
	slow := h.subscribe([]string{"s"})
	fast := h.subscribe([]string{"s"})
	for i := range 3 {
		h.Broadcast(ctx, "s", text("m"))
		<-fast.ch
		if i < 2 && h.Subscribers("s") != 2 {
			t.Fatalf("dropped after %d", i+1)
		}
	}
	select {
	case <-slow.gone:
	default:
		t.Fatal("the slow one wasn't dropped")
	}
	if h.Subscribers("s") != 1 {
		t.Errorf("%d listening", h.Subscribers("s"))
	}
	if New(signer, Options{}).o.Buffer != 64 {
		t.Error("the default buffer isn't 64")
	}

	// Its stream ends, so the browser reconnects.
	h2, srv := serve(t, Options{Buffer: 1})
	l := listen(t, srv, h2, "s")
	waitFor(t, h2, "s", 1)
	h2.mu.Lock()
	var s *subscriber
	for s = range h2.streams["s"] {
	}
	h2.mu.Unlock()
	h2.mu.Lock()
	h2.remove(s) // as send does to one behind
	h2.mu.Unlock()
	for ev := range l.events {
		_ = ev
	}
	waitFor(t, h2, "s", 0)
}

// Refreshes close together are one, after the last; a request's own is
// marked so its page skips it.
func TestRefresh(t *testing.T) {
	h, srv := serve(t, Options{Debounce: 100 * time.Millisecond})
	l := listen(t, srv, h, "board")
	waitFor(t, h, "board", 1)
	var last time.Time
	for i := range 5 {
		if i > 0 {
			time.Sleep(30 * time.Millisecond)
		}
		last = time.Now()
		h.Refresh("board", "")
	}
	if got := l.next(t); got != `data: <turbo-stream action="refresh"></turbo-stream>` {
		t.Errorf("%q", got)
	}
	if after := time.Since(last); after < 90*time.Millisecond {
		t.Errorf("sent %v after the last: not a debounce after it", after)
	}
	l.quiet(t, 200*time.Millisecond)

	// Different requests' refreshes aren't merged.
	h.Refresh("board", "req-1")
	h.Refresh("board", "req-2")
	got := []string{l.next(t), l.next(t)}
	if !strings.Contains(got[0]+got[1], `request-id="req-1"`) || !strings.Contains(got[0]+got[1], `request-id="req-2"`) {
		t.Errorf("%q", got)
	}
	if New(signer, Options{}).o.Debounce != 500*time.Millisecond {
		t.Error("the default debounce isn't half a second")
	}
}

// Broadcasts in a transaction wait for its commit, and a rollback's never
// go.
func TestBroadcastTx(t *testing.T) {
	ctx := context.Background()
	d := testkit.DB(t, func(context.Context, *db.DB) error { return nil })
	h := New(signer, Options{Debounce: time.Millisecond})
	s := h.subscribe([]string{"s"})
	err := d.Tx(ctx, func(tx *db.Tx) error {
		h.BroadcastTx(ctx, tx, "s", text("committed"))
		h.RefreshTx(tx, "s", "")
		select {
		case m := <-s.ch:
			t.Errorf("sent before the commit: %q", m)
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if m := <-s.ch; m != "committed" {
		t.Errorf("%q", m)
	}
	select {
	case m := <-s.ch:
		if !strings.Contains(m, "refresh") {
			t.Errorf("%q", m)
		}
	case <-time.After(time.Second):
		t.Error("no refresh after the commit")
	}
	d.Tx(ctx, func(tx *db.Tx) error {
		h.BroadcastTx(ctx, tx, "s", text("rolled back"))
		h.RefreshTx(tx, "s", "")
		return errors.New("no")
	})
	select {
	case m := <-s.ch:
		t.Errorf("a rollback's broadcast: %q", m)
	case <-time.After(50 * time.Millisecond):
	}
	// A component that fails to draw is the caller's error, then and there.
	boom := templ.ComponentFunc(func(context.Context, io.Writer) error { return errors.New("boom") })
	d.Tx(ctx, func(tx *db.Tx) error {
		if err := h.BroadcastTx(ctx, tx, "s", boom); err == nil {
			t.Error("no error")
		}
		return nil
	})
	if err := h.Broadcast(ctx, "s", boom); err == nil {
		t.Error("no error")
	}
}

// Closing the hub ends every stream, so a server's shutdown doesn't wait
// on them.
func TestClose(t *testing.T) {
	h, srv := serve(t, Options{})
	l := listen(t, srv, h, "s")
	waitFor(t, h, "s", 1)
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		done <- srv.Config.Shutdown(ctx)
	}()
	if err := <-done; err != nil {
		t.Errorf("shutdown: %v", err)
	}
	for range l.events {
	}
	// A new listener after closing is turned away.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", h.URL("s"), nil)
	if err := h.Serve(w, r); web.StatusOf(err) != http.StatusServiceUnavailable {
		t.Errorf("after closing: %v", err)
	}
}

// The app can listen as a page does: what's broadcast and refreshed
// arrives, until it stops.
func TestListen(t *testing.T) {
	ctx := context.Background()
	h := New(sign.Signer{Key: []byte("k")}, Options{Debounce: time.Millisecond})
	messages, stop := h.Listen("project:1", "board")
	if h.Subscribers("project:1") != 1 || h.Subscribers("board") != 1 {
		t.Fatal("not subscribed")
	}
	h.Broadcast(ctx, "project:1", turbo.Append("deploys", text("<li>one</li>")))
	h.Broadcast(ctx, "project:2", turbo.Remove("elsewhere"))
	h.Refresh("board", "req-1")
	for _, want := range []string{
		`<turbo-stream action="append" target="deploys"><template><li>one</li></template></turbo-stream>`,
		`<turbo-stream action="refresh" request-id="req-1"></turbo-stream>`,
	} {
		select {
		case got := <-messages:
			if got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no %q", want)
		}
	}
	stop()
	if _, open := <-messages; open {
		t.Error("still open after stop")
	}
	if h.Subscribers("project:1") != 0 {
		t.Error("still subscribed after stop")
	}

	// After Close, a listener gets a closed channel.
	h.Close()
	if _, open := <-first(h.Listen("x")); open {
		t.Error("listening to a closed hub")
	}
}

func first(c <-chan string, _ func()) <-chan string { return c }
