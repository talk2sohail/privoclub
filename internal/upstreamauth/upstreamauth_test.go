package upstreamauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"privo.club/privo/internal/egress"
)

// fakeUpstream is an MCP server and its authorization server on one host.
type fakeUpstream struct {
	srv *httptest.Server
	mu  sync.Mutex

	challenge       string // WWW-Authenticate of the 401; "" means the default
	noAuth          bool   // the MCP endpoint answers 200
	prmResource     string // "" means the server URL
	prmPath         string // where the PRM is served ("" means /.well-known/oauth-protected-resource/mcp)
	asMeta          map[string]any
	asMetaPath      string // "" means /.well-known/oauth-authorization-server
	tokenAnswers    []tokenAnswer
	tokenRequests   []url.Values
	tokenAuth       []string
	registerAnswer  string
	registerStatus  int
	registerBodies  []map[string]any
	revoked         []url.Values
	hugeMetadata    bool
	redirectMeta    string
	requestsByPaths []string
}

type tokenAnswer struct {
	status int
	body   string
}

func newFake(t *testing.T) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeUpstream) url(p string) string { return f.srv.URL + p }

func (f *fakeUpstream) defaultASMeta() map[string]any {
	return map[string]any{
		"issuer":                           f.srv.URL,
		"authorization_endpoint":           f.url("/authorize"),
		"token_endpoint":                   f.url("/token"),
		"revocation_endpoint":              f.url("/revoke"),
		"registration_endpoint":            f.url("/register"),
		"code_challenge_methods_supported": []string{"S256"},
	}
}

