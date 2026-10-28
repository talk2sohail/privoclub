// Package egress provides the one HTTP client Privo uses for outbound requests
// to addresses supplied by others (ADR 0008).
//
// The dialer checks the resolved IP at connect time, which also defeats DNS
// rebinding. Non-public ranges are refused unless an operator allows a CIDR
// explicitly (self-hosted installs with in-house servers).
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrBlocked is returned when a destination is not allowed.
var ErrBlocked = errors.New("egress: destination not allowed")

// BlockedError says which address was refused and why.
type BlockedError struct {
	Addr   string
	Reason string
}

func (e *BlockedError) Error() string { return fmt.Sprintf("egress: %s refused: %s", e.Addr, e.Reason) }
func (e *BlockedError) Unwrap() error { return ErrBlocked }

// Policy decides which destinations are allowed.
type Policy struct {
	// Allow lists non-public prefixes an operator explicitly permits.
	Allow []netip.Prefix
}

// ParseAllowList parses a comma-separated list of CIDRs (PRIVO_EGRESS_ALLOW_CIDRS).
func ParseAllowList(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		p, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("egress: allow list entry %q: %w", part, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// Check reports whether ip may be dialled.
func (p Policy) Check(ip netip.Addr) error {
	ip = ip.Unmap()
	for _, pre := range p.Allow {
		if pre.Contains(ip) {
			return nil
		}
	}
	if reason := nonPublic(ip); reason != "" {
		return &BlockedError{Addr: ip.String(), Reason: reason}
	}
	return nil
}

var blockedPrefixes = []struct {
	prefix netip.Prefix
	reason string
}{
	{netip.MustParsePrefix("0.0.0.0/8"), "this network"},
	{netip.MustParsePrefix("100.64.0.0/10"), "carrier-grade NAT"},
	{netip.MustParsePrefix("192.0.0.0/24"), "IETF protocol assignments"},
	{netip.MustParsePrefix("192.0.2.0/24"), "documentation"},
	{netip.MustParsePrefix("198.18.0.0/15"), "benchmarking"},
	{netip.MustParsePrefix("198.51.100.0/24"), "documentation"},
	{netip.MustParsePrefix("203.0.113.0/24"), "documentation"},
	{netip.MustParsePrefix("240.0.0.0/4"), "reserved"},
	{netip.MustParsePrefix("64:ff9b::/96"), "NAT64"},
	{netip.MustParsePrefix("64:ff9b:1::/48"), "local NAT64"},
	{netip.MustParsePrefix("2002::/16"), "6to4 (can embed private IPv4)"},
	{netip.MustParsePrefix("2001::/32"), "Teredo (can embed private IPv4)"},
	{netip.MustParsePrefix("100::/64"), "discard"},
	{netip.MustParsePrefix("2001:db8::/32"), "documentation"},
}

func nonPublic(ip netip.Addr) string {
	switch {
	case !ip.IsValid():
		return "invalid address"
	case ip.IsLoopback():
		return "loopback"
	case ip.IsPrivate():
		return "private network"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "link-local (includes cloud metadata)"
	case ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return "multicast"
	case ip.IsUnspecified():
		return "unspecified"
	}
	for _, b := range blockedPrefixes {
		if b.prefix.Contains(ip) {
			return b.reason
		}
	}
	return ""
}

// Options configure a client.
type Options struct {
	Policy         Policy
	Timeout        time.Duration // whole request; 0 means none (streams)
	DialTimeout    time.Duration
	MaxRedirects   int  // default 3
	SameHostOnly   bool // refuse redirects to another host (metadata fetches)
	MaxIdlePerHost int
}

// NewClient returns an HTTP client that enforces the policy on every dial
// and redirect. It never uses environment proxies: a proxy would hide the
// real destination from the check.
func NewClient(o Options) *http.Client {
	if o.DialTimeout == 0 {
		o.DialTimeout = 10 * time.Second
	}
	if o.MaxRedirects == 0 {
		o.MaxRedirects = 3
	}
	if o.MaxIdlePerHost == 0 {
		o.MaxIdlePerHost = 16
	}
	dialer := &net.Dialer{
		Timeout: o.DialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return &BlockedError{Addr: address, Reason: "unparseable address"}
			}
			return o.Policy.Check(ap.Addr())
		},
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   o.MaxIdlePerHost,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   o.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= o.MaxRedirects {
				return fmt.Errorf("egress: more than %d redirects", o.MaxRedirects)
			}
			if o.SameHostOnly && req.URL.Host != via[0].URL.Host {
				return &BlockedError{Addr: req.URL.Host, Reason: "cross-host redirect"}
			}
			if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
				return &BlockedError{Addr: req.URL.Host, Reason: "unsupported scheme"}
			}
			if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return &BlockedError{Addr: req.URL.Host, Reason: "redirect downgrades https to http"}
			}
			return nil
		},
	}
}

// CheckURL validates a URL before it is stored (for example when an admin
// registers a remote server): scheme, no credentials, and every address the
// host resolves to must pass the policy. Dial-time checks still apply later.
func (p Policy) CheckURL(ctx context.Context, raw string, resolver *net.Resolver) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("egress: invalid URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, &BlockedError{Addr: raw, Reason: "scheme must be http or https"}
	}
	if u.User != nil {
		return nil, &BlockedError{Addr: u.Host, Reason: "credentials in URL"}
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("egress: URL has no host")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return u, p.Check(ip)
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("egress: resolving %s: %w", host, err)
	}
	for _, a := range addrs {
		if err := p.Check(a); err != nil {
			return nil, err
		}
	}
	return u, nil
}
