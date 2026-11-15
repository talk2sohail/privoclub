// Package clientip finds the address a request came from when Privo runs
// behind reverse proxies, such as Cloudflare and Traefik on Coolify.
//
// Without trusted proxies the address is the TCP peer, as before. With them,
// X-Forwarded-For is read from the right: each proxy appends the address it
// received the request from, so the first address that is not a trusted proxy
// is the client. Anything to its left was written by the client and is never
// used.
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// From returns the address r came from, given the proxies Privo trusts.
func From(r *http.Request, trusted []netip.Prefix) netip.Addr {
	peer := parse(r.RemoteAddr)
	if !peer.IsValid() || !in(peer, trusted) {
		return peer
	}
	hops := r.Header.Values("X-Forwarded-For")
	var all []string
	for _, h := range hops {
		all = append(all, strings.Split(h, ",")...)
	}
	for i := len(all) - 1; i >= 0; i-- {
		a := parse(strings.TrimSpace(all[i]))
		if !a.IsValid() {
			return peer // a hop we cannot read: stop at the last proxy we trust
		}
		if !in(a, trusted) {
			return a
		}
	}
	return peer
}

// Key is the address to count a limit against: an IPv4 address, or the /64
// of an IPv6 address, since one subscriber is given at least a /64.
func Key(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	if a.Is4() {
		return a.String()
	}
	p, _ := a.Prefix(64)
	return p.String()
}

func parse(s string) netip.Addr {
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	a, err := netip.ParseAddr(strings.Trim(s, "[]"))
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap().WithZone("")
}

func in(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
