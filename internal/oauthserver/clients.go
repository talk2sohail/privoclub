// Package oauthserver is Privo's own OAuth 2.1 authorization server, with
// which AI clients sign in to the gateway (F06, ADR 0022). It is built on
// authelia.com/provider/oauth2 (ADR 0022 point 16). Privo supplies the
// storage, decides which clients are accepted, and makes two checks in front
// of the library that the library does not make: the redirect address rule
// and "the resource parameter must be present".
package oauthserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"privo.club/privo/internal/store"
)

// Client is an AI client that may ask a member for access.
type Client struct {
	ID           string // a metadata document URL, or an id Privo made at registration
	Name         string
	Kind         string // store.ClientKnown, ClientApproved, ClientDocument or ClientRegistered
	RedirectURIs []string
}

// Host is the host of a metadata document client, "" for a registered one.
func (c Client) Host() string {
	if u, err := url.Parse(c.ID); err == nil && u.Scheme == "https" {
		return u.Host
	}
	return ""
}

// LoopbackOnly reports whether every redirect address of the client is on the
// person's own computer. Such a client cannot be told from another program
// on that computer, and the consent page says so (ADR 0022 point 7).
func (c Client) LoopbackOnly() bool {
	for _, raw := range c.RedirectURIs {
		u, err := url.Parse(raw)
		if err != nil || !loopback(u) {
			return false
		}
	}
	return len(c.RedirectURIs) > 0
}

// KnownClient is a client metadata document Privo accepts without setup.
type KnownClient struct {
	URL  string
	Name string
}

// Known is the built-in list (ADR 0022 point 12, agreed by the owner on
// 2026-11-12). The documents were fetched on 2026-11-14 and each named
// itself by this URL, except the two marked: their sites refuse automated
// fetches, so those URLs are from the adoption survey cited in the ADR. A
// wrong URL here cannot accept the wrong client: the document is fetched when
// a client uses it and must name itself by the same URL.
var Known = []KnownClient{
	{URL: "https://claude.ai/oauth/claude-code-client-metadata", Name: "Claude Code"},
	{URL: "https://claude.ai/oauth/mcp-oauth-client-metadata", Name: "Claude"},
	{URL: "https://vscode.dev/oauth/client-metadata.json", Name: "Visual Studio Code"},
	{URL: "https://chatgpt.com/oauth/client.json", Name: "ChatGPT"}, // not fetched
	{URL: "https://chatgpt.com/oauth/codex/client.json", Name: "Codex"},
	{URL: "https://github.com/copilot/cli/client-metadata.json", Name: "GitHub Copilot CLI"}, // not fetched
	{URL: "https://zed.dev/oauth/client-metadata.json", Name: "Zed"},
	{URL: "https://antigravity.google/oauth/client-metadata.json", Name: "Google Antigravity"},
}

// Trust policies (config.OAuthClients*).
const (
	PolicyKnown    = "known"
	PolicyApproved = "approved"
	PolicyAny      = "any"
)

// Limits on a client metadata document (ADR 0022 point 11).
const (
	docTimeout   = 5 * time.Second
	docMaxBytes  = 64 << 10
	docMaxAge    = 24 * time.Hour
	docMinAge    = 5 * time.Minute
	docDefault   = time.Hour
	docFailedFor = time.Minute // a failed fetch is not repeated for this long
	docCacheSize = 512
	maxRedirects = 20  // redirect addresses in one document or registration
	maxNameLen   = 100 // characters of a client's name that are kept
	maxURLLen    = 2048
)

// NotAcceptedError says why a client may not ask. Its text is for a page a
// person reads; it never goes into a redirect.
type NotAcceptedError struct {
	ClientID string
	Host     string // the document's host, when the client is a document
	Reason   string
	// Approvable: an administrator can accept this client on the Clients page.
	Approvable bool
}

func (e *NotAcceptedError) Error() string { return "oauthserver: client not accepted: " + e.Reason }

// ClientStore is what Clients needs of the store.
type ClientStore interface {
	OAuthClient(ctx context.Context, org store.OrgID, clientID string) (store.OAuthClient, error)
}

// Clients decides which clients are accepted and reads their metadata.
type Clients struct {
	Org    store.OrgID
	Store  ClientStore
	Policy string // PolicyKnown, PolicyApproved or PolicyAny
	// HTTP fetches metadata documents. It must refuse non-public addresses
	// (the egress client) and follow no redirect; New sets that up.
	HTTP *http.Client
	Now  func() time.Time
	// Known replaces the built-in list (tests).
	Known []KnownClient
	// AllowHTTP accepts http metadata documents (tests with a local server).
	AllowHTTP bool

	mu    sync.Mutex
	cache map[string]cachedDoc
}

type cachedDoc struct {
	c     Client
	err   error
	until time.Time
}

