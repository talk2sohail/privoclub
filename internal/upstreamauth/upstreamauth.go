// Package upstreamauth is Privo's side of OAuth as a client of upstream MCP
// servers (ADR 0017): discover the authorization server of a server, register
// a client with it, build the authorization request, and exchange, refresh and
// revoke tokens.
//
// Every request goes through the HTTP client the caller gives, which in
// production is the egress client (ADR 0008). Answers are bounded, redirects
// are not followed, and errors never repeat what a server sent (a token, a code
// or upstream text cannot reach a log through this package). Token prints as
// redacted.
package upstreamauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Limits on what an upstream can make this process read or wait for.
const (
	MaxBody        = 256 << 10
	DefaultTimeout = 10 * time.Second
	maxScopes      = 50
	maxScopeLen    = 200
)

// Errors callers act on.
var (
	// ErrNoAuthNeeded: the server answered without asking for authorization.
	ErrNoAuthNeeded = errors.New("upstreamauth: the server did not ask for authorization")
	// ErrNoRegistration: there is no way to get a client id from this
	// authorization server without the admin's help.
	ErrNoRegistration = errors.New("upstreamauth: no way to register a client")
	// ErrInvalidGrant: the authorization server refused the grant (expired,
	// revoked or reused refresh token, wrong code). The grant is dead.
	ErrInvalidGrant = errors.New("upstreamauth: the authorization server refused the grant")
	// ErrInvalidClient: the client registration is no longer accepted.
	ErrInvalidClient = errors.New("upstreamauth: the authorization server refused the client")
)

// Client makes the requests. HTTP should be the egress client.
type Client struct {
	HTTP    *http.Client
	Timeout time.Duration // per request; zero means DefaultTimeout
	// UserAgent is sent with every request.
	UserAgent string
}

// ResourceMetadata is a protected resource metadata document (RFC 9728).
type ResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// ServerMetadata is an authorization server metadata document (RFC 8414 or
// OpenID discovery), the fields Privo uses.
type ServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	ScopesSupported                   []string `json:"scopes_supported"`
	IssParameterSupported             bool     `json:"authorization_response_iss_parameter_supported"`
	CIMDSupported                     bool     `json:"client_id_metadata_document_supported"`
}

// Discovery is what Discover found.
type Discovery struct {
	// Resource is the value to send as the resource parameter (RFC 8707): the
	// protected resource's identifier, which the server URL lies under.
	Resource string
	// Scopes to ask for: from the 401's scope parameter, else the resource's
	// scopes_supported.
	Scopes []string
	Server ServerMetadata
}

// Discover finds the authorization server of the MCP server at serverURL
// (transport "http" or "sse"). It asks the server without a token, follows the
// resource_metadata of the 401 (or the well-known locations), checks that the
// resource matches serverURL, fetches the authorization server's metadata,
// checks its issuer, and refuses one without S256. expectIssuer, when set, must
// be among the resource's authorization servers.
func (c *Client) Discover(ctx context.Context, serverURL, transport, expectIssuer string) (Discovery, error) {
	su, err := parseHTTPS(serverURL)
	if err != nil {
		return Discovery{}, fmt.Errorf("server URL: %w", err)
	}
	challenge, err := c.probe(ctx, su, transport)
	if err != nil {
		return Discovery{}, err
	}
	rm, err := c.resourceMetadata(ctx, su, challenge.resourceMetadata)
	if err != nil {
		return Discovery{}, err
	}
	if !resourceCovers(rm.Resource, su) {
		return Discovery{}, errors.New("the protected resource metadata names a different resource than the server URL")
	}
	if len(rm.AuthorizationServers) == 0 {
		return Discovery{}, errors.New("the protected resource metadata names no authorization server")
	}
	issuer := rm.AuthorizationServers[0]
	if expectIssuer != "" {
		if !slices.Contains(rm.AuthorizationServers, expectIssuer) {
			return Discovery{}, errors.New("the expected authorization server is not one the resource names")
		}
		issuer = expectIssuer
	}
	sm, err := c.serverMetadata(ctx, issuer)
	if err != nil {
		return Discovery{}, err
	}
	d := Discovery{Resource: rm.Resource, Server: sm}
	switch {
	case len(challenge.scopes) > 0:
		d.Scopes = challenge.scopes
	default:
		d.Scopes = cleanScopes(rm.ScopesSupported)
	}
	return d, nil
}

