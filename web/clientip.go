package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP is the visitor's address, as Rails' remote_ip finds it: the
// rightmost X-Forwarded-For address that isn't a private or loopback one
// (the proxies in front of the app), else the connection's.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	chain := []string{}
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(h, ",") {
			if p := strings.TrimSpace(part); p != "" {
				chain = append(chain, p)
			}
		}
	}
	chain = append(chain, host)
	for i := len(chain) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(chain[i])
		if err != nil {
			continue
		}
		if !addr.IsPrivate() && !addr.IsLoopback() && !addr.IsLinkLocalUnicast() {
			return addr.String()
		}
	}
	return host
}
