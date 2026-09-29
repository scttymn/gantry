package web

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// streamApp serves a stream that says its first part, then waits for
// next before ending.
func streamApp(t *testing.T, contentType string, next chan struct{}) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	rt := NewRouter(slog.New(slog.NewTextHandler(&logs, nil)), nil)
	rt.Handle("GET /stream", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", contentType)
		rc := http.NewResponseController(w)
		// A long stream moves its own write deadline.
		deadline := rc.SetWriteDeadline(time.Now().Add(time.Hour))
		fmt.Fprintf(w, "data: first deadline=%v\n\n", deadline)
		if err := rc.Flush(); err != nil {
			return err
		}
		<-next
		io.WriteString(w, "data: last\n\n")
		return nil
	})
	rt.Handle("GET /half", func(w http.ResponseWriter, r *http.Request) error {
		io.WriteString(w, strings.Repeat("a page, half drawn ", 1000))
		http.NewResponseController(w).Flush()
		return errors.New("the database went away")
	})
	rt.Handle("GET /gone", func(w http.ResponseWriter, r *http.Request) error {
		<-r.Context().Done()
		next <- struct{}{}
		return r.Context().Err()
	})
	srv := httptest.NewServer(rt.Handler())
	t.Cleanup(srv.Close)
	return srv, &logs
}

func TestStreams(t *testing.T) {
	for _, contentType := range []string{"text/event-stream", "text/plain; charset=utf-8"} {
		next := make(chan struct{})
		srv, _ := streamApp(t, contentType, next)
		req, _ := http.NewRequest("GET", srv.URL+"/stream", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		var body io.Reader = resp.Body
		gzipped := resp.Header.Get("Content-Encoding") == "gzip"
		if gzipped {
			body = gunzip(t, resp.Body)
		}
		// The first part arrives while the handler is still waiting: nothing
		// held it back. (Held back, it would never come: give up after 5s.)
		giveUp := time.AfterFunc(5*time.Second, func() { resp.Body.Close() })
		line, err := bufio.NewReader(body).ReadString('\n')
		giveUp.Stop()
		if err != nil || line != "data: first deadline=<nil>\n" {
			t.Errorf("%s: first line %q, %v", contentType, line, err)
		}
		if strings.HasPrefix(contentType, "text/event-stream") == gzipped {
			t.Errorf("%s: gzipped %v; events never are, other text is", contentType, gzipped)
		}
		close(next)
		resp.Body.Close()
	}
}

// A failure after the response started can't be a page any more: the
// connection is cut, so the client sees an error, not a page that looks
// whole.
func TestFailureMidStream(t *testing.T) {
	srv, logs := streamApp(t, "text/plain", make(chan struct{}))
	resp, err := http.Get(srv.URL + "/half")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if err == nil {
		t.Error("the client read a whole response")
	}
	if !strings.Contains(logs.String(), "the database went away") {
		t.Error("the failure wasn't logged")
	}
}

// A client that leaves cancels the handler's work, and that isn't an error.
func TestClientLeaves(t *testing.T) {
	stopped := make(chan struct{}, 1)
	srv, logs := streamApp(t, "text/plain", stopped)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/gone", nil)
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	http.DefaultClient.Do(req)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler's context wasn't cancelled")
	}
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(logs.String(), "level=ERROR") || strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("logged: %s", logs.String())
	}
}

func TestDownload(t *testing.T) {
	for _, tc := range []struct{ name, disposition string }{
		{"backup-2026-09-29.tar.gz", `attachment; filename="backup-2026-09-29.tar.gz"`},
		{`résumé "final".pdf`, `attachment; filename="r_sum_ _final_.pdf"; filename*=UTF-8''r%C3%A9sum%C3%A9%20%22final%22.pdf`},
		{"../../etc/passwd", `attachment; filename="passwd"`},
		{"it's*.txt", `attachment; filename="it's*.txt"`},
		{"café's.txt", `attachment; filename="caf_'s.txt"; filename*=UTF-8''caf%C3%A9%27s.txt`},
		{"", `attachment; filename="download"`},
	} {
		w := httptest.NewRecorder()
		err := Download(w, httptest.NewRequest("GET", "/", nil), tc.name, "application/gzip", strings.NewReader("the bytes"))
		if err != nil || w.Header().Get("Content-Disposition") != tc.disposition || w.Header().Get("Content-Type") != "application/gzip" || w.Body.String() != "the bytes" {
			t.Errorf("%q: %v %q %q %q", tc.name, err, w.Header().Get("Content-Disposition"), w.Header().Get("Content-Type"), w.Body)
		}
	}
}

func gunzip(t *testing.T, r io.Reader) io.Reader {
	t.Helper()
	z, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	return z
}
