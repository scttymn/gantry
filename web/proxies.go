package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Proxies is which peers are proxies the app trusts, and which header one
// names the visitor's address in (Rails' trusted_proxies and RemoteIp). A
// proxy in front of the app (a load balancer, Cloudflare's tunnel,
// kamal-proxy) knows what the app can't see: the visitor's address, whether
// they used https, and the host they asked for; it says so in headers that
// anyone can also send. So they're believed only from a trusted peer, and
// from any other they're ignored and removed before the app sees them.
type Proxies struct {
	// Trusted are the proxies' addresses; nil is PrivateNetworks, Rails'
	// default, which trusts whatever reaches the app from a private or
	// loopback address.
	Trusted []netip.Prefix
	// ClientIP is the header a proxy puts the visitor's address in:
	// X-Forwarded-For when "" (the nearest address that isn't a proxy's),
	// or a provider's own, one address: "CF-Connecting-IP", "Fly-Client-IP".
	ClientIP string
}

// PrivateNetworks are the loopback, private and link-local ranges.
var PrivateNetworks = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
}

// forwarding are the headers a proxy speaks in, removed when the peer isn't
// one.
var forwarding = []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-Port",
	"X-Forwarded-Ssl", "Forwarded", "X-Real-Ip"}

// origin is where a request comes from, worked out once as it arrives.
type origin struct{ ip, scheme, host string }

var originKey = NewKey[origin]("origin")

func (p Proxies) trusts(a netip.Addr) bool {
	a = a.Unmap()
	nets := p.Trusted
	if nets == nil {
		nets = PrivateNetworks
	}
	for _, n := range nets {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// origin is where r comes from, and whether its peer is a trusted proxy.
func (p Proxies) origin(r *http.Request) (origin, bool) {
	peer := r.RemoteAddr
	if h, _, err := net.SplitHostPort(peer); err == nil {
		peer = h
	}
	o := origin{ip: peer, scheme: "http", host: cleanHost(r.Host)}
	if r.TLS != nil {
		o.scheme = "https"
	}
	addr, err := netip.ParseAddr(peer)
	if err != nil || !p.trusts(addr) {
		return o, false
	}
	if p.ClientIP != "" {
		if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get(p.ClientIP))); err == nil {
			o.ip = a.Unmap().String()
		}
	} else {
		// The nearest address that isn't a proxy's, from the right: each
		// proxy appends the address it was reached from.
		chain := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(chain) - 1; i >= 0; i-- {
			if a, err := netip.ParseAddr(strings.TrimSpace(chain[i])); err == nil && !p.trusts(a) {
				o.ip = a.Unmap().String()
				break
			}
		}
	}
	if proto := strings.ToLower(strings.TrimSpace(firstValue(r.Header.Get("X-Forwarded-Proto")))); proto == "https" || proto == "http" {
		o.scheme = proto
	}
	if host := strings.TrimSpace(lastValue(r.Header.Get("X-Forwarded-Host"))); host != "" {
		o.host = cleanHost(host)
	}
	return o, true
}

func firstValue(s string) string { v, _, _ := strings.Cut(s, ","); return v }

func lastValue(s string) string { return s[strings.LastIndex(s, ",")+1:] }

// arrive works out where each request comes from, keeps it in Current, and
// refuses a host the app doesn't answer to.
func (rt *Router) arrive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o, trusted := rt.Proxies.origin(r)
		if !trusted {
			r = withoutForwarding(r, rt.Proxies.ClientIP)
		}
		Set(r, originKey, o)
		if len(rt.Hosts) > 0 && r.URL.Path != "/up" && !allowedHost(rt.Hosts, o.host) {
			rt.Error(w, r, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withoutForwarding is r without the headers a proxy speaks in, on a copy,
// so nothing later reads a forged one. r itself when it has none.
func withoutForwarding(r *http.Request, clientIP string) *http.Request {
	names := forwarding
	if clientIP != "" {
		names = append(names[:len(names):len(names)], clientIP)
	}
	var copied *http.Request
	for _, name := range names {
		if _, ok := r.Header[http.CanonicalHeaderKey(name)]; !ok {
			continue
		}
		if copied == nil {
			copied = r.WithContext(r.Context())
			copied.Header = r.Header.Clone()
		}
		copied.Header.Del(name)
	}
	if copied == nil {
		return r
	}
	return copied
}

// allowedHost reports whether host is one of hosts (Rails' config.hosts):
// "example.com" is that host, ".example.com" it and its subdomains.
func allowedHost(hosts []string, host string) bool {
	for _, h := range hosts {
		h = cleanHost(h)
		if h == host || strings.HasPrefix(h, ".") && (host == h[1:] || strings.HasSuffix(host, h)) {
			return true
		}
	}
	return false
}

// cleanHost is a host for comparing: without its port, lowercase, and
// without a trailing dot.
func cleanHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// ClientIP is the visitor's address: a trusted proxy's word for it, else
// the peer's (Rails' remote_ip). Outside a router, it's worked out with the
// default rule.
func ClientIP(r *http.Request) string {
	if o, ok := Get(r, originKey); ok {
		return o.ip
	}
	o, _ := Proxies{}.origin(r)
	return o.ip
}

// Scheme is "https" or "http", as the visitor used it: a trusted proxy's
// X-Forwarded-Proto, else whether the connection was TLS.
func Scheme(r *http.Request) string {
	if o, ok := Get(r, originKey); ok {
		return o.scheme
	}
	o, _ := Proxies{}.origin(r)
	return o.scheme
}

// RequestHost is the host a request is for, cleaned for comparing (no
// port, lowercase, no trailing dot): a trusted proxy's X-Forwarded-Host,
// else the request's own.
func RequestHost(r *http.Request) string {
	if o, ok := Get(r, originKey); ok {
		return o.host
	}
	return cleanHost(r.Host)
}