func (c *Clients) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Clients) known(id string) (KnownClient, bool) {
	list := c.Known
	if list == nil {
		list = Known
	}
	for _, k := range list {
		if k.URL == id {
			return k, true
		}
	}
	return KnownClient{}, false
}

// documentURL checks that a client id is a URL a metadata document can live
// at: https, a host, a path, and nothing that makes two spellings of one
// address (credentials, a fragment, dot segments).
func (c *Clients) documentURL(id string) (*url.URL, error) {
	if len(id) > maxURLLen {
		return nil, errors.New("the client id is too long")
	}
	u, err := url.Parse(id)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return nil, errors.New("the client id is not a URL")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !c.AllowHTTP) {
		return nil, errors.New("a client metadata document must be at an https address")
	}
	if u.User != nil || u.Fragment != "" || u.RawFragment != "" || strings.Contains(id, "#") {
		return nil, errors.New("the client id must not hold credentials or a fragment")
	}
	if u.Path == "" || u.Path == "/" {
		return nil, errors.New("the client id must have a path")
	}
	for _, seg := range strings.Split(u.EscapedPath(), "/") {
		if seg == "." || seg == ".." {
			return nil, errors.New("the client id must not hold dot segments")
		}
	}
	return u, nil
}

// kindOf applies the trust policy to a client id without fetching anything.
// It returns the kind the client is accepted as, or why it is not.
func (c *Clients) kindOf(ctx context.Context, id string) (kind string, stored store.OAuthClient, err error) {
	if id == "" {
		return "", stored, &NotAcceptedError{Reason: "The request names no client."}
	}
	row, rowErr := c.Store.OAuthClient(ctx, c.Org, id)
	if rowErr != nil && !errors.Is(rowErr, store.ErrNotFound) {
		return "", stored, rowErr
	}
	if rowErr == nil {
		return row.Kind, row, nil // approved or registered: accepted under every policy
	}
	u, urlErr := c.documentURL(id)
	if urlErr != nil {
		// Not a document and not registered here.
		return "", stored, &NotAcceptedError{ClientID: id, Reason: "This client is not registered with this Privo."}
	}
	if _, ok := c.known(id); ok && c.Policy != PolicyApproved {
		return store.ClientKnown, stored, nil
	}
	if c.Policy == PolicyAny {
		return store.ClientDocument, stored, nil
	}
	return "", stored, &NotAcceptedError{ClientID: id, Host: u.Host, Approvable: true,
		Reason: "This Privo does not know this client. An administrator can approve it on the Clients page."}
}

// Accepted reports whether a client may be dealt with at all, without
// fetching its document. The token and revocation endpoints use it: a client
// that held a grant keeps working while its vendor's site is down, and stops
// when an administrator removes it or the policy no longer covers it.
func (c *Clients) Accepted(ctx context.Context, id string) (Client, error) {
	kind, row, err := c.kindOf(ctx, id)
	if err != nil {
		return Client{}, err
	}
	out := Client{ID: id, Kind: kind, Name: row.Name, RedirectURIs: row.RedirectURIs}
	if k, ok := c.known(id); ok && out.Name == "" {
		out.Name = k.Name
	}
	return out, nil
}

// Lookup returns a client with its redirect addresses, for an authorization
// request: the policy first, then the document (fetched or from the cache)
// for a client that has one.
func (c *Clients) Lookup(ctx context.Context, id string) (Client, error) {
	kind, row, err := c.kindOf(ctx, id)
	if err != nil {
		return Client{}, err
	}
	if kind == store.ClientRegistered {
		return Client{ID: id, Kind: kind, Name: row.Name, RedirectURIs: row.RedirectURIs}, nil
	}
	doc, err := c.Document(ctx, id)
	if err != nil {
		return Client{}, err
	}
	doc.Kind = kind
	if k, ok := c.known(id); ok && kind == store.ClientKnown {
		doc.Name = k.Name // Privo's own name for a client it vouches for
	}
	return doc, nil
}

// Document fetches and checks a client metadata document, or returns the
// cached one. A failure is cached for a minute, so requests nobody is signed
// in for cannot make Privo fetch one address over and over.
func (c *Clients) Document(ctx context.Context, id string) (Client, error) {
	if _, err := c.documentURL(id); err != nil {
		return Client{}, &NotAcceptedError{ClientID: id, Reason: "This client's address is not one a client can use: " + err.Error() + "."}
	}
	now := c.now()
	c.mu.Lock()
	if d, ok := c.cache[id]; ok && now.Before(d.until) {
		c.mu.Unlock()
		return d.c, d.err
	}
	c.mu.Unlock()

	doc, age, err := c.fetch(ctx, id)
	if err != nil {
		u, _ := url.Parse(id)
		err = &NotAcceptedError{ClientID: id, Host: u.Host, Reason: "The client's description could not be used: " + err.Error() + "."}
		age = docFailedFor
	}
	c.mu.Lock()
	if c.cache == nil || len(c.cache) >= docCacheSize {
		c.cache = map[string]cachedDoc{}
	}
	c.cache[id] = cachedDoc{c: doc, err: err, until: now.Add(age)}
	c.mu.Unlock()
	return doc, err
}

