package web

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// slowly is a body that arrives in chunks with a pause before each: a phone's
// upload, or a large file to one.
type slowly struct {
	chunks, size int
	pause        time.Duration
}

func (s *slowly) Read(p []byte) (int, error) {
	if s.chunks == 0 {
		return 0, io.EOF
	}
	time.Sleep(s.pause)
	s.chunks--
	return copy(p, strings.Repeat("x", min(s.size, len(p)))), nil
}

// stalled is a body that sends prefix, then nothing until the test ends.
func stalled(t *testing.T, prefix string) io.Reader {
	r, w := io.Pipe()
	go w.Write([]byte(prefix))
	t.Cleanup(func() { w.Close() })
	return r
}

// client gives up on a request after 5 s, so a transfer the server never
// cuts off fails the test rather than hanging it.
var client = &http.Client{Timeout: 5 * time.Second}

// transfers is a router that echoes what it's sent and downloads body, on
// a server whose ReadTimeout and WriteTimeout are 100 ms.
func transfers(t *testing.T, body func() io.Reader) (srv *httptest.Server, ended <-chan struct{}) {
	done := make(chan struct{}, 10)
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Handle("GET /download", func(w http.ResponseWriter, r *http.Request) error {
		defer func() { done <- struct{}{} }()
		return Download(w, r, "big.bin", "application/octet-stream", body())
	})
	rt.Handle("POST /upload", func(w http.ResponseWriter, r *http.Request) error {
		f, _, err := r.FormFile("photo")
		if err != nil {
			return err
		}
		n, err := io.Copy(io.Discard, f)
		fmt.Fprintf(w, "%s %d", r.PostFormValue("note"), n)
		return err
	})
	rt.Handle("POST /data", func(w http.ResponseWriter, r *http.Request) error {
		_, err := io.Copy(w, r.Body)
		return err
	})
	srv = httptest.NewUnstartedServer(rt.Handler())
	srv.Config.ReadTimeout, srv.Config.WriteTimeout = 100*time.Millisecond, 100*time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, done
}

// multipartBody is a form with a note and a photo, its photo's bytes from
// photo.
func multipartBody(photo io.Reader) (io.Reader, string) {
	var head bytes.Buffer
	mw := multipart.NewWriter(&head)
	mw.WriteField("note", "hi")
	mw.CreateFormFile("photo", "photo.jpg")
	boundary := mw.Boundary()
	return io.MultiReader(&head, photo, strings.NewReader("\r\n--"+boundary+"--\r\n")), mw.FormDataContentType()
}

