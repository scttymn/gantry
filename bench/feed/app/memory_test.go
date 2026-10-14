package app_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"feed/db/seeds"
)

// What an open connection costs the app, by the runtime's own account: its
// goroutine's stack, and what's on the heap for it. In the challenge every
// user holds a connection open and asks of it now and then, so this, times
// the users, is the memory the app needs: it's what stops it there, not CPU.
//
// Three kinds of connection, each idle when counted: one that has asked for
// nothing yet, one that has had an answer the database had no part in, and
// one that has read the feed and a post (and, one in seven, liked it). The
// stack is counted twice: as the requests left it, and after the collector
// has been by a few times, which is when the runtime gives back what a
// goroutine no longer reaches into.
func TestMemoryPerConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("opens thousands of connections")
	}
	const conns = 2000
	a := newApp(t)
	if err := seeds.Fill(t.Context(), a.DB, 500); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	bearer := token("test-secret", time.Now().Add(time.Hour))

	ask := func(c net.Conn, r *bufio.Reader, method, path string) {
		t.Helper()
		fmt.Fprintf(c, "%s %s HTTP/1.1\r\nHost: feed\r\nAccept: application/json\r\nAuthorization: Bearer %s\r\nContent-Length: 0\r\n\r\n", method, path, bearer)
		r.Reset(c)
		res, err := http.ReadResponse(r, nil)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode >= 500 {
			t.Fatalf("%s %s = %d", method, path, res.StatusCode)
		}
	}
	kinds := []struct {
		name string
		use  func(i int, c net.Conn, r *bufio.Reader)
	}{
		{"has asked for nothing", func(int, net.Conn, *bufio.Reader) {}},
		{"has had an answer with no query", func(_ int, c net.Conn, r *bufio.Reader) { ask(c, r, "GET", "/no-such-path") }},
		{"has read the feed and a post", func(i int, c net.Conn, r *bufio.Reader) {
			ask(c, r, "GET", "/feed")
			ask(c, r, "GET", fmt.Sprint("/posts/", 1+i))
			if i%7 == 0 {
				ask(c, r, "POST", fmt.Sprint("/posts/", 1+i, "/like"))
			}
		}},
	}
	for _, kind := range kinds {
		settle()
		before := memory()
		open := make([]net.Conn, conns)
		r := bufio.NewReader(nil)
		for i := range open {
			c, err := net.Dial("tcp", srv.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			open[i] = c
			kind.use(i, c, r)
		}
		fresh := memory()
		settle()
		settled := memory()
		per := func(now, was uint64) float64 { return (float64(now) - float64(was)) / conns / 1024 }
		t.Logf("a connection that %s: %.1f KB of stack as it was left, %.1f KB once collected; %.1f KB of heap",
			kind.name, per(fresh.StackInuse, before.StackInuse), per(settled.StackInuse, before.StackInuse), per(settled.HeapAlloc, before.HeapAlloc))
		for _, c := range open {
			c.Close()
		}
	}
}

// settle lets the collector by until what it can give back is given back: a
// goroutine's stack is halved once a collection, while it's using under a
// quarter of it.
func settle() {
	for range 6 {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
}

func memory() runtime.MemStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m
}
