package web

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func FuzzSent(f *testing.F) {
	f.Add("post", "post[title]=Hello&post[body]=World")
	f.Add("post", "post[]=x&other[a]=b&%zz=1")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, param, body string) {
		req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		_ = Sent(req, param)
	})
}

func FuzzProxies(f *testing.F) {
	f.Add("127.0.0.1:1234", "203.0.113.9, 10.0.0.1", "https", "example.com", "127.0.0.0/8,10.0.0.0/8")
	f.Add("198.51.100.5:80", "1.2.3.4", "https", "evil.example", "")
	f.Add("[::1]:443", "::1, 2001:db8::1", "http", "bad host/path", "invalid-cidr")
	f.Fuzz(func(t *testing.T, remoteAddr, xff, proto, host, trustedSpec string) {
		_, _ = ParseProxies(trustedSpec)
		p := Proxies{Trusted: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, ForwardedHost: true}
		rt := &Router{Proxies: p}
		req := WithCurrent(httptest.NewRequest(http.MethodGet, "http://app.localhost/", nil))
		req.RemoteAddr = remoteAddr
		req.Header.Set("X-Forwarded-For", xff)
		req.Header.Set("X-Forwarded-Proto", proto)
		req.Header.Set("X-Forwarded-Host", host)
		h := rt.arrive(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = ClientIP(r)
			_ = Scheme(r)
			_ = RequestHost(r)
		}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
	})
}

func FuzzPageCache(f *testing.F) {
	f.Add("app.localhost", "/news", "page=1", "", "")
	f.Add("other.localhost", "/news", "a=1&b=2", "session=secret", "")
	f.Add("app.localhost", "/news", "", "", "Bearer tok")
	f.Fuzz(func(t *testing.T, host, path, query, cookie, authz string) {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		if strings.ContainsAny(path, " \r\n\t") || strings.ContainsAny(query, " \r\n\t") || strings.ContainsAny(host, " \r\n\t/\\") {
			return
		}
		target := path
		if query != "" {
			target += "?" + query
		}
		req := WithCurrent(httptest.NewRequest(http.MethodGet, "http://app.localhost/", nil))
		req.Host = host
		req.URL.Path = path
		req.URL.RawQuery = query
		if cookie != "" && !strings.ContainsAny(cookie, "\r\n") {
			req.Header.Set("Cookie", cookie)
		}
		if authz != "" && !strings.ContainsAny(authz, "\r\n") {
			req.Header.Set("Authorization", authz)
		}
		cache := &PageCache{}
		called := 0
		h := cache.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called++
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(target))
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if len(req.Header.Values("Authorization")) > 0 {
			rec2 := httptest.NewRecorder()
			h.ServeHTTP(rec2, req)
			if called != 2 {
				t.Fatalf("authorized request was cached (called=%d)", called)
			}
		}
	})
}