type bearerChallenge struct {
	resourceMetadata string
	scopes           []string
	err              string
}

// probe makes an MCP request without a token and expects a 401.
func (c *Client) probe(ctx context.Context, su *url.URL, transport string) (bearerChallenge, error) {
	var req *http.Request
	var err error
	if transport == "sse" {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, su.String(), nil)
		if err == nil {
			req.Header.Set("Accept", "text/event-stream")
		}
	} else {
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"privo","version":"probe"}}}`
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, su.String(), strings.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
		}
	}
	if err != nil {
		return bearerChallenge{}, err
	}
	resp, err := c.do(req, true)
	if err != nil {
		return bearerChallenge{}, fmt.Errorf("asking the server: %w", err)
	}
	if resp.status != http.StatusUnauthorized {
		if resp.status < 300 {
			return bearerChallenge{}, ErrNoAuthNeeded
		}
		return bearerChallenge{}, fmt.Errorf("the server answered %d instead of asking for authorization", resp.status)
	}
	return parseBearer(resp.header.Values("WWW-Authenticate")), nil
}

// ParseBearerError returns the error code of a Bearer challenge
// ("invalid_token", "insufficient_scope", ...) and the scopes it asks for.
func ParseBearerError(h http.Header) (code string, scopes []string) {
	ch := parseBearer(h.Values("WWW-Authenticate"))
	return ch.err, ch.scopes
}

func parseBearer(values []string) bearerChallenge {
	var ch bearerChallenge
	for _, v := range values {
		scheme, rest, _ := strings.Cut(strings.TrimSpace(v), " ")
		if !strings.EqualFold(scheme, "Bearer") {
			continue
		}
		params := parseAuthParams(rest)
		ch.resourceMetadata = params["resource_metadata"]
		ch.scopes = cleanScopes(strings.Fields(params["scope"]))
		if e := params["error"]; oauthErrRe.MatchString(e) {
			ch.err = e
		}
		break
	}
	return ch
}

// parseAuthParams reads comma-separated name=value pairs where values may be
// quoted strings with backslash escapes (RFC 9110 section 11.2).
func parseAuthParams(s string) map[string]string {
	out := map[string]string{}
	for len(s) > 0 && len(out) < 20 {
		s = strings.TrimLeft(s, " \t,")
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			break
		}
		name := strings.ToLower(strings.TrimSpace(s[:eq]))
		s = strings.TrimLeft(s[eq+1:], " \t")
		var val string
		if strings.HasPrefix(s, `"`) {
			var b strings.Builder
			i := 1
			for ; i < len(s); i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
					b.WriteByte(s[i])
					continue
				}
				if s[i] == '"' {
					break
				}
				b.WriteByte(s[i])
			}
			val = b.String()
			if i < len(s) {
				i++
			}
			s = s[i:]
		} else {
			end := strings.IndexByte(s, ',')
			if end < 0 {
				end = len(s)
			}
			val = strings.TrimSpace(s[:end])
			s = s[end:]
		}
		if _, dup := out[name]; !dup {
			out[name] = val
		}
	}
	return out
}

// resourceMetadata fetches the document named by the challenge, or tries the
// well-known locations: with the server's path inserted, then at the root.
func (c *Client) resourceMetadata(ctx context.Context, su *url.URL, fromChallenge string) (ResourceMetadata, error) {
	var candidates []string
	if fromChallenge != "" {
		u, err := parseHTTPS(fromChallenge)
		if err != nil {
			return ResourceMetadata{}, fmt.Errorf("resource_metadata: %w", err)
		}
		if u.Host != su.Host {
			return ResourceMetadata{}, errors.New("the resource_metadata URL is on another host than the server")
		}
		candidates = []string{u.String()}
	} else {
		root := su.Scheme + "://" + su.Host
		if p := strings.TrimSuffix(su.EscapedPath(), "/"); p != "" {
			candidates = append(candidates, root+"/.well-known/oauth-protected-resource"+p)
		}
		candidates = append(candidates, root+"/.well-known/oauth-protected-resource")
	}
	var lastErr error
	for _, u := range candidates {
		var rm ResourceMetadata
		if err := c.getJSON(ctx, u, &rm); err != nil {
			lastErr = keepWorse(lastErr, err)
			continue
		}
		if rm.Resource == "" {
			lastErr = errors.New("the protected resource metadata has no resource")
			continue
		}
		return rm, nil
	}
	var nt *notThereError
	if errors.As(lastErr, &nt) {
		return ResourceMetadata{}, fmt.Errorf("%w: %w", ErrNoResourceMetadata, lastErr)
	}
	return ResourceMetadata{}, fmt.Errorf("protected resource metadata: %w", lastErr)
}

