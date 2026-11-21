// Package oidcauth signs people in with an OpenID Connect provider (ADR
// 0034): Google, Microsoft or any other. It sends the browser to the
// provider, exchanges the code and checks the ID token; who the person is in
// Privo is decided by the caller, by issuer and subject alone.
//
// The ID token's signature, issuer and audience are checked by go-oidc; the
// checks it leaves to its caller (azp, iat, nonce, at_hash, which algorithms,
// who may sign in) are made here.
package oidcauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"privo.club/privo/internal/config"
)

// Errors. Each says only what is safe to show; the provider's own words are
// never repeated (ADR 0034 point 5).
var (
	// ErrUnavailable is a provider Privo could not reach or read.
	ErrUnavailable = errors.New("oidcauth: the provider could not be reached")
	// ErrRejected is a code or token the provider refused, or an ID token
	// that failed a check.
	ErrRejected = errors.New("oidcauth: the sign-in was not accepted")
	// ErrNotAllowed is a person the domain limit refuses (point 6).
	ErrNotAllowed = errors.New("oidcauth: this account may not sign in here")
)

const (
	// Leeway on exp and iat, for clocks that differ (point 4).
	leeway = 2 * time.Minute
	// maxAge is how old an ID token's iat may be: a sign-in in flight lives
	// ten minutes.
	maxAge = 10 * time.Minute
	// retryAfter is how long a failed discovery is not tried again.
	retryAfter = 30 * time.Second
	// maxBody bounds every answer from the provider.
	maxBody = 1 << 20
	// timeout bounds every request to the provider.
	timeout = 10 * time.Second
)

// algorithms are the only signatures accepted: never none, never HMAC.
var algorithms = []string{oidc.RS256, oidc.PS256, oidc.ES256}

// Client is one configured provider. Discovery runs at the first use and is
// kept; a failure is retried after retryAfter.
type Client struct {
	Issuer         string // exactly as configured; identities are keyed by it
	ClientID       string
	ClientSecret   string
	Kind           string // config.OIDCGoogle, OIDCMicrosoft or OIDCGeneric
	AllowedDomains []string
	// HTTP is the egress client (ADR 0008). Redirects are not followed, a
	// request takes at most ten seconds, and an answer at most 1 MiB.
	HTTP *http.Client
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	// mu guards the fields below and is never held during network I/O:
	// every page asks AuthOrigin (security review 2026-11-22, L-1).
	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	failedAt time.Time
	inflight chan struct{} // closed when the discovery under way ends
	origin   atomic.Pointer[string]
}

// Identity is who the provider says signed in. Subject is the key; the rest
// is a display snapshot and is never used to find or link anyone.
type Identity struct {
	Subject  string
	Email    string
	Name     string
	Username string // preferred_username
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// client is the HTTP client for the provider: the egress client, no
// redirects, bounded in time and size.
func (c *Client) client() *http.Client {
	hc := *c.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	hc.Timeout = timeout
	hc.Transport = limited{next: c.HTTP.Transport}
	return &hc
}

// discover returns the provider's metadata and verifier, fetching them once.
// One discovery runs at a time, in the background with its own time limit, so
// a visitor who gives up cannot fail it for everyone, and callers wait without
// holding a lock.
func (c *Client) discover(ctx context.Context) (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	c.mu.Lock()
	if c.provider != nil {
		p, v := c.provider, c.verifier
		c.mu.Unlock()
		return p, v, nil
	}
	if !c.failedAt.IsZero() && c.now().Sub(c.failedAt) < retryAfter {
		c.mu.Unlock()
		return nil, nil, ErrUnavailable
	}
	ch := c.inflight
	if ch == nil {
		ch = make(chan struct{})
		c.inflight = ch
		go c.run(ch) //nolint:gosec,contextcheck // detached on purpose: a visitor who gives up must not fail discovery for everyone (review L-1)
	}
	c.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
		return nil, nil, ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider == nil {
		return nil, nil, ErrUnavailable
	}
	return c.provider, c.verifier, nil
}

// run performs one discovery and records what it found.
func (c *Client) run(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	p, v, origin, err := c.fetch(ctx)
	c.mu.Lock()
	if err != nil {
		c.failedAt = c.now()
	} else {
		c.provider, c.verifier = p, v
		c.origin.Store(&origin)
	}
	c.inflight = nil
	c.mu.Unlock()
	close(done)
}

func (c *Client) fetch(ctx context.Context) (*oidc.Provider, *oidc.IDTokenVerifier, string, error) {
	// go-oidc keeps the client from this context for the key set too; the
	// key set itself runs with a background context.
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, c.client()), c.Issuer)
	if err != nil {
		return nil, nil, "", ErrUnavailable
	}
	var meta struct {
		AuthURL  string `json:"authorization_endpoint"`
		TokenURL string `json:"token_endpoint"`
		KeysURL  string `json:"jwks_uri"`
	}
	if err := p.Claims(&meta); err != nil || !usable(meta.AuthURL) || !usable(meta.TokenURL) || !usable(meta.KeysURL) {
		return nil, nil, "", fmt.Errorf("%w: the discovery document has no usable endpoints", ErrUnavailable)
	}
	u, _ := url.Parse(meta.AuthURL)
	v := p.Verifier(&oidc.Config{
		ClientID:             c.ClientID,
		SupportedSigningAlgs: algorithms,
		// exp gets the leeway; iat is checked below.
		Now: func() time.Time { return c.now().Add(-leeway) },
	})
	return p, v, u.Scheme + "://" + u.Host, nil
}

// usable is an endpoint Privo will send a browser or a secret to: https, or
// http on loopback for development, as the issuer.
func usable(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

func (c *Client) oauth(p *oidc.Provider, redirect string) *oauth2.Config {
	return &oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, Endpoint: p.Endpoint(), RedirectURL: redirect,
		Scopes: []string{oidc.ScopeOpenID, "email", "profile"}}
}

