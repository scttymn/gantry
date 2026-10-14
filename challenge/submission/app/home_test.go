package app_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/testkit"

	"twelve-dollar/app"
	"twelve-dollar/test"
)

// newApp is the app on a test database.
func newApp(t *testing.T) *app.App {
	return &app.App{DB: test.DB(t), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Secret: []byte("test-secret"), Started: time.Now()}
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
	a := newApp(t)
	h := a.Handler()
	// The page links to a post: the links check follows it.
	if _, err := a.DB.Write.ExecContext(t.Context(), `INSERT INTO users (id, username) VALUES (1, 'ada'); INSERT INTO posts (id, user_id, body) VALUES (1, 1, 'hello')`); err != nil {
		t.Fatal(err)
	}
	w := get(t, h, "/")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Twelve dollar is running.") {
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
