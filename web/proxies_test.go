package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// seen is what a handler behind the router learns of the request.
func seen(w http.ResponseWriter, r *http.Request) error {
	fmt.Fprintf(w, "ip=%s scheme=%s host=%s xff=%q proto=%q", ClientIP(r), Scheme(r), RequestHost(r),
		r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Forwarded-Proto"))
	return nil
}

func from(h http.Handler, peer string, header http.Header) string {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/who", nil)
	r.RemoteAddr = peer + ":40000"
	r.Host = "app.example.com"
	for k, v := range header {
		r.Header[k] = v
	}
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		return fmt.Sprint(w.Code)
	}
	return w.Body.String()
}

func TestProxies(t *testing.T) {
	router := func(p Proxies) http.Handler {
		rt := NewRouter(slog.New(slog.DiscardHandler), nil)
		rt.Proxies = p
		rt.Handle("GET /who", seen)
		rt.Constraint(Host("admin.example.com"), func(s *Scope) { s.Handle("GET /admin", sayp("admin")) })
		return rt.Handler()
	}
	forged := http.Header{"X-Forwarded-For": {"203.0.113.9"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"admin.example.com"}}

	h := router(Proxies{})
	for _, tc := range []struct {
		name, peer string
		header     http.Header
		want       string
	}{
		{"direct, forged headers ignored and removed", "198.51.100.7", forged,
			`ip=198.51.100.7 scheme=http host=app.example.com xff="" proto=""`},
		{"through a private proxy", "10.0.0.2", http.Header{"X-Forwarded-For": {"1.2.3.4, 203.0.113.9, 10.0.0.9"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"Shop.Example.com:443"}},
			`ip=203.0.113.9 scheme=https host=shop.example.com xff="1.2.3.4, 203.0.113.9, 10.0.0.9" proto="https"`},
		{"a private proxy, no header", "172.18.0.2", nil,
			`ip=172.18.0.2 scheme=http host=app.example.com xff="" proto=""`},
	} {
		if got := from(h, tc.peer, tc.header); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}

	// A forwarded host is the host constraints see, only from a proxy.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin", nil)
	r.RemoteAddr, r.Host, r.Header = "10.0.0.2:1", "app.example.com", http.Header{"X-Forwarded-Host": {"admin.example.com"}}
	h.ServeHTTP(w, r)
	if w.Body.String() != "admin" {
		t.Errorf("through a proxy: %d %q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.RemoteAddr = "198.51.100.7:1"
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Errorf("a forged host reached the admin: %d", w.Code)
	}

	// A provider's own header (Cloudflare's), trusted from a proxy only.
	cf := router(Proxies{ClientIP: "CF-Connecting-IP"})
	header := http.Header{"Cf-Connecting-Ip": {"203.0.113.50"}, "X-Forwarded-For": {"198.51.100.1"}}
	if got := from(cf, "127.0.0.1", header); !strings.HasPrefix(got, "ip=203.0.113.50 ") {
		t.Errorf("CF-Connecting-IP from the tunnel: %s", got)
	}
	if got := from(cf, "198.51.100.7", header); !strings.HasPrefix(got, "ip=198.51.100.7 ") {
		t.Errorf("CF-Connecting-IP forged: %s", got)
	}

	// The app's own list of proxies, in place of the private networks.
	own := router(Proxies{Trusted: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}})
	if got := from(own, "203.0.113.5", http.Header{"X-Forwarded-For": {"198.51.100.20"}}); !strings.HasPrefix(got, "ip=198.51.100.20 ") {
		t.Errorf("a listed proxy: %s", got)
	}
	if got := from(own, "10.0.0.2", http.Header{"X-Forwarded-For": {"198.51.100.20"}}); !strings.HasPrefix(got, "ip=10.0.0.2 ") {
		t.Errorf("a private address that isn't on the list was trusted: %s", got)
	}
}

// Outside a router, ClientIP works it out with the default rule.
func TestClientIPOutsideARouter(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:1"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := ClientIP(r); got != "203.0.113.9" {
		t.Errorf("= %s", got)
	}
}

func TestHosts(t *testing.T) {
	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Hosts = []string{"example.com", ".shop.example.com"}
	rt.Handle("GET /who", seen)
	h := rt.Handler()
	for host, want := range map[string]int{
		"example.com":          200,
		"EXAMPLE.com:8080":     200,
		"shop.example.com":     200, // .shop.example.com: it and its subdomains
		"eu.shop.example.com":  200,
		"evil.com":             403,
		"example.com.evil.com": 403,
		"evilshop.example.com": 403, // ends with it, but isn't a subdomain
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/who", nil)
		r.Host = host
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s = %d, want %d", host, w.Code, want)
		}
	}
	// The health check answers whatever the host: a proxy asks by address.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/up", nil)
	r.Host = "10.0.0.5:8080"
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("/up = %d", w.Code)
	}
}
