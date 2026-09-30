package testkit

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

// Visitor is a browser for an app's tests (Rails' integration tests): it
// keeps cookies, as a browser does, so a test signs in and goes on; sends
// Header with every request (a bearer token); and fills in and submits the
// forms it finds. The app is called in the test's process, at
// http://app.localhost (a local host: cookies aren't Secure there).
type Visitor struct {
	// Header goes with every request.
	Header http.Header
	t      testing.TB
	h      http.Handler
	jar    *cookiejar.Jar
}

const origin = "http://app.localhost"

// Browser is a Visitor for h, the app's handler.
func Browser(t testing.TB, h http.Handler) *Visitor {
	jar, _ := cookiejar.New(nil)
	return &Visitor{Header: http.Header{}, t: t, h: h, jar: jar}
}

// Response is what a request got: its status, headers and body, and the
// page to look into, follow or submit.
type Response struct {
	Code   int
	Header http.Header
	Body   string
	URL    *url.URL
	v      *Visitor
	doc    *goquery.Document
}

// Get requests path, as following a link.
func (v *Visitor) Get(path string) *Response { return v.do("GET", path, nil, "") }

// Post posts a form to path.
func (v *Visitor) Post(path string, form url.Values) *Response {
	return v.do("POST", path, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
}

// PostFiles posts a form with files to path, as multipart/form-data.
func (v *Visitor) PostFiles(path string, form url.Values, files map[string]Upload) *Response {
	v.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, values := range form {
		for _, val := range values {
			mw.WriteField(name, val)
		}
	}
	for name, f := range files {
		part, err := mw.CreateFormFile(name, f.Filename)
		if err != nil {
			v.t.Fatalf("testkit: %v", err)
		}
		part.Write(f.Data)
	}
	mw.Close()
	return v.do("POST", path, &body, mw.FormDataContentType())
}

// JSON sends body (nil for none) as JSON to path, asking for JSON back.
func (v *Visitor) JSON(method, path string, body any) *Response {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			v.t.Fatalf("testkit: %v", err)
		}
		r = bytes.NewReader(b)
	}
	return v.do(method, path, r, "application/json")
}

func (v *Visitor) do(method, target string, body io.Reader, contentType string) *Response {
	v.t.Helper()
	u, err := url.Parse(origin)
	if err != nil {
		v.t.Fatal(err)
	}
	u, err = u.Parse(target)
	if err != nil {
		v.t.Fatalf("testkit: %q isn't a path: %v", target, err)
	}
	r := httptest.NewRequest(method, u.String(), body)
	r.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if contentType == "application/json" {
		r.Header.Set("Accept", "application/json")
	}
	for k, vs := range v.Header {
		r.Header[k] = vs
	}
	for _, c := range v.jar.Cookies(u) {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	v.h.ServeHTTP(w, r)
	resp := w.Result()
	v.jar.SetCookies(u, resp.Cookies())
	b, _ := io.ReadAll(resp.Body)
	return &Response{Code: resp.StatusCode, Header: resp.Header, Body: string(b), URL: u, v: v}
}

// Find is the page's elements matching a CSS selector (Rails'
// assert_select).
func (p *Response) Find(selector string) *goquery.Selection {
	if p.doc == nil {
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(p.Body))
		if err != nil {
			p.v.t.Fatalf("testkit: %s isn't HTML: %v", p.URL.Path, err)
		}
		p.doc = doc
	}
	return p.doc.Find(selector)
}

// Text is the text of the elements matching selector, trimmed.
func (p *Response) Text(selector string) string {
	return strings.TrimSpace(p.Find(selector).Text())
}

// ReadJSON decodes the body into v.
func (p *Response) ReadJSON(v any) error { return json.Unmarshal([]byte(p.Body), v) }

// Follow requests where a redirect sends the browser (Rails'
// follow_redirect!). A response that isn't a redirect fails the test.
func (p *Response) Follow() *Response {
	p.v.t.Helper()
	loc := p.Header.Get("Location")
	if p.Code < 300 || p.Code > 399 || loc == "" {
		p.v.t.Fatalf("testkit: %s answered %d, not a redirect", p.URL.Path, p.Code)
	}
	next, err := p.URL.Parse(loc)
	if err != nil {
		p.v.t.Fatalf("testkit: a redirect to %q: %v", loc, err)
	}
	return p.v.Get(next.RequestURI())
}

// Submit fills in the first form matching selector with fields and submits
// it, as a person would: what the page already set (hidden fields, checked
// boxes, the selected option, a textarea's text) goes too, buttons don't,
// and a form without an action posts to the page it's on.
func (p *Response) Submit(selector string, fields map[string]string) *Response {
	p.v.t.Helper()
	return p.SubmitFiles(selector, fields, nil)
}

// Upload is a file a test chooses for a form's file field (Rails'
// fixture_file_upload): its bytes, from File(t, fixtures, "still.jpg").
type Upload struct {
	Filename string
	Data     []byte
}

// SubmitFiles is Submit with files chosen for the form's file fields, by
// name: the form goes as multipart/form-data, as a browser sends one with
// a file.
func (p *Response) SubmitFiles(selector string, fields map[string]string, files map[string]Upload) *Response {
	p.v.t.Helper()
	form := p.Find(selector).FilterFunction(func(_ int, s *goquery.Selection) bool { return goquery.NodeName(s) == "form" }).First()
	if form.Length() == 0 {
		form = p.Find(selector).Find("form").First()
	}
	if form.Length() == 0 {
		p.v.t.Fatalf("testkit: no form %q on %s", selector, p.URL.Path)
	}
	values := url.Values{}
	form.Find("input, select, textarea").Each(func(_ int, s *goquery.Selection) {
		name, ok := s.Attr("name")
		if !ok || name == "" {
			return
		}
		switch goquery.NodeName(s) {
		case "textarea":
			values.Add(name, s.Text())
		case "select":
			opt := s.Find("option[selected]").First()
			if opt.Length() == 0 {
				opt = s.Find("option").First()
			}
			if val, ok := opt.Attr("value"); ok {
				values.Add(name, val)
			} else {
				values.Add(name, opt.Text())
			}
		default:
			switch t, _ := s.Attr("type"); strings.ToLower(t) {
			case "submit", "button", "image", "reset", "file":
			case "checkbox", "radio":
				if _, checked := s.Attr("checked"); checked {
					val, ok := s.Attr("value")
					if !ok {
						val = "on"
					}
					values.Add(name, val)
				}
			default:
				val, _ := s.Attr("value")
				values.Add(name, val)
			}
		}
	})
	for k, val := range fields {
		values.Set(k, val)
	}
	action, _ := form.Attr("action")
	target, err := p.URL.Parse(action)
	if err != nil {
		p.v.t.Fatalf("testkit: the form's action %q: %v", action, err)
	}
	if method, _ := form.Attr("method"); strings.EqualFold(method, "post") {
		if len(files) > 0 {
			return p.v.PostFiles(target.RequestURI(), values, files)
		}
		return p.v.Post(target.RequestURI(), values)
	}
	target.RawQuery = values.Encode()
	return p.v.Get(target.RequestURI())
}
