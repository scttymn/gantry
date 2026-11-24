package web

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func secured(s Security) http.Handler {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Security = s
	rt.Public = public{"404.html": "<style>h1{color:red}</style><h1>Not here</h1>"}
	rt.Cache = &PageCache{}
	rt.Handle("GET /{$}", func(w http.ResponseWriter, r *http.Request) error {
		_, err := w.Write([]byte("<h1>home</h1>"))
		return err
	})
	rt.Cached("GET /posts/{id}", func(w http.ResponseWriter, r *http.Request) error { return NotFound })
	return rt.Handler()
}

func get(h http.Handler, path, peer string, https bool, header ...string) http.Header {
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr = peer + ":40000"
	r.Header.Set("Accept", "text/html")
	if https {
		r.TLS = &tls.ConnectionState{}
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Header()
}

func TestHSTS(t *testing.T) {
	h := secured(Security{HSTS: 365 * 24 * time.Hour})
	for _, tc := range []struct {
		name, peer string
		https      bool
		header     []string
		want       string
	}{
		{"over TLS", "198.51.100.7", true, nil, "max-age=31536000"},
		{"https at the proxy", "10.0.0.2", false, []string{"X-Forwarded-Proto", "https"}, "max-age=31536000"},
		{"over http", "198.51.100.7", false, nil, ""},
		{"http at the proxy", "10.0.0.2", false, []string{"X-Forwarded-Proto", "http"}, ""},
		{"a forged https", "198.51.100.7", false, []string{"X-Forwarded-Proto", "https"}, ""},
	} {
		if got := get(h, "/", tc.peer, tc.https, tc.header...).Get("Strict-Transport-Security"); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := get(secured(Security{}), "/", "198.51.100.7", true).Get("Strict-Transport-Security"); got != "" {
		t.Errorf("sent with no HSTS: %q", got)
	}
	if got := get(secured(Security{HSTS: 500 * time.Millisecond}), "/", "198.51.100.7", true).Get("Strict-Transport-Security"); got != "" {
		t.Errorf("sent for half a second: %q", got)
	}
}

func TestCSP(t *testing.T) {
	const policy = "default-src 'self'"
	for _, tc := range []struct {
		s          Security
		send, omit string
	}{
		{Security{CSP: policy}, "Content-Security-Policy", "Content-Security-Policy-Report-Only"},
		{Security{CSP: policy, CSPReportOnly: true}, "Content-Security-Policy-Report-Only", "Content-Security-Policy"},
	} {
		h := secured(tc.s)
		page := get(h, "/", "198.51.100.7", true)
		if page.Get(tc.send) != policy || page.Get(tc.omit) != "" {
			t.Errorf("%s: %v", tc.send, page)
		}
		for _, path := range []string{"/nope", "/posts/1"} {
			notFound := get(h, path, "198.51.100.7", true)
			if got := notFound.Get(tc.send); got != staticPagePolicy || notFound.Get(tc.omit) != "" {
				t.Errorf("%s on %s's static 404 page: %q", tc.send, path, got)
			}
		}
	}
	none := secured(Security{})
	for _, path := range []string{"/", "/nope"} {
		h := get(none, path, "198.51.100.7", true)
		if h.Get("Content-Security-Policy") != "" || h.Get("Content-Security-Policy-Report-Only") != "" {
			t.Errorf("%s: a policy with none set: %v", path, h)
		}
	}
	if !strings.Contains(staticPagePolicy, "style-src 'self' 'unsafe-inline'") || strings.Contains(staticPagePolicy, "script-src 'unsafe-inline'") {
		t.Errorf("the static pages' policy: %s", staticPagePolicy)
	}
}

func TestHeadersAlone(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.TLS = &tls.ConnectionState{}
	Headers(http.NotFoundHandler()).ServeHTTP(w, r)
	h := w.Header()
	if h.Get("X-Frame-Options") != "SAMEORIGIN" || h.Get("Strict-Transport-Security") != "" || h.Get("Content-Security-Policy") != "" {
		t.Errorf("%v", h)
	}
}

func TestContentSecurityPolicy(t *testing.T) {
	got := ContentSecurityPolicy("'sha256-map' 'sha256-entry'", "'sha256-styles'")
	want := "default-src 'self'; script-src 'self' 'sha256-map' 'sha256-entry'; style-src 'self' 'sha256-styles' " + turboProgressBar +
		"; img-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'self'"
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
	if got := ContentSecurityPolicy("", " "); !strings.HasPrefix(got, "default-src 'self'; script-src 'self'; style-src 'self' "+turboProgressBar+";") {
		t.Errorf("no hashes: %s", got)
	}
}

func TestTurboProgressBarHash(t *testing.T) {
	for _, file := range []string{
		"../cmd/gantry/templates/new/assets/js/vendor/@hotwired--turbo.js",
		"../bench/feed/assets/js/vendor/@hotwired--turbo.js",
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile("class ProgressBar\\{static animationDuration=(\\d+);static get defaultCSS\\(\\)\\{return u`([^`]*)`").FindSubmatch(src)
		if m == nil {
			t.Fatalf("%s: no ProgressBar.defaultCSS", file)
		}
		duration, _ := strconv.Atoi(string(m[1]))
		css := strings.NewReplacer("${ProgressBar.animationDuration}", strconv.Itoa(duration),
			"${ProgressBar.animationDuration/2}", strconv.Itoa(duration/2)).Replace(string(m[2]))
		if strings.Contains(css, "${") {
			t.Fatalf("%s: an expression left in %q", file, css)
		}
		lines := strings.Split(strings.TrimPrefix(css, "\n"), "\n")
		indent := len(lines[0]) - len(strings.TrimLeft(lines[0], " \t"))
		for i, line := range lines {
			lines[i] = line[min(indent, len(line)):]
		}
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		if got := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"; got != turboProgressBar {
			t.Errorf("%s: Turbo's progress bar is %s, not %s", file, got, turboProgressBar)
		}
	}
}
