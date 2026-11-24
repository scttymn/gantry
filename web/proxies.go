package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Proxies is which peers are proxies the app trusts, and which header one
// names the visitor's address in (Rails' trusted_proxies and RemoteIp). A
// proxy in front of the app (a load balancer, Cloudflare's tunnel,
// kamal-proxy) knows what the app can't see: the visitor's address, whether
// they used https, and the host they asked for; it says so in headers that
// anyone can also send. So they're believed only from a trusted peer, and
// from any other they're ignored and removed before the app sees them.
type Proxies struct {
	// Trusted are the proxies' addresses; nil (with no Names) is
	// PrivateNetworks, Rails' default, which trusts whatever reaches the app
	// from a private or loopback address. That's right when the only way in
	// is through the proxy; when anything else can reach the app from a
	// private address (another container on its network, a Docker gateway,
	// a host on the same LAN), it can name any visitor's address and slip
	// past per-address rate limits. List the proxies then (ParseProxies
	// reads them from the environment), or an empty, non-nil list when
	// there's no proxy at all.
	Trusted []netip.Prefix
	// Names are proxies by their name in DNS: a container's on a Docker
	// network ("cloudflared"), whose address changes as it restarts. Each is
	// looked up at most once a minute; while a lookup fails, it's trusted
	// nowhere. Only its addresses are trusted, not the rest of its network.
	Names []string
	// ClientIP is the header a proxy puts the visitor's address in:
	// X-Forwarded-For when "" (the nearest address that isn't a proxy's),
	// or a provider's own, one address: "CF-Connecting-IP", "Fly-Client-IP".
	ClientIP string
	// ForwardedHost believes a proxy's X-Forwarded-Host, for a proxy that
	// changes the Host header and says so there. Off, the header is removed:
	// a proxy that keeps Host (kamal-proxy, Cloudflare's tunnel) may pass on
	// a visitor's own, which would let them name any host.
	ForwardedHost bool
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

var forwarding = []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-Port",
	"X-Forwarded-Ssl", "Forwarded", "X-Real-Ip"}

type origin struct{ ip, scheme, host string }

var originKey = NewKey[origin]("origin")

func (p Proxies) trusts(a netip.Addr) bool {
	a = a.Unmap()
	for _, name := range p.Names {
		if slices.Contains(proxyAddrs(name), a) {
			return true
		}
	}
	nets := p.Trusted
	if nets == nil && len(p.Names) == 0 {
		nets = PrivateNetworks
	}
	for _, n := range nets {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

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
		chain := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(chain) - 1; i >= 0; i-- {
			if a, err := netip.ParseAddr(strings.TrimSpace(chain[i])); err == nil && !p.trusts(a) {
				o.ip = a.Unmap().String()
				break
			}
		}
	}
	if proto := strings.ToLower(lastValue(r.Header, "X-Forwarded-Proto")); proto == "https" || proto == "http" {
		o.scheme = proto
	}
	if host := lastValue(r.Header, "X-Forwarded-Host"); host != "" && p.ForwardedHost {
		o.host = cleanHost(host)
	}
	return o, true
}

func lastValue(h http.Header, name string) string {
	values := h.Values(name)
	if len(values) == 0 {
		return ""
	}
	s := values[len(values)-1]
	return strings.TrimSpace(s[strings.LastIndex(s, ",")+1:])
}

// ParseProxies is a list of proxies' addresses and CIDR ranges, separated
// by commas or spaces ("10.0.1.5, 172.18.0.0/16"), for Proxies.Trusted:
// nil when s is blank, so the default (PrivateNetworks) holds, and none at
// all for "none", for an app that no proxy is in front of.
func ParseProxies(s string) ([]netip.Prefix, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	if len(fields) == 0 {
		return nil, nil
	}
	if len(fields) == 1 && fields[0] == "none" {
		return []netip.Prefix{}, nil
	}
	prefixes := make([]netip.Prefix, 0, len(fields))
	for _, f := range fields {
		if p, err := netip.ParsePrefix(f); err == nil {
			prefixes = append(prefixes, p.Masked())
		} else if a, err := netip.ParseAddr(f); err == nil {
			prefixes = append(prefixes, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		} else {
			return nil, fmt.Errorf("web: trusted proxy %q is neither an address nor a CIDR range", f)
		}
	}
	return prefixes, nil
}

func (rt *Router) arrive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o, trusted := rt.Proxies.origin(r)
		if !trusted {
			r = withoutForwarding(r, forwarding, rt.Proxies.ClientIP)
		} else if !rt.Proxies.ForwardedHost {
			r = withoutForwarding(r, []string{"X-Forwarded-Host"})
		}
		Set(r, originKey, o)
		if rt.TimeZone != nil {
			Set(r, zoneKey, rt.TimeZone)
		}
		if len(rt.Hosts) > 0 && r.URL.Path != "/up" && !allowedHost(rt.Hosts, o.host) {
			rt.Error(w, r, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withoutForwarding(r *http.Request, names []string, clientIP ...string) *http.Request {
	names = append(names[:len(names):len(names)], clientIP...)
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

func allowedHost(hosts []string, host string) bool {
	for _, h := range hosts {
		h = cleanHost(h)
		if h == host || strings.HasPrefix(h, ".") && (host == h[1:] || strings.HasSuffix(host, h)) {
			return true
		}
	}
	return false
}

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

var (
	proxyNames  sync.Map
	proxyClock  = time.Now
	lookupProxy = func(name string) ([]netip.Addr, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return net.DefaultResolver.LookupNetIP(ctx, "ip", name)
	}
)

type proxyLookup struct {
	mu    sync.Mutex
	at    time.Time
	addrs []netip.Addr
}

func proxyAddrs(name string) []netip.Addr {
	v, _ := proxyNames.LoadOrStore(name, &proxyLookup{})
	l := v.(*proxyLookup)
	l.mu.Lock()
	defer l.mu.Unlock()
	if now := proxyClock(); l.at.IsZero() || now.Sub(l.at) > time.Minute {
		addrs, _ := lookupProxy(name)
		for i := range addrs {
			addrs[i] = addrs[i].Unmap()
		}
		l.at, l.addrs = now, addrs
	}
	return l.addrs
}