// Ready runs discovery if it has not run, so AuthOrigin is known.
func (c *Client) Ready(ctx context.Context) error {
	_, _, err := c.discover(ctx)
	return err
}

// AuthorizeURL is where the browser is sent: the code flow with PKCE (S256),
// state and nonce (point 3).
func (c *Client) AuthorizeURL(ctx context.Context, redirect, state, nonce, verifier string) (string, error) {
	p, _, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	return c.oauth(p, redirect).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

// AuthOrigin is the origin of the provider's authorization endpoint, once
// discovery has run ("" before): the sign-in form's CSP must allow it. It
// takes no lock: every page asks.
func (c *Client) AuthOrigin() string {
	if o := c.origin.Load(); o != nil {
		return *o
	}
	return ""
}

// claims are the ID token's claims Privo reads.
type claims struct {
	Subject       string `json:"sub"`
	AuthorizedBy  string `json:"azp"`
	Email         string `json:"email"`
	EmailVerified any    `json:"email_verified"` // a boolean, or "true" from some providers
	Name          string `json:"name"`
	Username      string `json:"preferred_username"`
	HostedDomain  string `json:"hd"` // Google Workspace
}

// Exchange trades the code for tokens and checks the ID token (point 4). The
// nonce is the one sealed when the sign-in started.
func (c *Client) Exchange(ctx context.Context, code, redirect, verifier, nonce string) (Identity, error) {
	p, v, err := c.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	tok, err := c.oauth(p, redirect).Exchange(context.WithValue(ctx, oauth2.HTTPClient, c.client()), code, oauth2.VerifierOption(verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			return Identity{}, ErrRejected
		}
		return Identity{}, ErrUnavailable
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Identity{}, fmt.Errorf("%w: no ID token", ErrRejected)
	}
	id, err := v.Verify(ctx, raw)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: the ID token failed a check", ErrRejected)
	}
	var cl claims
	if err := id.Claims(&cl); err != nil {
		return Identity{}, fmt.Errorf("%w: unreadable claims", ErrRejected)
	}
	if err := c.check(id, cl, nonce, tok.AccessToken); err != nil {
		return Identity{}, err
	}
	if err := c.allowed(cl); err != nil {
		// The subject only, so the refusal can be counted per account; a
		// caller never signs anyone in with an error.
		return Identity{Subject: id.Subject}, err
	}
	return Identity{Subject: id.Subject, Email: cl.Email, Name: cl.Name, Username: cl.Username}, nil
}

// check makes the checks go-oidc leaves to its caller.
func (c *Client) check(id *oidc.IDToken, cl claims, nonce, accessToken string) error {
	switch {
	case id.Subject == "" || len(id.Subject) > 255:
		return fmt.Errorf("%w: no usable subject", ErrRejected)
	case nonce == "" || subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(nonce)) != 1:
		return fmt.Errorf("%w: the nonce does not match", ErrRejected)
	case len(id.Audience) > 1 && cl.AuthorizedBy != c.ClientID:
		return fmt.Errorf("%w: several audiences and another authorized party", ErrRejected)
	case cl.AuthorizedBy != "" && cl.AuthorizedBy != c.ClientID:
		return fmt.Errorf("%w: issued to another party", ErrRejected)
	case id.IssuedAt.IsZero() || id.IssuedAt.After(c.now().Add(leeway)) || c.now().Sub(id.IssuedAt) > maxAge+leeway:
		return fmt.Errorf("%w: issued at an unusable time", ErrRejected)
	}
	if id.AccessTokenHash != "" {
		if err := id.VerifyAccessToken(accessToken); err != nil {
			return fmt.Errorf("%w: at_hash does not match", ErrRejected)
		}
	}
	return nil
}

// allowed applies the domain limit (point 6).
func (c *Client) allowed(cl claims) error {
	if len(c.AllowedDomains) == 0 {
		return nil
	}
	switch c.Kind {
	case config.OIDCGoogle:
		// The Workspace domain; a personal account has none.
		if hd, ok := asciiLower(cl.HostedDomain); ok && slices.Contains(c.AllowedDomains, hd) {
			return nil
		}
	case config.OIDCMicrosoft:
		// Refused at startup: the tenant is the limit, the email is not
		// verified. Never reached; refuse rather than trust the email.
	default:
		verified := cl.EmailVerified == true || cl.EmailVerified == "true"
		_, raw, ok := strings.Cut(cl.Email, "@")
		domain, ascii := asciiLower(raw)
		if verified && ok && ascii && !strings.Contains(domain, "@") && slices.Contains(c.AllowedDomains, domain) {
			return nil
		}
	}
	return ErrNotAllowed
}

// asciiLower lower-cases s when it is ASCII only. Go's Unicode lowering maps
// some letters onto ASCII ones ("İnc.com" becomes "inc.com"), while the
// name the provider verified is another, registrable domain (security review
// 2026-11-22, M-1): a domain with anything but ASCII never matches.
func asciiLower(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return "", false
		}
	}
	return strings.ToLower(s), true
}

// limited bounds every answer's body and fails a larger one.
type limited struct{ next http.RoundTripper }

func (l limited) RoundTrip(r *http.Request) (*http.Response, error) {
	next := l.next
	if next == nil {
		next = http.DefaultTransport
	}
	resp, err := next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &capped{r: resp.Body, left: maxBody}
	return resp, nil
}

type capped struct {
	r    io.ReadCloser
	left int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errors.New("oidcauth: the answer is larger than 1 MiB")
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func (c *capped) Close() error { return c.r.Close() }
