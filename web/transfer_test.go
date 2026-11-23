package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// slowly is a body that arrives in chunks with a pause before each: a phone's
// upload, or a large file to one.
type slowly struct {
	chunks int
	pause  time.Duration
}

func (s *slowly) Read(p []byte) (int, error) {
	if s.chunks == 0 {
		return 0, io.EOF
	}
	time.Sleep(s.pause)
	s.chunks--
	return copy(p, "chunk"), nil
}

// An upload or a download that takes longer than the server's ReadTimeout
// and WriteTimeout, but keeps moving, isn't cut off by them.
func TestSlowTransfers(t *testing.T) {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Handle("GET /download", func(w http.ResponseWriter, r *http.Request) error {
		return Download(w, r, "big.bin", "application/octet-stream", &slowly{chunks: 5, pause: 60 * time.Millisecond})
	})
	rt.Handle("POST /upload", func(w http.ResponseWriter, r *http.Request) error {
		_, err := io.WriteString(w, r.PostFormValue("note"))
		return err
	})
	rt.Handle("POST /data", func(w http.ResponseWriter, r *http.Request) error {
		_, err := io.Copy(w, r.Body)
		return err
	})
	srv := httptest.NewUnstartedServer(rt.Handler())
	srv.Config.ReadTimeout, srv.Config.WriteTimeout = 100*time.Millisecond, 100*time.Millisecond
	srv.Start()
	defer srv.Close()
	want := strings.Repeat("chunk", 5)

	res, err := http.Get(srv.URL + "/download")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || string(body) != want {
		t.Errorf("download: %q, %v", body, err)
	}

	for _, tc := range []struct{ path, contentType, prefix string }{
		{"/upload", "application/x-www-form-urlencoded", "note="},
		{"/data", "application/json", ""},
	} {
		res, err := http.Post(srv.URL+tc.path, tc.contentType, io.MultiReader(strings.NewReader(tc.prefix), &slowly{chunks: 5, pause: 60 * time.Millisecond}))
		if err != nil {
			t.Errorf("%s: %v", tc.path, err)
			continue
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 || string(body) != want {
			t.Errorf("%s: %d %q, %v", tc.path, res.StatusCode, body, err)
		}
	}
}
