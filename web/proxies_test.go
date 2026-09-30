package web

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the zones, wherever the tests run
)

// seen is what a handler behind the router learns of the request.
func seen(w http.ResponseWriter, r *http.Request) error {
	fmt.Fprintf(w, "ip=%s scheme=%s host=%s xff=%q proto=%q xfh=%q", ClientIP(r), Scheme(r), RequestHost(r),
		r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Forwarded-Proto"), r.Header.Get("X-Forwarded-Host"))
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

	h := router(Proxies{ForwardedHost: true})
	for _, tc := range []struct {
		name, peer string
		header     http.Header
		want       string
	}{
		{"direct, forged headers ignored and removed", "198.51.100.7", forged,
			`ip=198.51.100.7 scheme=http host=app.example.com xff="" proto="" xfh=""`},
		{"through a private proxy", "10.0.0.2", http.Header{"X-Forwarded-For": {"1.2.3.4, 203.0.113.9, 10.0.0.9"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-Host": {"Shop.Example.com:443"}},
			`ip=203.0.113.9 scheme=https host=shop.example.com xff="1.2.3.4, 203.0.113.9, 10.0.0.9" proto="https" xfh="Shop.Example.com:443"`},
		{"a private proxy, no header", "172.18.0.2", nil,
			`ip=172.18.0.2 scheme=http host=app.example.com xff="" proto="" xfh=""`},
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

	// Without ForwardedHost, a proxy's X-Forwarded-Host is a visitor's own
	// (Cloudflare passes it through): ignored and removed.
	keep := router(Proxies{})
	if got := from(keep, "10.0.0.2", http.Header{"X-Forwarded-Host": {"admin.example.com"}, "X-Forwarded-Proto": {"https"}}); got != `ip=10.0.0.2 scheme=https host=app.example.com xff="" proto="https" xfh=""` {
		t.Errorf("a forwarded host believed: %s", got)
	}
	w = httptest.NewRecorder()
	r.RemoteAddr = "10.0.0.2:1"
	keep.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Errorf("a proxy's forwarded host reached the admin: %d", w.Code)
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

// Proxies by name: a container's name on a Docker network, looked up at
// most once a minute. Only its address is trusted, not the network's; a
// failed lookup trusts no one.
func TestProxiesByName(t *testing.T) {
	var (
		lookups int
		addrs   = []netip.Addr{netip.MustParseAddr("172.18.0.5")}
		failing bool
		now     = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	)
	defer func(l func(string) ([]netip.Addr, error), c func() time.Time) { lookupProxy, proxyClock = l, c }(lookupProxy, proxyClock)
	proxyNames.Clear()
	lookupProxy = func(name string) ([]netip.Addr, error) {
		lookups++
		if name != "cloudflared" {
			t.Errorf("looked up %q", name)
		}
		if failing {
			return nil, fmt.Errorf("no such host")
		}
		return addrs, nil
	}
	proxyClock = func() time.Time { return now }

	rt := NewRouter(slog.New(slog.DiscardHandler), nil)
	rt.Proxies = Proxies{Names: []string{"cloudflared"}, ClientIP: "Cf-Connecting-Ip"}
	rt.Handle("GET /who", seen)
	h := rt.Handler()
	tunnel := http.Header{"Cf-Connecting-Ip": {"203.0.113.50"}, "X-Forwarded-Proto": {"https"}}
	if got := from(h, "172.18.0.5", tunnel); !strings.HasPrefix(got, "ip=203.0.113.50 scheme=https ") {
		t.Errorf("from cloudflared: %s", got)
	}
	if got := from(h, "172.18.0.9", tunnel); !strings.HasPrefix(got, "ip=172.18.0.9 scheme=http ") {
		t.Errorf("another container on its network: %s", got)
	}
	if got := from(h, "127.0.0.1", tunnel); !strings.HasPrefix(got, "ip=127.0.0.1 scheme=http ") {
		t.Errorf("loopback, trusted by default but not with Names: %s", got)
	}
	if lookups != 1 {
		t.Errorf("%d lookups in a minute, want 1", lookups)
	}

	// cloudflared restarts at another address: seen after the minute.
	addrs = []netip.Addr{netip.MustParseAddr("172.18.0.6")}
	now = now.Add(61 * time.Second)
	if got := from(h, "172.18.0.6", tunnel); !strings.HasPrefix(got, "ip=203.0.113.50 ") {
		t.Errorf("after a restart: %s", got)
	}
	failing = true
	now = now.Add(61 * time.Second)
	if got := from(h, "172.18.0.6", tunnel); !strings.HasPrefix(got, "ip=172.18.0.6 ") {
		t.Errorf("a failed lookup trusted: %s", got)
	}
	if lookups != 3 {
		t.Errorf("%d lookups, want 3", lookups)
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

func TestZone(t *testing.T) {
	chicago, _ := time.LoadLocation("America/Chicago")
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	noon := time.Date(2026, 1, 15, 18, 0, 0, 0, time.UTC)
	shown := func(rt *Router, filters Pipeline) string {
		t.Helper()
		rt.Scope("/", filters, func(s *Scope) {
			s.Handle("GET /when", func(w http.ResponseWriter, r *http.Request) error {
				_, err := io.WriteString(w, Zone(r).String()+" "+Local(r, noon).Format("15:04"))
				return err
			})
		})
		w := httptest.NewRecorder()
		rt.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/when", nil))
		return w.Body.String()
	}
	if got := shown(NewRouter(nil, nil), nil); got != "UTC 18:00" {
		t.Errorf("no zone set: %q", got)
	}
	rt := NewRouter(nil, nil)
	rt.TimeZone = chicago
	if got := shown(rt, nil); got != "America/Chicago 12:00" {
		t.Errorf("the app's zone: %q", got)
	}
	rt = NewRouter(nil, nil)
	rt.TimeZone = chicago
	user := func(w http.ResponseWriter, r *http.Request) error { SetZone(r, tokyo); return nil }
	if got := shown(rt, Pipeline{user}); got != "Asia/Tokyo 03:00" {
		t.Errorf("the user's zone: %q", got)
	}
	// Outside a router: UTC.
	if z := Zone(httptest.NewRequest("GET", "/", nil)); z != time.UTC {
		t.Errorf("outside a router: %v", z)
	}
}