func (f *fakeUpstream) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requestsByPaths = append(f.requestsByPaths, r.URL.Path)
	prmPath := f.prmPath
	if prmPath == "" {
		prmPath = "/.well-known/oauth-protected-resource/mcp"
	}
	asPath := f.asMetaPath
	if asPath == "" {
		asPath = "/.well-known/oauth-authorization-server"
	}
	switch r.URL.Path {
	case "/mcp":
		if f.noAuth {
			w.WriteHeader(http.StatusOK)
			return
		}
		ch := f.challenge
		if ch == "" {
			ch = `Bearer resource_metadata="` + f.url(prmPath) + `", scope="read write"`
		}
		if ch != "none" {
			w.Header().Set("WWW-Authenticate", ch)
		}
		w.WriteHeader(http.StatusUnauthorized)
	case prmPath:
		res := f.prmResource
		if res == "" {
			res = f.url("/mcp")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": res, "authorization_servers": []string{f.srv.URL}, "scopes_supported": []string{"read", "write", "admin"}})
	case asPath:
		if f.redirectMeta != "" {
			http.Redirect(w, r, f.redirectMeta, http.StatusFound)
			return
		}
		if f.hugeMetadata {
			_, _ = w.Write([]byte(`{"issuer":"` + strings.Repeat("a", MaxBody+10) + `"}`))
			return
		}
		m := f.asMeta
		if m == nil {
			m = f.defaultASMeta()
		}
		_ = json.NewEncoder(w).Encode(m)
	case "/token":
		_ = r.ParseForm()
		f.tokenRequests = append(f.tokenRequests, r.PostForm)
		f.tokenAuth = append(f.tokenAuth, r.Header.Get("Authorization"))
		a := tokenAnswer{status: 200, body: `{"access_token":"at-1","token_type":"Bearer","refresh_token":"rt-1","expires_in":3600,"scope":"read write"}`}
		if len(f.tokenAnswers) > 0 {
			a, f.tokenAnswers = f.tokenAnswers[0], f.tokenAnswers[1:]
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
	case "/register":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.registerBodies = append(f.registerBodies, body)
		st := f.registerStatus
		if st == 0 {
			st = http.StatusCreated
		}
		w.WriteHeader(st)
		ans := f.registerAnswer
		if ans == "" {
			ans = `{"client_id":"dyn-123","client_secret":"dyn-secret","token_endpoint_auth_method":"client_secret_post"}`
		}
		_, _ = w.Write([]byte(ans))
	case "/revoke":
		_ = r.ParseForm()
		f.revoked = append(f.revoked, r.PostForm)
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func testClient() *Client {
	return &Client{HTTP: egress.NewClient(egress.Options{Policy: egress.Policy{Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}, SameHostOnly: true, MaxRedirects: 1}), Timeout: 2 * time.Second}
}

func TestResourceMetadataFromWWWAuthenticate(t *testing.T) {
	f := newFake(t)
	f.prmPath = "/custom/prm" // only reachable through the challenge
	d, err := testClient().Discover(context.Background(), f.url("/mcp"), "http", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Resource != f.url("/mcp") || d.Server.Issuer != f.srv.URL || d.Server.TokenEndpoint != f.url("/token") {
		t.Fatalf("discovery = %+v", d)
	}
	if strings.Join(d.Scopes, " ") != "read write" {
		t.Errorf("scopes = %v, want the challenge's scope", d.Scopes)
	}
}

func TestResourceMetadataWellKnownFallback(t *testing.T) {
	f := newFake(t)
	f.challenge = `Bearer realm="x"` // no resource_metadata
	d, err := testClient().Discover(context.Background(), f.url("/mcp"), "http", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(d.Scopes, " ") != "read write admin" {
		t.Errorf("scopes = %v, want scopes_supported", d.Scopes)
	}
	// The path-inserted location is tried first.
	f.mu.Lock()
	paths := strings.Join(f.requestsByPaths, " ")
	f.mu.Unlock()
	if !strings.Contains(paths, "/.well-known/oauth-protected-resource/mcp") {
		t.Errorf("requests: %s", paths)
	}

	// Root location when the path-inserted one is missing.
	g := newFake(t)
	g.challenge = "none"
	g.prmPath = "/.well-known/oauth-protected-resource"
	g.prmResource = g.srv.URL // the whole origin is the resource
	if _, err := testClient().Discover(context.Background(), g.url("/mcp"), "http", ""); err != nil {
		t.Fatalf("root PRM: %v", err)
	}
}

func TestResourceMismatchRefused(t *testing.T) {
	for name, res := range map[string]string{
		"other path":   "/other",
		"other origin": "https://evil.example/mcp",
		"sibling":      "/mc",
	} {
		f := newFake(t)
		if strings.HasPrefix(res, "/") {
			res = f.url(res)
		}
		f.prmResource = res
		if _, err := testClient().Discover(context.Background(), f.url("/mcp"), "http", ""); err == nil || !strings.Contains(err.Error(), "different resource") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A resource_metadata URL on another host is refused before it is fetched.
	f := newFake(t)
	f.challenge = `Bearer resource_metadata="https://evil.example/prm"`
	if _, err := testClient().Discover(context.Background(), f.url("/mcp"), "http", ""); err == nil || !strings.Contains(err.Error(), "another host") {
		t.Errorf("foreign resource_metadata: %v", err)
	}
}

func TestAuthServerMetadataRFC8414ThenOIDC(t *testing.T) {
	f := newFake(t)
	f.asMetaPath = "/.well-known/openid-configuration"
	d, err := testClient().Discover(context.Background(), f.url("/mcp"), "http", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Server.Issuer != f.srv.URL {
		t.Fatalf("issuer %q", d.Server.Issuer)
	}
	f.mu.Lock()
	paths := strings.Join(f.requestsByPaths, " ")
	f.mu.Unlock()
	if strings.Index(paths, "oauth-authorization-server") > strings.Index(paths, "openid-configuration") {
		t.Errorf("RFC 8414 must be tried before OpenID discovery: %s", paths)
	}
}

func TestIssuerMismatchRefused(t *testing.T) {
	f := newFake(t)
	m := f.defaultASMeta()
	m["issuer"] = "https://evil.example"
	f.asMeta = m
	if _, err := testClient().Discover(context.Background(), f.url("/mcp"), "http", ""); err == nil || !strings.Contains(err.Error(), "issuer differs") {
		t.Fatalf("issuer mismatch: %v", err)
	}
	// An expected issuer the resource does not name is refused.
	g := newFake(t)
	if _, err := testClient().Discover(context.Background(), g.url("/mcp"), "http", "https://other.example"); err == nil {
		t.Fatal("unexpected issuer accepted")
	}
	if _, err := testClient().Discover(context.Background(), g.url("/mcp"), "http", g.srv.URL); err != nil {
		t.Fatalf("expected issuer: %v", err)
	}
}

func TestAuthServerWithoutS256Refused(t *testing.T) {
	for name, methods := range map[string]any{"plain only": []string{"plain"}, "none listed": nil} {
		f := newFake(t)
		m := f.defaultASMeta()
		m["code_challenge_methods_supported"] = methods
		f.asMeta = m
		if _, err := testClient().Discover(context.Background(), f.url("/mcp"), "http", ""); err == nil || !strings.Contains(err.Error(), "S256") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Endpoints must be https (or loopback http).
	f := newFake(t)
	m := f.defaultASMeta()
	m["token_endpoint"] = "http://as.example/token"
	f.asMeta = m
	if _, err := testClient().Discover(context.Background(), f.url("/mcp"), "http", ""); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("plain http token endpoint: %v", err)
	}
}

func TestDiscoveryRefusesPrivateAddressesAndOtherHosts(t *testing.T) {
	f := newFake(t)
	strict := &Client{HTTP: egress.NewClient(egress.Options{SameHostOnly: true}), Timeout: time.Second}
	if _, err := strict.Discover(context.Background(), f.url("/mcp"), "http", ""); err == nil || !errors.Is(err, egress.ErrBlocked) {
		t.Errorf("loopback without an allow list: %v", err)
	}
	// Redirects are not followed.
	g := newFake(t)
	g.redirectMeta = "https://evil.example/meta"
	if _, err := testClient().Discover(context.Background(), g.url("/mcp"), "http", ""); err == nil {
		t.Error("metadata redirect followed")
	}
	// A server that does not ask for authorization.
	h := newFake(t)
	h.noAuth = true
	if _, err := testClient().Discover(context.Background(), h.url("/mcp"), "http", ""); !errors.Is(err, ErrNoAuthNeeded) {
		t.Errorf("no auth: %v", err)
	}
	// Plain http on a public host is refused before anything is sent.
	if _, err := testClient().Discover(context.Background(), "http://mcp.example/mcp", "http", ""); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("http server URL: %v", err)
	}
}

func TestDiscoveryAnswersAreBounded(t *testing.T) {
	f := newFake(t)
	f.hugeMetadata = true
	_, err := testClient().Discover(context.Background(), f.url("/mcp"), "http", "")
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("huge metadata: %v", err)
	}
	if strings.Contains(err.Error(), "aaaa") {
		t.Error("error repeats the answer")
	}
}

func TestRegistrationOrder(t *testing.T) {
	cb := "https://privo.example/connect/callback"
	sm := ServerMetadata{CIMDSupported: true, RegistrationEndpoint: "https://as.example/register"}
	for _, c := range []struct {
		admin string
		sm    ServerMetadata
		want  string
	}{
		{"given", sm, RegistrationAdmin},
		{"", sm, RegistrationCIMD},
		{"", ServerMetadata{RegistrationEndpoint: "https://as.example/register"}, RegistrationDynamic},
	} {
		if got, err := ChooseRegistration(c.admin, c.sm, cb); err != nil || got != c.want {
			t.Errorf("ChooseRegistration(%q, %+v) = %q, %v; want %q", c.admin, c.sm, got, err, c.want)
		}
	}
}

func TestNoRegistrationPathNamesWhatToSupply(t *testing.T) {
	cb := "https://privo.example/connect/callback"
	_, err := ChooseRegistration("", ServerMetadata{}, cb)
	if !errors.Is(err, ErrNoRegistration) || !strings.Contains(err.Error(), cb) || !strings.Contains(err.Error(), "client id") {
		t.Fatalf("no registration: %v", err)
	}
}

func TestClientMetadataDocument(t *testing.T) {
	pub, _ := url.Parse("https://privo.example")
	var doc map[string]any
	if err := json.Unmarshal(ClientMetadata(pub, true), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["client_id"] != "https://privo.example/oauth/client-metadata.json" || doc["client_id"] != ClientMetadataURL(pub) {
		t.Errorf("client_id = %v", doc["client_id"])
	}
	uris, _ := doc["redirect_uris"].([]any)
	if len(uris) != 1 || uris[0] != "https://privo.example/connect/callback" {
		t.Errorf("redirect_uris = %v", doc["redirect_uris"])
	}
	if doc["token_endpoint_auth_method"] != "none" || doc["application_type"] != "web" {
		t.Errorf("doc = %v", doc)
	}
	if _, ok := doc["client_secret"]; ok {
		t.Error("a client secret in a public document")
	}
}

func TestDynamicRegistration(t *testing.T) {
	f := newFake(t)
	pub, _ := url.Parse("https://privo.example")
	r, err := testClient().Register(context.Background(), f.url("/register"), pub)
	if err != nil {
		t.Fatal(err)
	}
	if r.ClientID != "dyn-123" || r.ClientSecret != "dyn-secret" || r.AuthMethod != "client_secret_post" {
		t.Fatalf("registered %+v", r)
	}
	body := f.registerBodies[0]
	if body["client_id"] != nil || body["client_name"] != "Privo (privo.example)" {
		t.Errorf("registration body %v", body)
	}
	// Public client answer.
	f.registerAnswer = `{"client_id":"pub-1"}`
	if r, err := testClient().Register(context.Background(), f.url("/register"), pub); err != nil || r.AuthMethod != "none" || r.ClientSecret != "" {
		t.Errorf("public registration %+v %v", r, err)
	}
	// Refusals and garbage, without repeating the answer.
	f.registerStatus, f.registerAnswer = http.StatusBadRequest, `{"error":"invalid_redirect_uri","error_description":"<script>"}`
	if _, err := testClient().Register(context.Background(), f.url("/register"), pub); err == nil || strings.Contains(err.Error(), "script") {
		t.Errorf("refused registration: %v", err)
	}
	f.registerStatus, f.registerAnswer = http.StatusCreated, `{"client_id":"has space"}`
	if _, err := testClient().Register(context.Background(), f.url("/register"), pub); err == nil {
		t.Error("invalid client id accepted")
	}
}

func endpointOf(f *fakeUpstream, method string) Endpoint {
	return Endpoint{AuthorizationEndpoint: f.url("/authorize"), TokenEndpoint: f.url("/token"), RevocationEndpoint: f.url("/revoke"),
		Resource: f.url("/mcp"), ClientID: "client-1", ClientSecret: "secret-1", AuthMethod: method}
}

func TestAuthorizeURLCarriesResourceScopesAndS256(t *testing.T) {
	f := newFake(t)
	raw, err := AuthorizeURL(endpointOf(f, "none"), "https://privo.example/connect/callback", "st", Challenge("v"), []string{"read", "write"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	for k, want := range map[string]string{"response_type": "code", "client_id": "client-1", "redirect_uri": "https://privo.example/connect/callback",
		"state": "st", "code_challenge": Challenge("v"), "code_challenge_method": "S256", "resource": f.url("/mcp"), "scope": "read write"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	// The challenge is the RFC 7636 example.
	if Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk") != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Error("S256 challenge does not match RFC 7636")
	}
}

func TestExchangeSendsVerifierResourceAndClientAuth(t *testing.T) {
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	for _, method := range []string{"none", "client_secret_basic", "client_secret_post"} {
		f := newFake(t)
		tok, err := testClient().Exchange(context.Background(), endpointOf(f, method), "code-1", "verifier-1", "https://privo.example/connect/callback", now)
		if err != nil {
			t.Fatal(err)
		}
		if tok.AccessToken != "at-1" || tok.RefreshToken != "rt-1" || !tok.Expiry.Equal(now.Add(time.Hour)) || tok.Scope != "read write" {
			t.Fatalf("%s: token %#v", method, tok.Expiry)
		}
		form := f.tokenRequests[0]
		for k, want := range map[string]string{"grant_type": "authorization_code", "code": "code-1", "code_verifier": "verifier-1",
			"redirect_uri": "https://privo.example/connect/callback", "resource": f.url("/mcp")} {
			if form.Get(k) != want {
				t.Errorf("%s: %s = %q", method, k, form.Get(k))
			}
		}
		switch method {
		case "client_secret_basic":
			if !strings.HasPrefix(f.tokenAuth[0], "Basic ") || form.Get("client_secret") != "" {
				t.Errorf("basic auth not used: %q %v", f.tokenAuth[0], form)
			}
		case "client_secret_post":
			if form.Get("client_secret") != "secret-1" || f.tokenAuth[0] != "" {
				t.Errorf("post auth not used")
			}
		default:
			if form.Get("client_id") != "client-1" || form.Get("client_secret") != "" || f.tokenAuth[0] != "" {
				t.Errorf("public client sent a secret")
			}
		}
	}
}

func TestRefreshSendsResourceAndKeepsOldRefreshToken(t *testing.T) {
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	f := newFake(t)
	f.tokenAnswers = []tokenAnswer{{200, `{"access_token":"at-2","token_type":"bearer","expires_in":"60"}`}}
	tok, err := testClient().Refresh(context.Background(), endpointOf(f, "none"), "rt-old", now)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at-2" || tok.RefreshToken != "rt-old" || !tok.Expiry.Equal(now.Add(time.Minute)) {
		t.Fatal("refresh did not keep the old refresh token or read expires_in")
	}
	form := f.tokenRequests[0]
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "rt-old" || form.Get("resource") != f.url("/mcp") {
		t.Errorf("refresh form %v", form)
	}
	if _, err := testClient().Refresh(context.Background(), endpointOf(f, "none"), "", now); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("no refresh token: %v", err)
	}
}

func TestTokenErrorsAreClassifiedAndNotRepeated(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		ans  tokenAnswer
		want error
	}{
		{tokenAnswer{400, `{"error":"invalid_grant","error_description":"secret-leak"}`}, ErrInvalidGrant},
		{tokenAnswer{401, `{"error":"invalid_client"}`}, ErrInvalidClient},
		{tokenAnswer{500, `secret-leak`}, nil},
		{tokenAnswer{200, `{"access_token":"x","token_type":"mac"}`}, nil},
		{tokenAnswer{200, `{"access_token":"has space","token_type":"bearer"}`}, nil},
		{tokenAnswer{200, `{"access_token":"x","token_type":"bearer","expires_in":"soon"}`}, nil},
	} {
		f := newFake(t)
		f.tokenAnswers = []tokenAnswer{c.ans}
		_, err := testClient().Refresh(context.Background(), endpointOf(f, "none"), "rt", now)
		if err == nil {
			t.Errorf("%s: accepted", c.ans.body)
			continue
		}
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.ans.body, err, c.want)
		}
		if strings.Contains(err.Error(), "secret-leak") || strings.Contains(err.Error(), "rt") && strings.Contains(err.Error(), "refresh_token=") {
			t.Errorf("error repeats what the server sent: %v", err)
		}
	}
}

func TestRevoke(t *testing.T) {
	f := newFake(t)
	if err := testClient().Revoke(context.Background(), endpointOf(f, "client_secret_post"), "rt-1", "refresh_token"); err != nil {
		t.Fatal(err)
	}
	if f.revoked[0].Get("token") != "rt-1" || f.revoked[0].Get("token_type_hint") != "refresh_token" || f.revoked[0].Get("client_secret") != "secret-1" {
		t.Errorf("revocation form %v", f.revoked[0])
	}
	e := endpointOf(f, "none")
	e.RevocationEndpoint = ""
	if err := testClient().Revoke(context.Background(), e, "rt-1", ""); err != nil {
		t.Errorf("no revocation endpoint: %v", err)
	}
}

func TestTokenNeverPrints(t *testing.T) {
	tok := Token{AccessToken: "at-secret", RefreshToken: "rt-secret", Scope: "s"}
	for _, s := range []string{fmt.Sprint(tok), fmt.Sprintf("%v %+v %#v %s", tok, tok, tok, tok), fmt.Sprintf("%v", &tok)} {
		if strings.Contains(s, "secret") {
			t.Errorf("printed: %s", s)
		}
	}
	// JSON is how the vault stores it, so it does carry the tokens.
	b, _ := json.Marshal(tok)
	var back Token
	_ = json.Unmarshal(b, &back)
	if back.AccessToken != "at-secret" || back.RefreshToken != "rt-secret" {
		t.Error("JSON round trip lost the tokens")
	}
	if !(Token{Expiry: time.Unix(100, 0)}).Expired(time.Unix(80, 0), 30*time.Second) || (Token{}).Expired(time.Now(), time.Hour) {
		t.Error("Expired")
	}
}

func TestParseBearer(t *testing.T) {
	h := http.Header{}
	h.Add("WWW-Authenticate", `Basic realm="x"`)
	h.Add("WWW-Authenticate", `Bearer error="insufficient_scope", scope="repo read:org", resource_metadata="https://x/y?a=\"b\""`)
	code, scopes := ParseBearerError(h)
	if code != "insufficient_scope" || strings.Join(scopes, ",") != "repo,read:org" {
		t.Errorf("ParseBearerError = %q %v", code, scopes)
	}
	ch := parseBearer(h.Values("WWW-Authenticate"))
	if ch.resourceMetadata != `https://x/y?a="b"` {
		t.Errorf("resource_metadata %q", ch.resourceMetadata)
	}
	h2 := http.Header{"Www-Authenticate": {`Bearer error="<script>"`}}
	if code, _ := ParseBearerError(h2); code != "" {
		t.Errorf("invalid error code kept: %q", code)
	}
	_ = io.Discard
}

// ADR 0023: a server that publishes no protected resource metadata is told
// apart from other failures, so an administrator can name its authorization
// server by hand; and no empty resource parameter is sent.
func TestNoResourceMetadataIsNamed(t *testing.T) {
	f := newFake(t)
	f.challenge = `Bearer realm="x"`     // no resource_metadata
	f.prmPath = "/not/a/well-known/path" // the well-known locations answer 404
	_, err := testClient().Discover(context.Background(), f.url("/mcp"), "http", "")
	if !errors.Is(err, ErrNoResourceMetadata) {
		t.Fatalf("Discover = %v, want ErrNoResourceMetadata", err)
	}
	sm, err := testClient().ServerMetadataFor(context.Background(), f.srv.URL)
	if err != nil || sm.TokenEndpoint != f.url("/token") {
		t.Fatalf("ServerMetadataFor = %+v, %v", sm, err)
	}
	// A PRM that is there but broken is not "missing".
	g := newFake(t)
	g.challenge = `Bearer realm="x"`
	g.prmResource = "https://other.example/mcp"
	if _, err := testClient().Discover(context.Background(), g.url("/mcp"), "http", ""); err == nil || errors.Is(err, ErrNoResourceMetadata) {
		t.Errorf("a PRM for another resource: %v", err)
	}
	for _, bad := range []string{"ftp://x.example/a", "https://u:p@x.example/a", "not a url", ""} {
		if CheckEndpoint(bad) == nil {
			t.Errorf("CheckEndpoint(%q) passed", bad)
		}
	}
}

func TestEmptyResourceIsNotSent(t *testing.T) {
	f := newFake(t)
	e := endpointOf(f, "none")
	e.Resource = ""
	raw, err := AuthorizeURL(e, "https://privo.example/connect/callback", "st", Challenge("v"), nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if _, ok := u.Query()["resource"]; ok {
		t.Errorf("the authorization URL carries an empty resource: %s", raw)
	}
	f.tokenAnswers = []tokenAnswer{{200, `{"access_token":"a","token_type":"Bearer","refresh_token":"r","expires_in":60}`}}
	if _, err := testClient().Exchange(context.Background(), e, "code", "verifier", "https://privo.example/connect/callback", time.Now()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tokenRequests[0]["resource"]; ok {
		t.Errorf("the token request carries an empty resource: %v", f.tokenRequests[0])
	}
}
