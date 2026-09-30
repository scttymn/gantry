package testkit

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/web"
)

// site is a small app: a sign-in form, a page behind it, an API, and a
// PATCH form (method override).
func signInSite() http.Handler {
	rt := web.NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Handle("GET /login", func(w http.ResponseWriter, r *http.Request) error {
		io.WriteString(w, `<form action="/session" method="post">
			<input type="hidden" name="token" value="t0k3n">
			<input name="email" value="">
			<input type="password" name="password">
			<input type="checkbox" name="remember" value="yes" checked>
			<input type="checkbox" name="spam" value="yes">
			<select name="plan"><option value="a">A</option><option value="b" selected>B</option></select>
			<textarea name="note">hello</textarea>
			<button type="submit">Sign in</button></form>`)
		return nil
	})
	rt.Handle("POST /session", func(w http.ResponseWriter, r *http.Request) error {
		r.ParseForm()
		got := fmt.Sprintf("token=%s email=%s password=%s remember=%s spam=%s plan=%s note=%s",
			r.Form.Get("token"), r.Form.Get("email"), r.Form.Get("password"), r.Form.Get("remember"), r.Form.Get("spam"), r.Form.Get("plan"), r.Form.Get("note"))
		http.SetCookie(w, &http.Cookie{Name: "session", Value: url.QueryEscape(got), Path: "/"})
		http.Redirect(w, r, "/secret", http.StatusSeeOther)
		return nil
	})
	rt.Handle("GET /secret", func(w http.ResponseWriter, r *http.Request) error {
		c, err := r.Cookie("session")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return nil
		}
		v, _ := url.QueryUnescape(c.Value)
		fmt.Fprintf(w, `<h1 class="who">%s</h1><form action="/things/1" method="post"><input type="hidden" name="_method" value="patch"><input name="name" value="old"></form>`, v)
		return nil
	})
	rt.Handle("PATCH /things/{id}", func(w http.ResponseWriter, r *http.Request) error {
		fmt.Fprintf(w, "patched %s to %s", r.PathValue("id"), r.PostFormValue("name"))
		return nil
	})
	rt.Handle("POST /api/echo", func(w http.ResponseWriter, r *http.Request) error {
		tok, _ := web.BearerToken(r)
		var in map[string]any
		if err := web.ReadJSON(r, &in); err != nil {
			return err
		}
		return web.JSON(w, http.StatusCreated, map[string]any{"token": tok, "got": in})
	})
	return rt.Handler()
}

func TestBrowser(t *testing.T) {
	b := Browser(t, signInSite())

	page := b.Get("/secret")
	if page.Code != 302 || page.Header.Get("Location") != "/login" {
		t.Fatalf("signed out: %d %q", page.Code, page.Header.Get("Location"))
	}
	page = page.Follow()
	if page.Code != 200 || page.Find("form").Length() != 1 {
		t.Fatalf("the sign-in page: %d %s", page.Code, page.Body)
	}

	// Submit fills the form it finds, keeping what the page set (the
	// hidden token, the checked box, the selected option, the text).
	page = page.Submit("form", map[string]string{"email": "sam@example.com", "password": "secret"})
	if page.Code != 303 {
		t.Fatalf("signing in: %d %s", page.Code, page.Body)
	}
	page = page.Follow() // the session cookie goes with it
	want := "token=t0k3n email=sam@example.com password=secret remember=yes spam= plan=b note=hello"
	if got := page.Text("h1.who"); got != want {
		t.Errorf("signed in as\n %q\nwant %q", got, want)
	}

	// A form that PATCHes through _method.
	if page = page.Submit("form", map[string]string{"name": "new"}); page.Code != 200 || page.Body != "patched 1 to new" {
		t.Errorf("a PATCH form: %d %q", page.Code, page.Body)
	}

	// Headers go with every request: a bearer token, for an API.
	b.Header.Set("Authorization", "Bearer hou_abc")
	var got struct {
		Token string
		Got   map[string]any
	}
	page = b.JSON("POST", "/api/echo", map[string]string{"name": "sam"})
	if err := page.ReadJSON(&got); err != nil || page.Code != 201 || got.Token != "hou_abc" || got.Got["name"] != "sam" {
		t.Errorf("JSON: %d %+v %v", page.Code, got, err)
	}
	if page := b.JSON("POST", "/api/echo", nil); page.Code != 400 || !strings.Contains(page.Body, "error") {
		t.Errorf("no body: %d %s", page.Code, page.Body)
	}
}