// ErrNoResourceMetadata means the server publishes no protected resource
// metadata (RFC 9728), so it does not say where its people sign in. An
// administrator can name the authorization server, or its endpoints, by hand
// (ADR 0023).
var ErrNoResourceMetadata = errors.New("the server publishes no protected resource metadata")

// ServerMetadataFor fetches and checks an authorization server's metadata by
// its issuer, for a server that does not name its authorization server.
func (c *Client) ServerMetadataFor(ctx context.Context, issuer string) (ServerMetadata, error) {
	return c.serverMetadata(ctx, issuer)
}

// CheckEndpoint checks an endpoint entered by hand: https (or http on a
// loopback address, for tests), a host, no credentials or fragment.
func CheckEndpoint(raw string) error {
	_, err := parseHTTPS(raw)
	return err
}

// serverMetadata fetches the authorization server's metadata in the order of
// the MCP authorization spec and checks the issuer.
func (c *Client) serverMetadata(ctx context.Context, issuer string) (ServerMetadata, error) {
	iu, err := parseHTTPS(issuer)
	if err != nil {
		return ServerMetadata{}, fmt.Errorf("authorization server: %w", err)
	}
	if iu.RawQuery != "" || iu.Fragment != "" {
		return ServerMetadata{}, errors.New("authorization server: an issuer has no query or fragment")
	}
	root := iu.Scheme + "://" + iu.Host
	p := strings.TrimSuffix(iu.EscapedPath(), "/")
	candidates := []string{root + "/.well-known/oauth-authorization-server" + p}
	if p != "" {
		candidates = append(candidates, root+"/.well-known/openid-configuration"+p, root+p+"/.well-known/openid-configuration")
	} else {
		candidates = append(candidates, root+"/.well-known/openid-configuration")
	}
	var lastErr error
	for _, u := range candidates {
		var sm ServerMetadata
		if err := c.getJSON(ctx, u, &sm); err != nil {
			lastErr = keepWorse(lastErr, err)
			continue
		}
		if sm.Issuer != issuer {
			return ServerMetadata{}, errors.New("authorization server metadata: the issuer differs from the address it was fetched for")
		}
		if !slices.Contains(sm.CodeChallengeMethodsSupported, "S256") {
			return ServerMetadata{}, errors.New("authorization server does not support PKCE with S256")
		}
		for name, e := range map[string]string{"authorization_endpoint": sm.AuthorizationEndpoint, "token_endpoint": sm.TokenEndpoint} {
			if _, err := parseHTTPS(e); err != nil {
				return ServerMetadata{}, fmt.Errorf("authorization server metadata: %s: %w", name, err)
			}
		}
		for name, e := range map[string]*string{"revocation_endpoint": &sm.RevocationEndpoint, "registration_endpoint": &sm.RegistrationEndpoint} {
			if *e == "" {
				continue
			}
			if _, err := parseHTTPS(*e); err != nil {
				return ServerMetadata{}, fmt.Errorf("authorization server metadata: %s: %w", name, err)
			}
		}
		return sm, nil
	}
	return ServerMetadata{}, fmt.Errorf("authorization server metadata: %w", lastErr)
}

// Registration methods, in the order they are tried.
const (
	RegistrationAdmin   = "admin"
	RegistrationCIMD    = "cimd"
	RegistrationDynamic = "dynamic"
)

// ChooseRegistration picks how Privo gets a client id: the admin's, Privo's
// client ID metadata document, or dynamic registration. With none it returns
// ErrNoRegistration wrapped in a message that says what the admin must supply.
func ChooseRegistration(adminClientID string, sm ServerMetadata, callbackURL string) (string, error) {
	switch {
	case adminClientID != "":
		return RegistrationAdmin, nil
	case sm.CIMDSupported:
		return RegistrationCIMD, nil
	case sm.RegistrationEndpoint != "":
		return RegistrationDynamic, nil
	}
	return "", fmt.Errorf("%w: the authorization server registers no clients by itself; create an OAuth app with the provider, set its callback URL to %s, and give its client id (and secret, if it has one)",
		ErrNoRegistration, callbackURL)
}

