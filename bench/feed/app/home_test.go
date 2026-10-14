package app_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/jobs"
	"github.com/scttymn/gantry/live"
	"github.com/scttymn/gantry/mail"
	"github.com/scttymn/gantry/sign"
	"github.com/scttymn/gantry/testkit"

	"feed/app"
	"feed/test"
)

// newApp is the app on a test database, its jobs defined; they run when
// the test calls a.Jobs.Drain.
func newApp(t *testing.T) *app.App {
	t.Setenv("DATA_DIR", t.TempDir())
	d := test.DB(t)
	mailer, mailErr := mail.Open(t.Context(), "test", t.TempDir(), mail.Config{}, nil)
	if mailErr != nil {
		t.Fatal(mailErr)
	}
	t.Cleanup(func() { mailer.Close() })
	q, err := jobs.New(t.Context(), d, jobs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	signer := sign.Signer{Key: []byte("test-key")}
	a := &app.App{Mail: mailer, DB: d, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Signer: signer, Jobs: q, Live: live.New(signer, live.Options{}),
		Secret: []byte("test-secret"), Started: time.Now()}
	if err := a.DefineJobs(); err != nil {
		t.Fatal(err)
	}
	return a
}

func handler(t *testing.T) http.Handler { return newApp(t).Handler() }

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Accept", "text/html")
	h.ServeHTTP(w, r)
	return w
}

func TestHome(t *testing.T) {
	h := handler(t)
	w := get(t, h, "/")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Feed is running.") {
		t.Fatalf("GET / = %d\n%s", w.Code, w.Body)
	}
	// Every stylesheet, script and image the page refers to loads.
	testkit.Links(t, h, testkit.Page{Path: "/"})
}

func TestUp(t *testing.T) {
	if w := get(t, handler(t), "/up"); w.Code != http.StatusOK {
		t.Fatalf("GET /up = %d", w.Code)
	}
}

// A path with no route is the API's 404, as JSON.
func TestNotFound(t *testing.T) {
	w := get(t, handler(t), "/no-such-page")
	if w.Code != http.StatusNotFound || strings.TrimSpace(w.Body.String()) != `{"error":"not found"}` {
		t.Fatalf("a missing path = %d\n%s", w.Code, w.Body)
	}
}

// The bench's own routes (the profiler, what the process has used) are in a
// build with the bench tag, on a port of their own: an ordinary build
// serves nothing under /debug.
func TestNoDebugRoutes(t *testing.T) {
	h := handler(t)
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/profile", "/debug/usage"} {
		if w := get(t, h, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d", path, w.Code)
		}
	}
}