// SubmitFiles sends a form with its file, multipart, keeping what the page
// set, as a browser does.
func TestSubmitFiles(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("GET /form", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<form method="post" action="/upload" enctype="multipart/form-data">
<input type="hidden" name="_method" value="put"><input name="clip[title]" value="old">
<input type="file" name="clip[thumbnail]"></form>`)
	})
	h.HandleFunc("POST /upload", func(w http.ResponseWriter, r *http.Request) {
		f, header, err := r.FormFile("clip[thumbnail]")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		data, _ := io.ReadAll(f)
		fmt.Fprintf(w, "%s %s %s %s", r.FormValue("_method"), r.FormValue("clip[title]"), header.Filename, data)
	})
	page := Browser(t, h).Get("/form").SubmitFiles("form", map[string]string{"clip[title]": "new"},
		map[string]Upload{"clip[thumbnail]": {Filename: "still.jpg", Data: []byte("jpeg bytes")}})
	if page.Code != 200 || page.Body != "put new still.jpg jpeg bytes" {
		t.Errorf("%d %q", page.Code, page.Body)
	}
}

// failures runs f with a recorder, as a test would, and is what it reported.
func failures(t *testing.T, f func(tb testing.TB)) *recorder {
	r := &recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f(r)
	}()
	<-done
	return r
}

func TestBrowserReports(t *testing.T) {
	r := failures(t, func(tb testing.TB) {
		Browser(tb, signInSite()).Get("/login").Submit("form.missing", nil)
	})
	if !r.fatal || !strings.Contains(strings.Join(r.errors, " "), `no form "form.missing"`) {
		t.Errorf("a form that isn't there: %q", r.errors)
	}
	r = failures(t, func(tb testing.TB) { Browser(tb, signInSite()).Get("/login").Follow() })
	if !r.fatal || !strings.Contains(strings.Join(r.errors, " "), "not a redirect") {
		t.Errorf("following what isn't a redirect: %q", r.errors)
	}
}

func TestClock(t *testing.T) {
	c := Clock(time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC))
	now := c.Now // what a gantry part is handed
	c.Advance(14 * 24 * time.Hour)
	if got := now(); !got.Equal(time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("after two weeks: %v", got)
	}
	c.Set(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	if now().Year() != 2030 {
		t.Errorf("after Set: %v", now())
	}
	if now() != now() {
		t.Error("time moved by itself")
	}
}

func TestHTTP(t *testing.T) {
	fake := HTTP(t)
	fake.On("GET", "https://api.example.com/v1/zones", 200, `{"zones":["a"]}`)
	fake.Handle("POST", "https://api.example.com/v1/dns", func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		return fake.Response(201, "made "+string(body)), nil
	})
	client := fake.Client()

	resp, err := client.Get("https://api.example.com/v1/zones?page=2") // any query, when the script has none
	if err != nil || resp.StatusCode != 200 || read(resp) != `{"zones":["a"]}` {
		t.Fatalf("GET: %v %v", resp, err)
	}
	resp, err = client.Post("https://api.example.com/v1/dns", "application/json", strings.NewReader("x"))
	if err != nil || resp.StatusCode != 201 || read(resp) != "made x" {
		t.Fatalf("POST: %v %v", resp, err)
	}
	if got := fake.Requests(); len(got) != 2 || got[1].Method != "POST" || got[1].URL.Path != "/v1/dns" {
		t.Errorf("recorded %v", got)
	}
}

// A request the test didn't script fails the test, and the code gets an
// error, as WebMock's default.
func TestHTTPUnscripted(t *testing.T) {
	var err error
	r := failures(t, func(tb testing.TB) {
		_, err = HTTP(tb).Client().Get("https://evil.example.com/steal")
		runtime.Goexit()
	})
	if err == nil || !errors.Is(err, ErrUnscripted) || len(r.errors) != 1 || !strings.Contains(r.errors[0], "GET https://evil.example.com/steal") {
		t.Errorf("err %v, reported %q", err, r.errors)
	}
	// Scripted for GET is not scripted for DELETE.
	r = failures(t, func(tb testing.TB) {
		fake := HTTP(tb)
		fake.On("GET", "https://api.example.com/v1/dns", 200, "{}")
		req, _ := http.NewRequest("DELETE", "https://api.example.com/v1/dns", nil)
		_, err = fake.Client().Do(req)
		runtime.Goexit()
	})
	if !errors.Is(err, ErrUnscripted) || len(r.errors) != 1 {
		t.Errorf("another method: err %v, reported %q", err, r.errors)
	}
}

func read(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(b)
}
