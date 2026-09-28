package web

import (
	"bytes"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// posts has every action; drafts only some.
type posts struct{}

func (posts) Index(w http.ResponseWriter, r *http.Request) error { return say(w, "index") }
func (posts) New(w http.ResponseWriter, r *http.Request) error   { return say(w, "new") }
func (posts) Create(w http.ResponseWriter, r *http.Request) error {
	return say(w, "create "+Sent(r, "post")["title"])
}
func (posts) Show(w http.ResponseWriter, r *http.Request) error {
	return say(w, "show "+r.PathValue("id"))
}
func (posts) Edit(w http.ResponseWriter, r *http.Request) error {
	return say(w, "edit "+r.PathValue("id"))
}
func (posts) Update(w http.ResponseWriter, r *http.Request) error {
	return say(w, "update "+r.PathValue("id"))
}
func (posts) Delete(w http.ResponseWriter, r *http.Request) error {
	return say(w, "delete "+r.PathValue("id"))
}

type drafts struct{}

func (drafts) Index(w http.ResponseWriter, r *http.Request) error { return say(w, "drafts") }

func say(w http.ResponseWriter, s string) error { _, err := io.WriteString(w, s); return err }

func TestResources(t *testing.T) {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	wrapped := 0
	rt.Resources("/admin/posts", posts{}, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { wrapped++; h.ServeHTTP(w, r) })
	})
	rt.Resources("/drafts", drafts{}, nil)
	h := rt.Handler()
	for _, c := range []struct{ method, path, body, want string }{
		{"GET", "/admin/posts", "", "index"},
		{"GET", "/admin/posts/", "", "index"},
		{"GET", "/admin/posts/new", "", "new"},
		{"POST", "/admin/posts", "post%5Btitle%5D=Hi", "create Hi"},
		{"GET", "/admin/posts/7", "", "show 7"},
		{"GET", "/admin/posts/7/edit", "", "edit 7"},
		{"POST", "/admin/posts/7", "_method=patch", "update 7"},
		{"POST", "/admin/posts/7", "_method=put", "update 7"},
		{"POST", "/admin/posts/7", "_method=delete", "delete 7"},
		{"GET", "/drafts", "", "drafts"},
	} {
		var body io.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		}
		if rec := do(h, c.method, c.path, body); rec.Body.String() != c.want {
			t.Errorf("%s %s: %d %q, want %q", c.method, c.path, rec.Code, rec.Body, c.want)
		}
	}
	if wrapped != 9 {
		t.Errorf("wrap ran %d times", wrapped)
	}
	t.Run("an action the controller doesn't have isn't routed", func(t *testing.T) {
		if rec := do(h, "GET", "/drafts/new", nil); rec.Code != 404 {
			t.Errorf("%d", rec.Code)
		}
	})
}

func TestSent(t *testing.T) {
	t.Run("only the param's fields, and only those sent", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/", strings.NewReader("pillar%5Btitle%5D=Fitness&pillar%5Bposition%5D=&other%5Btitle%5D=x&pillarx=y"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		got := Sent(r, "pillar")
		if len(got) != 2 || got["title"] != "Fitness" || got["position"] != "" {
			t.Fatalf("%v", got)
		}
		if _, sent := got["body"]; sent {
			t.Error("a field not sent is there")
		}
	})
	t.Run("a repeated field is its last value, as a ticked checkbox after its hidden 0", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/", strings.NewReader("site%5Bvisible%5D=0&site%5Bvisible%5D=1"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if got := Sent(r, "site")["visible"]; got != "1" {
			t.Errorf("%q", got)
		}
	})
	t.Run("a multipart form's fields and file", func(t *testing.T) {
		var b bytes.Buffer
		m := multipart.NewWriter(&b)
		m.WriteField("staff_member[name]", "Chad")
		f, _ := m.CreateFormFile("staff_member[photo]", "chad.jpg")
		f.Write([]byte("jpeg bytes"))
		m.WriteField("staff_member[empty]", "")
		m.Close()
		r := httptest.NewRequest("POST", "/", &b)
		r.Header.Set("Content-Type", m.FormDataContentType())
		r.ParseMultipartForm(1 << 20)
		if got := Sent(r, "staff_member"); got["name"] != "Chad" || len(got) != 2 {
			t.Errorf("%v", got)
		}
		if name, data, ok := Upload(r, "staff_member[photo]"); !ok || name != "chad.jpg" || string(data) != "jpeg bytes" {
			t.Errorf("%s %q %v", name, data, ok)
		}
		if _, _, ok := Upload(r, "staff_member[none]"); ok {
			t.Error("a file that wasn't sent")
		}
	})
}

func TestSentence(t *testing.T) {
	for want, items := range map[string][]string{"": nil, "a": {"a"}, "a and b": {"a", "b"}, "a, b, and c": {"a", "b", "c"}} {
		if got := Sentence(items); got != want {
			t.Errorf("%v: %q", items, got)
		}
	}
}
