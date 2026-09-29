package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// public is an app's public folder with a 404 page, and no 403.
type public map[string]string

func (p public) Public(name string) ([]byte, bool) {
	b, ok := p[name]
	return []byte(b), ok
}

func answersApp(t *testing.T, page ErrorPage) (http.Handler, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	rt := NewRouter(slog.New(slog.NewTextHandler(&logs, nil)), page)
	rt.Public = public{"404.html": "<h1>static 404</h1>"}
	rt.Handle("GET /busy", func(w http.ResponseWriter, r *http.Request) error {
		return Status(http.StatusConflict, errors.New("already deploying"))
	})
	rt.Handle("GET /broken", func(w http.ResponseWriter, r *http.Request) error {
		return errors.New("disk full at /var/lib/secret")
	})
	rt.Handle("GET /invalid", func(w http.ResponseWriter, r *http.Request) error {
		return Invalid{"name": {"can't be blank"}, "email": {"is taken"}}
	})
	rt.Handle("GET /forbidden", func(w http.ResponseWriter, r *http.Request) error {
		return Status(http.StatusForbidden, nil)
	})
	rt.Handle("GET /panic", func(w http.ResponseWriter, r *http.Request) error { panic("boom") })
	rt.Handle("POST /echo", func(w http.ResponseWriter, r *http.Request) error {
		var in struct{ Name string }
		if err := ReadJSONLimit(r, &in, 64); err != nil {
			return err
		}
		return JSON(w, http.StatusCreated, map[string]string{"hello": in.Name})
	})
	return rt.Handler(), &logs
}

func ask(h http.Handler, method, path, accept, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if accept != "" {
		r.Header.Set("Accept", accept)
	}
	h.ServeHTTP(w, r)
	return w
}

func TestJSONAnswers(t *testing.T) {
	h, logs := answersApp(t, nil)
	for _, tc := range []struct {
		path string
		code int
		want string
	}{
		{"/nope", 404, `{"error":"Not Found"}`},
		{"/busy", 409, `{"error":"already deploying"}`},       // the handler's own message
		{"/broken", 500, `{"error":"Internal Server Error"}`}, // never the detail
		{"/forbidden", 403, `{"error":"Forbidden"}`},
		{"/invalid", 422, `{"errors":{"email":["is taken"],"name":["can't be blank"]}}`},
		{"/panic", 500, `{"error":"Internal Server Error"}`},
	} {
		w := ask(h, "GET", tc.path, "application/json", "")
		if w.Code != tc.code || strings.TrimSpace(w.Body.String()) != tc.want || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
			t.Errorf("%s = %d %s %q, want %d %s", tc.path, w.Code, w.Header().Get("Content-Type"), w.Body, tc.code, tc.want)
		}
	}
	if !strings.Contains(logs.String(), "disk full at /var/lib/secret") {
		t.Error("a 500's detail wasn't logged")
	}
	// A .json path is a JSON client too.
	if w := ask(h, "GET", "/nope.json", "", ""); !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("/nope.json = %q", w.Body)
	}
}

func TestErrorPagesStatic(t *testing.T) {
	h, _ := answersApp(t, nil)
	if w := ask(h, "GET", "/nope", "text/html", ""); w.Code != 404 || w.Body.String() != "<h1>static 404</h1>" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Errorf("the app's 404.html: %d %q", w.Code, w.Body)
	}
	// No 403.html: the router's own page, naming the status.
	if w := ask(h, "GET", "/forbidden", "text/html", ""); w.Code != 403 || !strings.Contains(w.Body.String(), "Forbidden") {
		t.Errorf("a generic page: %d %q", w.Code, w.Body)
	}
	// Neither a page nor data (an image): the bare status, as before.
	if w := ask(h, "GET", "/nope", "image/png", ""); w.Code != 404 || w.Body.Len() != 0 {
		t.Errorf("an image request: %d %q", w.Code, w.Body)
	}
}

// With neither a public folder nor a page of its own, a browser still gets
// a page, the router's.
func TestErrorPageBare(t *testing.T) {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	if w := ask(rt.Handler(), "GET", "/nope", "text/html", ""); w.Code != 404 || !strings.Contains(w.Body.String(), "<h1>Not Found</h1>") {
		t.Errorf("= %d %q", w.Code, w.Body)
	}
}

// The app's own error page comes first; if it fails, the static page.
func TestErrorPageFallsBack(t *testing.T) {
	h, _ := answersApp(t, func(w http.ResponseWriter, r *http.Request, status int) {
		io.WriteString(w, "the app's own page")
	})
	if w := ask(h, "GET", "/nope", "text/html", ""); w.Body.String() != "the app's own page" {
		t.Errorf("the app's page: %q", w.Body)
	}
	h, logs := answersApp(t, func(w http.ResponseWriter, r *http.Request, status int) {
		panic("the database is down")
	})
	if w := ask(h, "GET", "/nope", "text/html", ""); w.Code != 404 || w.Body.String() != "<h1>static 404</h1>" {
		t.Errorf("after the app's page failed: %d %q", w.Code, w.Body)
	}
	if !strings.Contains(logs.String(), "the database is down") {
		t.Error("the page's failure wasn't logged")
	}
}

func TestReadJSON(t *testing.T) {
	h, _ := answersApp(t, nil)
	for _, tc := range []struct {
		body string
		code int
		want string
	}{
		{`{"name":"sam","extra":1}`, 201, `{"hello":"sam"}`}, // unknown fields ignored
		{`{"name":`, 400, `"error"`},
		{``, 400, `the request has no JSON`},
		{`{"name":"` + strings.Repeat("x", 100) + `"}`, 413, `"error"`}, // over its limit
	} {
		w := ask(h, "POST", "/echo", "application/json", tc.body)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%.20q = %d %q, want %d", tc.body, w.Code, w.Body, tc.code)
		}
	}
}

func TestJSON(t *testing.T) {
	w := httptest.NewRecorder()
	if err := JSON(w, 202, struct {
		N int `json:"n"`
	}{3}); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if json.Unmarshal(w.Body.Bytes(), &got); w.Code != 202 || got["n"] != 3 || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("%d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
}
