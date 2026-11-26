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
	// OnBlocked, when set, is called on every refusal with a strictly redacted
	// target (scheme://host or IP only) and the fixed refusal reason.
	OnBlocked func(ctx context.Context, target, reason string)
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
	return p.checkIP(context.Background(), "", ip)
}

func (p Policy) checkIP(ctx context.Context, target string, ip netip.Addr) error {
	ip = ip.Unmap()
	for _, pre := range p.Allow {
		if pre.Contains(ip) {
			return nil
		}
	}
	if reason := nonPublic(ip); reason != "" {
		if target == "" {
			target = ip.String()
		}
		p.notify(ctx, target, reason)
		return &BlockedError{Addr: ip.String(), Reason: reason}
	}
	return nil
}

func (p Policy) notify(ctx context.Context, target, reason string) {
	if p.OnBlocked == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background() //nolint:contextcheck // fallback when caller passes nil ctx
	}
	p.OnBlocked(ctx, target, reason)
}

// RedactURL returns only scheme://host for raw, stripping userinfo, path,
// query parameters, and fragments. Unparseable inputs yield "(invalid)".
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid)"
	}
	return redactTarget(u)
}

// redactTarget returns only scheme://host (never userinfo, path, query, or fragment).
func redactTarget(u *url.URL) string {
	if u == nil {
		return "(invalid)"
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme != "" && u.Host != "":
		return scheme + "://" + u.Host
	case scheme != "":
		return scheme + "://"
	case u.Host != "":
		return u.Host
	default:
		return "(invalid)"
	}
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
	OnBlocked      func(ctx context.Context, target, reason string)
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
	if o.OnBlocked != nil {
		o.Policy.OnBlocked = o.OnBlocked
	}
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
		ControlContext: func(ctx context.Context, _, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				const reason = "unparseable address"
				o.Policy.notify(ctx, "(unparseable)", reason)
				return &BlockedError{Addr: address, Reason: reason}
			}
			return o.Policy.checkIP(ctx, "", ap.Addr())
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
				const reason = "cross-host redirect"
				o.Policy.notify(req.Context(), redactTarget(req.URL), reason)
				return &BlockedError{Addr: req.URL.Host, Reason: reason}
			}
			if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
				const reason = "unsupported scheme"
				o.Policy.notify(req.Context(), redactTarget(req.URL), reason)
				return &BlockedError{Addr: req.URL.Host, Reason: reason}
			}
			if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
				const reason = "redirect downgrades https to http"
				o.Policy.notify(req.Context(), redactTarget(req.URL), reason)
				return &BlockedError{Addr: req.URL.Host, Reason: reason}
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
		if inner := errors.Unwrap(err); inner != nil {
			return nil, fmt.Errorf("egress: invalid URL: %w", inner)
		}
		return nil, fmt.Errorf("egress: invalid URL")
	}
	target := redactTarget(u)
	if u.Scheme != "https" && u.Scheme != "http" {
		const reason = "scheme must be http or https"
		p.notify(ctx, target, reason)
		return nil, &BlockedError{Addr: target, Reason: reason}
	}
	if u.User != nil {
		const reason = "credentials in URL"
		p.notify(ctx, target, reason)
		return nil, &BlockedError{Addr: u.Host, Reason: reason}
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("egress: URL has no host")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return u, p.checkIP(ctx, target, ip)
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("egress: resolving %s: %w", host, err)
	}
	for _, a := range addrs {
		if err := p.checkIP(ctx, target, a); err != nil {
			return nil, err
		}
	}
	return u, nil
}
