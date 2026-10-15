package web

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	templruntime "github.com/a-h/templ/runtime"
)

func TestRouterReloadsInDevelopment(t *testing.T) {
	t.Chdir(t.TempDir())
	page := `<!doctype html><html><body><main>Page</main></body></html>`
	for _, tc := range []struct {
		env, warnings, reload string
		want                  bool
	}{
		{"development", "", "", true},
		{"development", "false", "", true},
		{"development", "", "false", false},
		{"production", "", "", false},
		{"test", "", "", false},
	} {
		t.Run(tc.env+tc.warnings+tc.reload, func(t *testing.T) {
			t.Setenv("GANTRY_ENV", tc.env)
			t.Setenv("GANTRY_DEV_WARNINGS", tc.warnings)
			t.Setenv("GANTRY_DEV_RELOAD", tc.reload)
			rt := NewRouter(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
				w.Header().Set("Content-Type", "text/html")
				_, err := io.WriteString(w, page)
				return err
			})
			h := rt.Handler()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
			if has := strings.Contains(rec.Body.String(), `src="/_gantry/reload.js"`); has != tc.want {
				t.Fatalf("script=%v body=%s", has, rec.Body.String())
			}
			if tc.want && !strings.HasSuffix(rec.Body.String(), `"></script></body></html>`) {
				t.Fatal("script isn't at the end of the body:", rec.Body.String())
			}
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/_gantry/reload.js", nil))
			if served := rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "EventSource"); served != tc.want {
				t.Fatalf("script served=%v: %d", served, rec.Code)
			}
		})
	}
}

func TestReloadInjection(t *testing.T) {
	h := &reloads{boot: "b00t"}
	page := `<html><body><script>const s = "</body>";</script><p>x</p></body></html>`
	out, ok := h.inject(context.Background(), []byte(page))
	want := `<html><body><script>const s = "</body>";</script><p>x</p><script src="/_gantry/reload.js" data-gantry-reload data-version="b00t.0" data-turbo-eval="false"></script></body></html>`
	if !ok || string(out) != want {
		t.Fatal(string(out))
	}
	if _, changed := h.inject(context.Background(), out); changed {
		t.Fatal("injected twice")
	}
	if out, ok := h.inject(context.Background(), []byte(`<html><p>no body tag</p></html>`)); !ok || !strings.Contains(string(out), `data-version="b00t.0" data-turbo-eval="false"></script></html>`) {
		t.Fatal(string(out))
	}
	for _, fragment := range []string{`<p>Turbo frame</p>`, `<turbo-stream action="append"></turbo-stream>`} {
		if _, changed := h.inject(context.Background(), []byte(fragment)); changed {
			t.Fatal("changed a fragment:", fragment)
		}
	}
}

// readEvent reads one server-sent event's name and data, skipping comments.
func readEvent(t *testing.T, r *bufio.Reader) (name, data string) {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "":
			if name != "" {
				return name, data
			}
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
}

func TestReloadStream(t *testing.T) {
	text := filepath.Join(t.TempDir(), "page_templ.txt")
	if err := os.WriteFile(text, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &reloads{boot: "b00t", interval: 10 * time.Millisecond, files: func() []string { return []string{text} }}
	srv := httptest.NewServer(h.handler(http.NotFoundHandler()))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/_gantry/reload")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatal(ct)
	}
	events := bufio.NewReader(res.Body)
	if name, data := readEvent(t, events); name != "version" || data != "b00t.0" {
		t.Fatal(name, data)
	}

	// templ rewrote a template's text: the server reads it on the next
	// render, so the version changes with no restart.
	later := time.Now().Add(time.Second)
	if err := os.WriteFile(text, []byte("new text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(text, later, later); err != nil {
		t.Fatal(err)
	}
	if name, data := readEvent(t, events); name != "version" || data != "b00t.1" {
		t.Fatal(name, data)
	}
	// A page rendered now has the new version.
	if out, _ := h.inject(context.Background(), []byte(`<html><body></body></html>`)); !strings.Contains(string(out), `data-version="b00t.1"`) {
		t.Fatal(string(out))
	}
}

func TestReloadStreamEndsOnShutdown(t *testing.T) {
	h := &reloads{boot: "b00t", interval: time.Hour, files: func() []string { return nil }}
	srv := httptest.NewUnstartedServer(h.handler(http.NotFoundHandler()))
	srv.Start()
	res, err := http.Get(srv.URL + "/_gantry/reload")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	readEvent(t, bufio.NewReader(res.Body))

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- srv.Config.Shutdown(ctx)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("shutdown waited on the stream:", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown is waiting on the stream")
	}
}

func TestTemplTextFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"app/home/index.templ", "app/home/index_templ.go", ".cache/dep/skip.templ", "_tmp/skip.templ"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TEMPL_DEV_MODE_WATCH_ROOT", root)
	t.Setenv("TEMPL_DEV_MODE", "")
	if files := templTextFiles(); files != nil {
		t.Fatal("watching without templ:", files)
	}
	t.Setenv("TEMPL_DEV_MODE", "true")
	want := templruntime.GetDevModeTextFileName(filepath.Join(root, "app/home/index.templ"))
	if files := templTextFiles(); len(files) != 1 || files[0] != want {
		t.Fatal(files, "want", want)
	}
}

// A tab that was hidden had no stream open while the text changed: its
// stream, when it's shown again, still says the version changed.
func TestReloadSeesChangesWhileNoStreamIsOpen(t *testing.T) {
	text := filepath.Join(t.TempDir(), "templ_page.txt")
	if err := os.WriteFile(text, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &reloads{boot: "b00t", interval: 10 * time.Millisecond, files: func() []string { return []string{text} }}
	if out, _ := h.inject(context.Background(), []byte(`<html><body></body></html>`)); !strings.Contains(string(out), `data-version="b00t.0"`) {
		t.Fatal(string(out))
	}
	later := time.Now().Add(time.Second)
	if err := os.WriteFile(text, []byte("new text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(text, later, later); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(h.handler(http.NotFoundHandler()))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/_gantry/reload")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	events := bufio.NewReader(res.Body)
	readEvent(t, events)
	if name, data := readEvent(t, events); name != "version" || data != "b00t.1" {
		t.Fatal(name, data)
	}
}