// CallbackURL is the one redirect URI Privo registers.
func CallbackURL(publicURL *url.URL) string {
	return publicURL.JoinPath("connect", "callback").String()
}

// ClientMetadataURL is Privo's client id under CIMD.
func ClientMetadataURL(publicURL *url.URL) string {
	return publicURL.JoinPath("oauth", "client-metadata.json").String()
}

// ClientMetadata is the document served at ClientMetadataURL (and the body of
// a dynamic registration request, without client_id).
func ClientMetadata(publicURL *url.URL, withClientID bool) []byte {
	doc := map[string]any{
		"client_name":                "Privo (" + publicURL.Host + ")",
		"client_uri":                 publicURL.String(),
		"redirect_uris":              []string{CallbackURL(publicURL)},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"application_type":           "web",
	}
	if withClientID {
		doc["client_id"] = ClientMetadataURL(publicURL)
	}
	b, _ := json.Marshal(doc)
	return b
}

// Registered is the result of a dynamic registration.
type Registered struct {
	ClientID     string
	ClientSecret string // empty for a public client
	AuthMethod   string // token endpoint auth method to use
}

// Register performs dynamic client registration (RFC 7591).
func (c *Client) Register(ctx context.Context, endpoint string, publicURL *url.URL) (Registered, error) {
	if _, err := parseHTTPS(endpoint); err != nil {
		return Registered{}, fmt.Errorf("registration endpoint: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(ClientMetadata(publicURL, false)))
	if err != nil {
		return Registered{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.do(req, false)
	if err != nil {
		return Registered{}, fmt.Errorf("registration: %w", err)
	}
	body := resp.body
	if resp.status != http.StatusCreated && resp.status != http.StatusOK {
		return Registered{}, fmt.Errorf("registration: the authorization server answered %d", resp.status)
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		AuthMethod   string `json:"token_endpoint_auth_method"`
	}
	if err := json.Unmarshal(body, &out); err != nil || !clientIDRe.MatchString(out.ClientID) || len(out.ClientSecret) > 512 {
		return Registered{}, errors.New("registration: the answer is not a client registration")
	}
	r := Registered{ClientID: out.ClientID, ClientSecret: out.ClientSecret, AuthMethod: "none"}
	if r.ClientSecret != "" {
		r.AuthMethod = "client_secret_basic"
		if out.AuthMethod == "client_secret_post" {
			r.AuthMethod = "client_secret_post"
		}
	}
	return r, nil
}

// TokenAuthMethod picks how to authenticate at the token endpoint with a
// client secret the admin entered.
func TokenAuthMethod(sm ServerMetadata, hasSecret bool) string {
	switch {
	case !hasSecret:
		return "none"
	case len(sm.TokenEndpointAuthMethodsSupported) == 0 || slices.Contains(sm.TokenEndpointAuthMethodsSupported, "client_secret_basic"):
		return "client_secret_basic"
	default:
		return "client_secret_post"
	}
}

// Endpoint is what token requests need to know about a server's registration.
type Endpoint struct {
	AuthorizationEndpoint string
	TokenEndpoint         string
	RevocationEndpoint    string
	Resource              string
	ClientID              string
	ClientSecret          string
	AuthMethod            string // none | client_secret_basic | client_secret_post
}

// NewVerifier returns a PKCE code verifier: 43 URL-safe characters.
func NewVerifier() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Challenge is the S256 code challenge for a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL is where the person's browser is sent.
func AuthorizeURL(e Endpoint, redirectURI, state, challenge string, scopes []string) (string, error) {
	u, err := parseHTTPS(e.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", e.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if e.Resource != "" {
		q.Set("resource", e.Resource)
	}
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Token is a grant's tokens. It never prints its secrets.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"`
	Scope        string    `json:"scope,omitempty"`
}

// String hides the tokens, so a Token passed to a logger or fmt prints nothing secret.
func (t Token) String() string { return "upstreamauth.Token{redacted}" }

// GoString hides the tokens for %#v.
func (t Token) GoString() string { return t.String() }

// Format hides the tokens for every verb, %+v included.
func (t Token) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, t.String()) }

// Expired reports whether the access token expires within skew of now.
func (t Token) Expired(now time.Time, skew time.Duration) bool {
	return !t.Expiry.IsZero() && !now.Add(skew).Before(t.Expiry)
}

// Exchange trades an authorization code for tokens.
func (c *Client) Exchange(ctx context.Context, e Endpoint, code, verifier, redirectURI string, now time.Time) (Token, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirectURI)
	if e.Resource != "" {
		form.Set("resource", e.Resource)
	}
	return c.token(ctx, e, form, "", now)
}

// Refresh uses a refresh token. When the authorization server does not rotate
// it, the old refresh token is kept.
func (c *Client) Refresh(ctx context.Context, e Endpoint, refreshToken string, now time.Time) (Token, error) {
	if refreshToken == "" {
		return Token{}, ErrInvalidGrant
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	if e.Resource != "" {
		form.Set("resource", e.Resource)
	}
	return c.token(ctx, e, form, refreshToken, now)
}

func (c *Client) token(ctx context.Context, e Endpoint, form url.Values, oldRefresh string, now time.Time) (Token, error) {
	if _, err := parseHTTPS(e.TokenEndpoint); err != nil {
		return Token{}, fmt.Errorf("token endpoint: %w", err)
	}
	switch e.AuthMethod {
	case "client_secret_post":
		form.Set("client_id", e.ClientID)
		form.Set("client_secret", e.ClientSecret)
	case "client_secret_basic":
	default:
		form.Set("client_id", e.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if e.AuthMethod == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(e.ClientID), url.QueryEscape(e.ClientSecret))
	}
	resp, err := c.do(req, false)
	if err != nil {
		return Token{}, fmt.Errorf("token request: %w", err)
	}
	body := resp.body
	var out struct {
		AccessToken  string          `json:"access_token"`
		TokenType    string          `json:"token_type"`
		RefreshToken string          `json:"refresh_token"`
		ExpiresIn    json.RawMessage `json:"expires_in"`
		Scope        string          `json:"scope"`
		Error        string          `json:"error"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.status != http.StatusOK || out.AccessToken == "" {
		switch out.Error {
		case "invalid_grant":
			return Token{}, ErrInvalidGrant
		case "invalid_client", "unauthorized_client":
			return Token{}, ErrInvalidClient
		}
		if oauthErrRe.MatchString(out.Error) {
			return Token{}, fmt.Errorf("token request: the authorization server answered %d (%s)", resp.status, out.Error)
		}
		return Token{}, fmt.Errorf("token request: the authorization server answered %d without a token", resp.status)
	}
	if !strings.EqualFold(out.TokenType, "bearer") {
		return Token{}, errors.New("token request: the token is not a bearer token")
	}
	if len(out.AccessToken) > 8192 || len(out.RefreshToken) > 8192 || len(out.Scope) > 4096 || !printable(out.AccessToken) || !printable(out.RefreshToken) {
		return Token{}, errors.New("token request: the answer is not a usable token")
	}
	t := Token{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, Scope: out.Scope}
	if t.RefreshToken == "" {
		t.RefreshToken = oldRefresh
	}
	if len(out.ExpiresIn) > 0 {
		secs, err := strconv.ParseInt(strings.Trim(string(out.ExpiresIn), `"`), 10, 64)
		if err != nil || secs < 0 || secs > 10*365*24*3600 {
			return Token{}, errors.New("token request: expires_in is not a number of seconds")
		}
		if secs > 0 {
			t.Expiry = now.Add(time.Duration(secs) * time.Second)
		}
	}
	return t, nil
}

// Revoke asks the authorization server to revoke a token (RFC 7009). A server
// without a revocation endpoint is not an error.
func (c *Client) Revoke(ctx context.Context, e Endpoint, token, hint string) error {
	if e.RevocationEndpoint == "" || token == "" {
		return nil
	}
	if _, err := parseHTTPS(e.RevocationEndpoint); err != nil {
		return fmt.Errorf("revocation endpoint: %w", err)
	}
	form := url.Values{}
	form.Set("token", token)
	if hint != "" {
		form.Set("token_type_hint", hint)
	}
	if e.AuthMethod != "client_secret_basic" {
		form.Set("client_id", e.ClientID)
	}
	if e.AuthMethod == "client_secret_post" {
		form.Set("client_secret", e.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if e.AuthMethod == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(e.ClientID), url.QueryEscape(e.ClientSecret))
	}
	resp, err := c.do(req, false)
	if err != nil {
		return fmt.Errorf("revocation: %w", err)
	}
	if resp.status != http.StatusOK {
		return fmt.Errorf("revocation: the authorization server answered %d", resp.status)
	}
	return nil
}

// ---- helpers

var (
	oauthErrRe = regexp.MustCompile(`^[a-z_]{1,40}$`)
	clientIDRe = regexp.MustCompile(`^[\x21-\x7e]{1,255}$`)
	scopeRe    = regexp.MustCompile(`^[\x21\x23-\x5b\x5d-\x7e]+$`) // RFC 6749 scope-token
)

func printable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func cleanScopes(in []string) []string {
	var out []string
	for _, s := range in {
		if len(s) <= maxScopeLen && scopeRe.MatchString(s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
		if len(out) == maxScopes {
			break
		}
	}
	return out
}

// CleanScopes keeps the valid, distinct scope tokens of in (at most 50).
func CleanScopes(in []string) []string { return cleanScopes(in) }

// parseHTTPS accepts an https URL, or http on a loopback host (development and
// tests; the egress policy still decides whether loopback may be dialled).
func parseHTTPS(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, errors.New("not a valid absolute URL")
	}
	if u.Fragment != "" {
		return nil, errors.New("a URL here has no fragment")
	}
	switch u.Scheme {
	case "https":
		return u, nil
	case "http":
		if isLoopback(u.Hostname()) {
			return u, nil
		}
	}
	return nil, errors.New("must use https")
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// resourceCovers reports whether the protected resource identifier covers the
// server URL: same origin, and the server's path is the resource's path or lies
// under it.
func resourceCovers(resource string, su *url.URL) bool {
	ru, err := parseHTTPS(resource)
	if err != nil || ru.RawQuery != "" {
		return false
	}
	if origin(ru) != origin(su) {
		return false
	}
	rp := strings.TrimSuffix(ru.EscapedPath(), "/")
	sp := strings.TrimSuffix(su.EscapedPath(), "/")
	return rp == "" || sp == rp || strings.HasPrefix(sp, rp+"/")
}

func origin(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	}
	return strings.ToLower(u.Scheme) + "://" + host
}

func (c *Client) getJSON(ctx context.Context, rawURL string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.do(req, false)
	if err != nil {
		return err
	}
	body := resp.body
	if resp.status == http.StatusNotFound {
		return &notThereError{fmt.Sprintf("%s answered 404", redactQuery(rawURL))}
	}
	if resp.status != http.StatusOK {
		return fmt.Errorf("%s answered %d", redactQuery(rawURL), resp.status)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("%s did not answer with JSON metadata", redactQuery(rawURL))
	}
	return nil
}

// notThereError is a location that does not exist: when several are tried, a
// real failure at another one is the more useful error.
type notThereError struct{ msg string }

func (e *notThereError) Error() string { return e.msg }

func keepWorse(prev, err error) error {
	var nt *notThereError
	if prev != nil && errors.As(err, &nt) {
		return prev
	}
	return err
}

// answer is what do read: the status, the headers and a bounded body.
type answer struct {
	status int
	header http.Header
	body   []byte
}

// do sends a request without following redirects and reads at most MaxBody
// bytes of the answer (discarding it when headersOnly).
func (c *Client) do(req *http.Request, headersOnly bool) (answer, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()
	base := c.HTTP
	if base == nil {
		return answer{}, errors.New("no HTTP client configured")
	}
	hc := *base
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := hc.Do(req.WithContext(ctx))
	if err != nil {
		var ne net.Error
		if (errors.Is(err, context.DeadlineExceeded) && req.Context().Err() == nil) || (errors.As(err, &ne) && ne.Timeout()) {
			return answer{}, &TimeoutError{Host: req.URL.Host, Wait: timeout}
		}
		return answer{}, transportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	a := answer{status: resp.StatusCode, header: resp.Header}
	if headersOnly {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxBody))
		return a, nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return answer{}, errors.New("reading the answer failed")
	}
	if len(b) > MaxBody {
		return answer{}, errors.New("the answer is too large")
	}
	a.body = b
	return a, nil
}

// TimeoutError is a request that got no answer in time. It names the host
// only: the URL may carry a code or a token in its query.
type TimeoutError struct {
	Host string
	Wait time.Duration
}

func (e *TimeoutError) Error() string {
	n := int(e.Wait.Round(time.Second) / time.Second)
	unit := "seconds"
	if n == 1 {
		unit = "second"
	}
	return fmt.Sprintf("%s did not answer within %d %s", e.Host, n, unit)
}

// transportError describes a transport error without the request URL (which
// may carry a code or a token in its query).
func transportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("request failed: %w", err)
}

func redactQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "the address"
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String()
}