// An upload or a download that takes longer than the server's ReadTimeout
// and WriteTimeout, but keeps moving at a phone's pace, isn't cut off.
func TestSlowTransfers(t *testing.T) {
	srv, _ := transfers(t, func() io.Reader { return &slowly{chunks: 8, size: 4 << 10, pause: 50 * time.Millisecond} })
	want := strings.Repeat("x", 8*4<<10)

	res, err := http.Get(srv.URL + "/download")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || string(body) != want {
		t.Errorf("download: %d bytes, %v", len(body), err)
	}

	form, contentType := multipartBody(&slowly{chunks: 8, size: 4 << 10, pause: 50 * time.Millisecond})
	for _, tc := range []struct {
		path, contentType string
		body              io.Reader
		want              string
	}{
		{"/upload", contentType, form, fmt.Sprint("hi ", 8*4<<10)},
		{"/data", "application/json", &slowly{chunks: 8, size: 4 << 10, pause: 50 * time.Millisecond}, want},
	} {
		res, err := client.Post(srv.URL+tc.path, tc.contentType, tc.body)
		if err != nil {
			t.Errorf("%s: %v", tc.path, err)
			continue
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 || string(body) != tc.want {
			t.Errorf("%s: %d %.40q, %v", tc.path, res.StatusCode, body, err)
		}
	}
}

// A transfer past 8.6 GiB earns its time as any other: the deadline doesn't
// overflow into the past and cut it off.
func TestPaceOfAHugeTransfer(t *testing.T) {
	start := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	p := pace{start: start, timeout: time.Minute, moved: 10 << 30}
	if got, want := p.deadline(start), start.Add(time.Minute); !got.Equal(want) {
		t.Errorf("at the start: %v, want the timeout from now, %v", got, want)
	}
	later := start.Add(1000 * time.Hour)
	earned := start.Add(time.Minute + time.Duration(10<<30/minRate)*time.Second)
	if got := p.deadline(later); !got.Equal(earned) {
		t.Errorf("later: %v, want what 10 GiB earns, %v", got, earned)
	}
}

// A download from memory (a bytes.Reader hands io.Copy all of itself in one
// write) is paced as it's read too, not cut off at the timeout.
func TestSlowDownloadFromMemory(t *testing.T) {
	const size = 32 << 20
	srv, _ := transfers(t, func() io.Reader { return bytes.NewReader(make([]byte, size)) })
	res, err := client.Get(srv.URL + "/download")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var n int64
	for {
		m, err := io.CopyN(io.Discard, res.Body, 2<<20)
		n += m
		if err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n != size {
		t.Errorf("%d bytes of %d", n, size)
	}
}

// One that stops, or trickles in a byte at a time (slowloris), is cut off
// as the server's timeouts alone would cut it off: it doesn't hold the
// connection open by sending just enough.
func TestStalledTransfers(t *testing.T) {
	srv, ended := transfers(t, func() io.Reader { return strings.NewReader(strings.Repeat("x", 64<<20)) })

	trickle := &slowly{chunks: 150, size: 1, pause: 20 * time.Millisecond} // 3 s, if let
	for _, tc := range []struct {
		name string
		body func() (io.Reader, string)
	}{
		{"a stalled upload", func() (io.Reader, string) { return multipartBody(stalled(t, "some of the photo")) }},
		{"a trickled upload", func() (io.Reader, string) { return multipartBody(trickle) }},
		{"a stalled form", func() (io.Reader, string) {
			return stalled(t, "note=hi"), "application/x-www-form-urlencoded"
		}},
	} {
		// Over a connection of its own: Go's client waits on a body it's
		// still sending before it hands back the answer.
		body, contentType := tc.body()
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		fmt.Fprintf(conn, "POST /upload HTTP/1.1\r\nHost: app\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n", contentType, 1<<20)
		go io.Copy(conn, body)
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		status, err := bufio.NewReader(conn).ReadString('\n')
		took := time.Since(start)
		conn.Close()
		if err != nil || !strings.HasPrefix(status, "HTTP/1.1 408") || took > 2*time.Second {
			t.Errorf("%s: %q after %v, %v; want 408 at once", tc.name, status, took, err)
		}
	}

	// A client that stops reading a download.
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /download HTTP/1.1\r\nHost: app\r\n\r\n")
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Error("a download to a client that stopped reading never ended")
	}
}

// A malformed form is 400, and one over its limit 413, not a timeout.
func TestFormErrors(t *testing.T) {
	srv, _ := transfers(t, func() io.Reader { return strings.NewReader("") })
	for _, tc := range []struct {
		name, contentType, body string
		want                    int
	}{
		{"malformed", "multipart/form-data; boundary=x", "not a multipart body", http.StatusBadRequest},
		{"too large", "application/x-www-form-urlencoded", "note=" + strings.Repeat("x", 2<<20), http.StatusRequestEntityTooLarge},
	} {
		res, err := http.Post(srv.URL+"/upload", tc.contentType, strings.NewReader(tc.body))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, res.StatusCode, tc.want)
		}
	}
}

// A negative timeout is none, as net/http has it.
func TestNegativeServerTimeout(t *testing.T) {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Handle("GET /download", func(w http.ResponseWriter, r *http.Request) error {
		return Download(w, r, "f.bin", "application/octet-stream", strings.NewReader("the bytes"))
	})
	srv := httptest.NewUnstartedServer(rt.Handler())
	srv.Config.ReadTimeout, srv.Config.WriteTimeout = -1, -1
	srv.Start()
	defer srv.Close()
	res, err := http.Get(srv.URL + "/download")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || string(body) != "the bytes" {
		t.Errorf("%q, %v", body, err)
	}
}