// document is the part of a client metadata document Privo reads.
type document struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	ClientSecret            string   `json:"client_secret"`
}

func (c *Clients) fetch(ctx context.Context, id string) (Client, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, docTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, id, nil)
	if err != nil {
		return Client{}, 0, errors.New("its address cannot be requested")
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		// The cause can name internal addresses; the person gets the class.
		return Client{}, 0, errors.New("it could not be fetched")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return Client{}, 0, fmt.Errorf("its address answered %d", res.StatusCode)
	}
	if mt, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type")); mt != "application/json" && !strings.HasSuffix(mt, "+json") {
		return Client{}, 0, errors.New("it is not JSON")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, docMaxBytes+1))
	if err != nil {
		return Client{}, 0, errors.New("it could not be read")
	}
	if len(body) > docMaxBytes {
		return Client{}, 0, errors.New("it is larger than 64 KiB")
	}
	var d document
	if err := json.Unmarshal(body, &d); err != nil {
		return Client{}, 0, errors.New("it is not valid JSON")
	}
	if d.ClientID != id {
		return Client{}, 0, errors.New("it names another client id than its own address")
	}
	if d.ClientSecret != "" || (d.TokenEndpointAuthMethod != "" && d.TokenEndpointAuthMethod != "none") {
		return Client{}, 0, errors.New("it asks for a client secret, and Privo accepts public clients only")
	}
	if len(d.GrantTypes) > 0 && !contains(d.GrantTypes, "authorization_code") {
		return Client{}, 0, errors.New("it does not use the authorization code grant")
	}
	if len(d.ResponseTypes) > 0 && !contains(d.ResponseTypes, "code") {
		return Client{}, 0, errors.New("it does not use the code response type")
	}
	if err := checkRedirects(d.RedirectURIs); err != nil {
		return Client{}, 0, err
	}
	return Client{ID: id, Name: cleanName(d.ClientName), RedirectURIs: d.RedirectURIs}, cacheAge(res.Header.Get("Cache-Control")), nil
}

// cacheAge is how long a document is kept: what its Cache-Control says,
// between five minutes and 24 hours; an hour when it says nothing.
func cacheAge(cc string) time.Duration {
	age := docDefault
	for _, part := range strings.Split(cc, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "no-store" || part == "no-cache" {
			age = 0
		}
		if v, ok := strings.CutPrefix(part, "max-age="); ok {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				age = time.Duration(n) * time.Second
			}
		}
	}
	return min(max(age, docMinAge), docMaxAge)
}

// checkRedirects checks the redirect addresses a client declares: at least
// one, a bounded number, each https or http on the person's own computer,
// none with credentials or a fragment.
func checkRedirects(uris []string) error {
	if len(uris) == 0 {
		return errors.New("it lists no redirect address")
	}
	if len(uris) > maxRedirects {
		return fmt.Errorf("it lists more than %d redirect addresses", maxRedirects)
	}
	for _, raw := range uris {
		u, err := url.Parse(raw)
		if err != nil || len(raw) > maxURLLen || u.Host == "" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") {
			return errors.New("it lists a redirect address that is not usable")
		}
		if u.Scheme != "https" && !loopback(u) {
			return errors.New("it lists a redirect address that is neither https nor on the person's own computer")
		}
	}
	return nil
}

// loopback reports whether u is plain http on the person's own computer.
func loopback(u *url.URL) bool {
	if u.Scheme != "http" {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// MatchRedirect is the redirect address rule (ADR 0022 point 11): the
// requested address must be one the client declared, character for
// character; except that for an http address on the person's own computer
// (127.0.0.1, [::1], localhost) the port is ignored, because such clients
// listen on a port they pick when they start. Host, path and query still
// have to be equal, and localhost is not 127.0.0.1.
func MatchRedirect(declared []string, requested string) bool {
	u, err := url.Parse(requested)
	if err != nil || requested == "" || len(requested) > maxURLLen || u.User != nil || u.Fragment != "" || strings.Contains(requested, "#") {
		return false
	}
	for _, raw := range declared {
		if raw == requested {
			return true
		}
		d, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if loopback(u) && loopback(d) && u.Hostname() == d.Hostname() && u.EscapedPath() == d.EscapedPath() && u.RawQuery == d.RawQuery {
			return true
		}
	}
	return false
}

// cleanName keeps what of a client's own name can be shown on a page: letters,
// digits, spaces and plain punctuation, at most maxNameLen characters. The
// name is shown as text and never trusted; this only keeps it readable.
func cleanName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if n >= maxNameLen {
			break
		}
		if r == ' ' || unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_.,()&+'", r) {
			b.WriteRune(r)
			n++
		}
	}
	return strings.TrimSpace(b.String())
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
