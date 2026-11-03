package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"privo.club/privo/internal/grants"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
)

// Tests for invites and connecting upstream accounts (ADR 0017).

// fakeAS is an upstream authorization server.
type fakeAS struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	codes    map[string]asGrant
	revoked  []string
	failNext bool
	// invalidClient makes the token endpoint refuse the client.
	invalidClient bool
	registered    int
	tokens        int
}

type asGrant struct{ challenge, redirect, resource, subject string }

func newFakeAS(t *testing.T) *fakeAS {
	a := &fakeAS{t: t, codes: map[string]asGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		a.mu.Lock()
		defer a.mu.Unlock()
		g, ok := a.codes[r.PostForm.Get("code")]
		delete(a.codes, r.PostForm.Get("code"))
		w.Header().Set("Content-Type", "application/json")
		if a.invalidClient {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		if a.failNext || !ok || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("client_id") != "client-1" ||
			r.PostForm.Get("redirect_uri") != g.redirect || r.PostForm.Get("resource") != g.resource ||
			upstreamauth.Challenge(r.PostForm.Get("code_verifier")) != g.challenge {
			a.failNext = false
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		a.tokens++
		_, _ = fmt.Fprintf(w, `{"access_token":"AT-%s-%d","token_type":"Bearer","refresh_token":"RT-%s-%d","expires_in":3600,"scope":"read write"}`, g.subject, a.tokens, g.subject, a.tokens)
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		a.registered++
		n := a.registered
		a.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"client_id":"re-registered-%d"}`, n)
	})
	mux.HandleFunc("POST /revoke", func(_ http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		a.mu.Lock()
		a.revoked = append(a.revoked, r.PostForm.Get("token"))
		a.mu.Unlock()
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)
	return a
}

// authorize checks where Privo sent the browser and returns what the
// authorization server would send back after subject signed in.
func (a *fakeAS) authorize(location, subject string) (code, state string) {
	a.t.Helper()
	u, err := url.Parse(location)
	if err != nil || u.Scheme+"://"+u.Host != a.srv.URL || u.Path != "/authorize" {
		a.t.Fatalf("browser sent to %s", location)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != "client-1" || q.Get("code_challenge_method") != "S256" ||
		q.Get("resource") != "https://linear.example/mcp" || q.Get("scope") != "read write" ||
		q.Get("redirect_uri") != "https://app.example.com/connect/callback" || q.Get("state") == "" {
		a.t.Fatalf("authorization request %v", q)
	}
	code = "c-" + randHex(8)
	a.mu.Lock()
	a.codes[code] = asGrant{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), resource: q.Get("resource"), subject: subject}
	a.mu.Unlock()
	return code, q.Get("state")
}

type fakeUpstreams struct {
	mu     sync.Mutex
	kicks  []string
	closes []string
}

func (f *fakeUpstreams) Kick(id string) { f.mu.Lock(); f.kicks = append(f.kicks, id); f.mu.Unlock() }
func (f *fakeUpstreams) ClosePerson(sid, uid string) {
	f.mu.Lock()
	f.closes = append(f.closes, sid+"/"+uid)
	f.mu.Unlock()
}

type connParts struct {
	as    *fakeAS
	ups   *fakeUpstreams
	svc   *grants.Service
	oauth *upstreamauth.Client
	srv   store.Server
}

func newConnParts(t *testing.T, st *store.Store, org store.OrgID, kr *keyring.Keyring, pub *url.URL, clk *clock, logger *slog.Logger) *connParts {
	t.Helper()
	as := newFakeAS(t)
	oauth := &upstreamauth.Client{HTTP: as.srv.Client(), Timeout: 2 * time.Second}
	srv := store.Server{OrgID: org, ID: ids.New(), Alias: "linear", Transport: "http", Enabled: true, CreatedAt: clk.now(),
		Config: store.ServerConfig{URL: "https://linear.example/mcp", Auth: store.AuthOAuth, OAuth: &store.ServerOAuth{
			Issuer: as.srv.URL, AuthorizationEndpoint: as.srv.URL + "/authorize", TokenEndpoint: as.srv.URL + "/token",
			RevocationEndpoint: as.srv.URL + "/revoke", Resource: "https://linear.example/mcp", Scopes: []string{"read", "write"},
			ClientID: "client-1", Registration: store.RegistrationAdmin, IssParameter: true}}}
	if err := st.CreateServer(context.Background(), srv, nil); err != nil {
		t.Fatal(err)
	}
	svc := &grants.Service{Store: st, Keys: kr, Auth: oauth, PublicURL: pub, Now: clk.now, Logger: logger}
	return &connParts{as: as, ups: &fakeUpstreams{}, svc: svc, oauth: oauth, srv: srv}
}

func newConnEnv(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, true)
}

// signedIn returns a browser signed in as a member with role.
func (e *env) signedIn(u ghUser, person, role string) (*browser, store.User) {
	e.t.Helper()
	user := e.member(u, person, role)
	b := e.browser()
	if res := e.signIn(b, u); res.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("sign in: %d %s", res.StatusCode, res.Body)
	}
	return b, user
}

// connect runs the browser leg and returns the callback's response.
func (e *env) connect(b *browser, subject string) page {
	e.t.Helper()
	p := b.get("/connect/linear")
	if p.StatusCode != http.StatusOK {
		e.t.Fatalf("connect page: %d %s", p.StatusCode, p.Body)
	}
	start := b.post("/connect/linear/start", url.Values{"csrf": {csrfOf(e.t, p.Body)}})
	if start.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("connect start: %d %s", start.StatusCode, start.Body)
	}
	code, state := e.as.authorize(start.Header.Get("Location"), subject)
	return b.get("/connect/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state) + "&iss=" + url.QueryEscape(e.as.srv.URL))
}

func (e *env) grant(userID string) (store.Grant, error) {
	return e.st.Grant(context.Background(), e.org, e.osrv.ID, userID)
}

var ada = ghUser{ID: 2001, Login: "ada", Name: "Ada"}

func TestConnectNeedsASession(t *testing.T) {
	e := newConnEnv(t)
	b := e.browser()
	p := b.get("/connect/linear")
	if p.StatusCode != http.StatusSeeOther || p.Header.Get("Location") != "/admin/signin?next=%2Fconnect%2Flinear" {
		t.Fatalf("anonymous connect page: %d %q", p.StatusCode, p.Header.Get("Location"))
	}
	if p := b.post("/connect/linear/start", url.Values{"csrf": {"x"}}); p.StatusCode != http.StatusSeeOther || p.Header.Get("Location") != "/admin/signin" {
		t.Fatalf("anonymous start: %d %q", p.StatusCode, p.Header.Get("Location"))
	}
	if p := b.get("/admin/connections"); p.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous connections page: %d", p.StatusCode)
	}
}

func TestSignInReturnsToTheConnectPage(t *testing.T) {
	e := newConnEnv(t)
	e.member(ada, "ada", store.RoleMember)
	b := e.browser()
	signin := b.get("/admin/signin?next=%2Fconnect%2Flinear")
	if !strings.Contains(signin.Body, `name="return_to" value="/connect/linear"`) {
		t.Fatalf("sign-in page does not carry the way back: %s", signin.Body)
	}
	start := b.post("/auth/github/start", url.Values{"return_to": {"/connect/linear"}})
	code, state := e.gh.authorize(start.Header.Get("Location"), ada)
	cb := b.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
	if cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/connect/linear" {
		t.Fatalf("after sign-in: %d %q", cb.StatusCode, cb.Header.Get("Location"))
	}
	// Anything but a connect path is ignored: no open redirect.
	for _, bad := range []string{"https://evil.example/", "//evil.example", "/connect/linear/../../x", "/admin/approvals", "/connect/LINEAR"} {
		e2 := newConnEnv(t)
		e2.member(ada, "ada", store.RoleMember)
		b2 := e2.browser()
		start := b2.post("/auth/github/start", url.Values{"return_to": {bad}})
		code, state := e2.gh.authorize(start.Header.Get("Location"), ada)
		cb := b2.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
		if cb.Header.Get("Location") != "/admin/start" { // a member's landing page
			t.Errorf("return_to %q led to %q", bad, cb.Header.Get("Location"))
		}
	}
}

func TestConnectPageNamesUserServerHostAndScopes(t *testing.T) {
	e := newConnEnv(t)
	b, _ := e.signedIn(ada, "ada", store.RoleMember)
	p := b.get("/connect/linear")
	host := strings.TrimPrefix(e.as.srv.URL, "http://")
	for _, want := range []string{"Connect your linear account", host, "read write", "Ada"} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("connect page lacks %q", want)
		}
	}
	// Unknown servers, servers without OAuth and roles that do not use tools.
	if p := b.get("/connect/nosuch"); p.StatusCode != http.StatusNotFound {
		t.Errorf("unknown server: %d", p.StatusCode)
	}
	_ = e.st.CreateServer(context.Background(), store.Server{OrgID: e.org, ID: ids.New(), Alias: "plain", Transport: "http", Enabled: true, CreatedAt: e.clock.now(),
		Config: store.ServerConfig{URL: "https://plain.example/mcp"}}, nil)
	if p := b.get("/connect/plain"); p.StatusCode != http.StatusNotFound {
		t.Errorf("server without OAuth: %d", p.StatusCode)
	}
	ab, _ := e.signedIn(ghUser{ID: 3001, Login: "appr"}, "appr", store.RoleApprover)
	if p := ab.get("/connect/linear"); p.StatusCode != http.StatusForbidden {
		t.Errorf("approver: %d", p.StatusCode)
	}
}

func TestConnectCSPAllowsOnlyThatAuthServer(t *testing.T) {
	e := newConnEnv(t)
	b, _ := e.signedIn(ada, "ada", store.RoleMember)
	p := b.get("/connect/linear")
	csp := p.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "form-action 'self' "+e.as.srv.URL+";") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("connect page CSP %q", csp)
	}
	if other := b.get("/admin/connections").Header.Get("Content-Security-Policy"); strings.Contains(other, e.as.srv.URL) {
		t.Fatalf("another page allows the authorization server: %q", other)
	}
}

func TestConnectStartNeedsCSRFAndSameOrigin(t *testing.T) {
	e := newConnEnv(t)
	b, _ := e.signedIn(ada, "ada", store.RoleMember)
	tok := csrfOf(t, b.get("/connect/linear").Body)
	if p := b.post("/connect/linear/start", url.Values{"csrf": {"wrong"}}); p.StatusCode != http.StatusForbidden {
		t.Errorf("wrong CSRF: %d", p.StatusCode)
	}
	if p := b.do(http.MethodPost, "/connect/linear/start", url.Values{"csrf": {tok}}, map[string]string{"Origin": "https://evil.example"}); p.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin: %d", p.StatusCode)
	}
	if p := b.get("/connect/linear/start"); p.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET start: %d", p.StatusCode)
	}
}

func TestConnectSuccessStoresAnEncryptedGrant(t *testing.T) {
	e := newConnEnv(t)
	b, u := e.signedIn(ada, "ada", store.RoleMember)
	cb := e.connect(b, "ada")
	if cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/admin/connections?result=connected" {
		t.Fatalf("callback: %d %q %s", cb.StatusCode, cb.Header.Get("Location"), cb.Body)
	}
	g, err := e.grant(u.ID)
	if err != nil || g.Scopes != "read write" || g.Catalog {
		t.Fatalf("grant %+v %v (a member's grant is not the catalog)", g, err)
	}
	if strings.Contains(string(g.Credential.Ciphertext), "AT-ada") {
		t.Fatal("token stored in the clear")
	}
	at, err := e.svc.AccessToken(context.Background(), e.org, e.osrv, u.ID)
	if err != nil || !strings.HasPrefix(at, "AT-ada-") {
		t.Fatalf("token %q %v", at, err)
	}
	if len(e.ups.kicks) != 0 || len(e.ups.closes) != 1 {
		t.Errorf("kicks %v closes %v", e.ups.kicks, e.ups.closes)
	}
	// An owner's grant becomes the catalog grant and kicks the server.
	ob, owner := e.signedIn(ghUser{ID: 1, Login: "boss"}, "boss", store.RoleOwner)
	e.connect(ob, "boss")
	if g, _ := e.grant(owner.ID); !g.Catalog || len(e.ups.kicks) != 1 || e.ups.kicks[0] != e.osrv.ID {
		t.Fatalf("owner grant catalog=%v kicks=%v", g.Catalog, e.ups.kicks)
	}
	acts := auditActionsOf(t, e)
	if !strings.Contains(acts, "upstream.connect:linear") {
		t.Errorf("connect not audited: %s", acts)
	}
	if mine := myConnectionsOf(t, b); len(mine) != 1 || !mine[0].Connected || mine[0].Alias != "linear" {
		t.Errorf("connections: %+v", mine)
	}
}

func myConnectionsOf(t *testing.T, b *browser) []myConnection {
	t.Helper()
	res := b.call(http.MethodGet, "/admin/api/me/connections", b.csrf(), "", nil)
	var out struct {
		Connections []myConnection `json:"connections"`
		CanConnect  bool           `json:"can_connect"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal([]byte(res.Body), &out) != nil || !out.CanConnect {
		t.Fatalf("my connections: %d %s", res.StatusCode, res.Body)
	}
	return out.Connections
}

func auditActionsOf(t *testing.T, e *env) string {
	t.Helper()
	time.Sleep(50 * time.Millisecond) // the audit writer is asynchronous
	evs, err := e.st.RecentAdminEvents(context.Background(), e.org, 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ev := range evs {
		out = append(out, ev.Action)
	}
	return strings.Join(out, " ")
}

func TestConnectStateSingleUseExpiresAndBoundToBrowser(t *testing.T) {
	e := newConnEnv(t)
	b, u := e.signedIn(ada, "ada", store.RoleMember)
	p := b.get("/connect/linear")
	start := b.post("/connect/linear/start", url.Values{"csrf": {csrfOf(t, p.Body)}})
	code, state := e.as.authorize(start.Header.Get("Location"), "ada")
	cbURL := "/connect/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state) + "&iss=" + url.QueryEscape(e.as.srv.URL)

	// Another browser (no state cookie, even with ada's session cookie) neither
	// completes nor uses up the state.
	other := e.browser()
	other.cookies[e.srv.sessionCookie] = b.cookies[e.srv.sessionCookie]
	if p := other.get(cbURL); p.StatusCode != http.StatusBadRequest {
		t.Fatalf("other browser: %d", p.StatusCode)
	}
	if p := b.get(cbURL); p.StatusCode != http.StatusSeeOther {
		t.Fatalf("the right browser after the attempt: %d %s", p.StatusCode, p.Body)
	}
	// Replay.
	b.cookies[e.srv.stateCookie] = &http.Cookie{Name: e.srv.stateCookie, Value: strings.Repeat("A", 43)}
	if p := b.get(cbURL); p.StatusCode != http.StatusBadRequest {
		t.Fatalf("replay: %d", p.StatusCode)
	}
	// Expiry.
	_ = e.st.DeleteGrant(context.Background(), e.org, e.osrv.ID, u.ID, e.event("reset"))
	p = b.get("/connect/linear")
	start = b.post("/connect/linear/start", url.Values{"csrf": {csrfOf(t, p.Body)}})
	code, state = e.as.authorize(start.Header.Get("Location"), "ada")
	e.clock.advance(StateTTL + time.Second)
	if p := b.get("/connect/callback?code=" + code + "&state=" + state + "&iss=" + url.QueryEscape(e.as.srv.URL)); p.StatusCode != http.StatusBadRequest && p.StatusCode != http.StatusForbidden {
		t.Fatalf("expired: %d", p.StatusCode)
	}
	if _, err := e.grant(u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("an expired connection stored a grant")
	}
}

func TestConnectCallbackNeedsTheSameSession(t *testing.T) {
	e := newConnEnv(t)
	b, u := e.signedIn(ada, "ada", store.RoleMember)
	p := b.get("/connect/linear")
	start := b.post("/connect/linear/start", url.Values{"csrf": {csrfOf(t, p.Body)}})
	code, state := e.as.authorize(start.Header.Get("Location"), "attacker")
	// The browser now carries another person's session (someone else signed in
	// on it, or a session cookie was planted) when the callback arrives: the
	// account must not land on that person.
	bb, _ := e.signedIn(ghUser{ID: 4001, Login: "bob"}, "bob", store.RoleMember)
	b.cookies[e.srv.sessionCookie] = bb.cookies[e.srv.sessionCookie]
	cb := b.get("/connect/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state) + "&iss=" + url.QueryEscape(e.as.srv.URL))
	if cb.StatusCode != http.StatusForbidden {
		t.Fatalf("callback in another session: %d %s", cb.StatusCode, cb.Body)
	}
	bob, _ := e.st.UserByPerson(context.Background(), e.org, "bob")
	for _, id := range []string{u.ID, bob.ID} {
		if _, err := e.grant(id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a grant was stored for %s", id)
		}
	}
	if e.as.tokens != 0 {
		t.Fatal("the code was exchanged")
	}
}

func TestConnectIssMismatchRefused(t *testing.T) {
	e := newConnEnv(t)
	b, u := e.signedIn(ada, "ada", store.RoleMember)
	for _, extra := range []string{"other", ""} {
		p := b.get("/connect/linear")
		start := b.post("/connect/linear/start", url.Values{"csrf": {csrfOf(t, p.Body)}})
		code, state := e.as.authorize(start.Header.Get("Location"), "ada")
		target := "/connect/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
		if extra != "" {
			target += "&iss=" + url.QueryEscape("https://evil.example")
		} // else: missing although the server promised to send it
		if cb := b.get(target); cb.StatusCode != http.StatusBadRequest {
			t.Fatalf("iss %q: %d", extra, cb.StatusCode)
		}
	}
	if _, err := e.grant(u.ID); !errors.Is(err, store.ErrNotFound) || e.as.tokens != 0 {
		t.Fatal("a mixed-up answer was exchanged or stored")
	}
}

func TestConnectErrorStoresNothing(t *testing.T) {
	e := newConnEnv(t)
	b, u := e.signedIn(ada, "ada", store.RoleMember)
	// The authorization server says no.
	p := b.get("/connect/linear")
	start := b.post("/connect/linear/start", url.Values{"csrf": {csrfOf(t, p.Body)}})
	_, state := e.as.authorize(start.Header.Get("Location"), "ada")
	cb := b.get("/connect/callback?error=access_denied&error_description=%3Cscript%3E&state=" + url.QueryEscape(state) + "&iss=" + url.QueryEscape(e.as.srv.URL))
	if cb.StatusCode != http.StatusBadRequest || strings.Contains(cb.Body, "<script>") {
		t.Fatalf("denied at the AS: %d", cb.StatusCode)
	}
	// The exchange fails.
	e.as.failNext = true
	if cb := e.connect(b, "ada"); cb.StatusCode != http.StatusBadRequest {
		t.Fatalf("failed exchange: %d", cb.StatusCode)
	}
	if _, err := e.grant(u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a failed connection stored a grant")
	}
}

func TestConnectionsPageListsOwnGrantsOnly(t *testing.T) {
	e := newConnEnv(t)
	ob, _ := e.signedIn(ghUser{ID: 1, Login: "boss"}, "boss", store.RoleOwner)
	e.connect(ob, "boss")
	b, _ := e.signedIn(ada, "ada", store.RoleMember)
	if mine := myConnectionsOf(t, b); len(mine) != 1 || mine[0].Connected || mine[0].Issuer == "" {
		t.Fatalf("ada sees someone else's connection: %+v", mine)
	}
	if mine := myConnectionsOf(t, ob); len(mine) != 1 || !mine[0].Connected || mine[0].ConnectedAt == "" {
		t.Fatalf("the owner does not see their own connection: %+v", mine)
	}
}

func TestDisconnectRevokesAndDeletes(t *testing.T) {
	e := newConnEnv(t)
	b, u := e.signedIn(ada, "ada", store.RoleMember)
	e.connect(b, "ada")
	tok := b.csrf()
	if p := b.call(http.MethodDelete, "/admin/api/me/connections/linear", "wrong", "", nil); p.StatusCode != http.StatusForbidden {
		t.Fatalf("disconnect without CSRF: %d", p.StatusCode)
	}
	if p := b.call(http.MethodDelete, "/admin/api/me/connections/linear", tok, "", map[string]string{"Origin": "https://evil.example"}); p.StatusCode != http.StatusForbidden {
		t.Fatalf("disconnect from another origin: %d", p.StatusCode)
	}
	if _, err := e.grant(u.ID); err != nil {
		t.Fatal("a refused disconnect deleted the grant")
	}
	p := b.call(http.MethodDelete, "/admin/api/me/connections/linear", tok, "", nil)
	if p.StatusCode != http.StatusOK || !strings.Contains(p.Body, `"disconnected":"linear"`) {
		t.Fatalf("disconnect: %d %s", p.StatusCode, p.Body)
	}
	if _, err := e.grant(u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("grant kept")
	}
	if len(e.as.revoked) != 1 || !strings.HasPrefix(e.as.revoked[0], "RT-ada") {
		t.Fatalf("revoked %v", e.as.revoked)
	}
	if p := b.call(http.MethodDelete, "/admin/api/me/connections/linear", tok, "", nil); p.StatusCode != http.StatusNotFound {
		t.Fatalf("second disconnect: %d", p.StatusCode)
	}
	if !strings.Contains(auditActionsOf(t, e), "upstream.disconnect:linear") {
		t.Error("disconnect not audited")
	}
}

func (e *env) invite(person, role string) string {
	e.t.Helper()
	plain, hash, err := NewInviteToken()
	if err != nil {
		e.t.Fatal(err)
	}
	err = e.st.CreateInvite(context.Background(), e.org, store.NewInvite{ID: ids.New(), SecretHash: hash, Person: person, Role: role, CreatedBy: "boss",
		Now: e.clock.now(), ExpiresAt: e.clock.now().Add(72 * time.Hour)}, e.event("invite.create"))
	if err != nil {
		e.t.Fatal(err)
	}
	return plain
}

func (e *env) claimInvite(b *browser, token string, u ghUser) page {
	e.t.Helper()
	if p := b.get("/admin/invite/" + token); p.StatusCode != http.StatusOK {
		e.t.Fatalf("invite page: %d", p.StatusCode)
	}
	start := b.post("/auth/github/start", url.Values{"invite_token": {token}})
	if start.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("invite start: %d %s", start.StatusCode, start.Body)
	}
	code, state := e.gh.authorize(start.Header.Get("Location"), u)
	return b.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
}

func TestInviteClaimCreatesMemberWithRole(t *testing.T) {
	e := newConnEnv(t)
	token := e.invite("ada", store.RoleMember)
	b := e.browser()
	page := b.get("/admin/invite/" + token)
	if !strings.Contains(page.Body, "<strong>ada</strong>") || !strings.Contains(page.Body, "<strong>member</strong>") {
		t.Fatalf("invite page: %s", page.Body)
	}
	cb := e.claimInvite(b, token, ghUser{ID: 7007, Login: "Ada-GH"})
	if cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/admin/start" {
		t.Fatalf("claim: %d %q %s", cb.StatusCode, cb.Header.Get("Location"), cb.Body)
	}
	u, err := e.st.UserByIdentity(context.Background(), e.org, store.ProviderGitHub, "7007")
	if err != nil || u.Person != "ada" || u.Role != store.RoleMember {
		t.Fatalf("invited user %+v %v", u, err)
	}
	if b.cookie(e.srv.sessionCookie) == nil {
		t.Fatal("no session after claiming")
	}
	// Used up.
	if p := e.browser().get("/admin/invite/" + token); p.StatusCode != http.StatusNotFound {
		t.Fatalf("used invite: %d", p.StatusCode)
	}
	for _, bad := range []string{"privo_invite_short", "privo_setup_" + strings.Repeat("A", 43), "privo_invite_" + strings.Repeat("A", 43)} {
		if p := e.browser().get("/admin/invite/" + bad); p.StatusCode != http.StatusNotFound {
			t.Errorf("bad invite %q: %d", bad, p.StatusCode)
		}
	}
	if !strings.Contains(auditActionsOf(t, e), "invite.claim") {
		t.Error("claim not audited")
	}
}

func TestInviteRefusedForExistingMember(t *testing.T) {
	e := newConnEnv(t)
	e.member(ada, "ada", store.RoleMember)
	token := e.invite("bob", store.RoleMember)
	cb := e.claimInvite(e.browser(), token, ada) // ada's GitHub account is already a member
	if cb.StatusCode != http.StatusConflict {
		t.Fatalf("claim by a member: %d", cb.StatusCode)
	}
	if p := e.browser().get("/admin/invite/" + token); p.StatusCode != http.StatusOK {
		t.Fatalf("the refused claim used up the invite: %d", p.StatusCode)
	}
}

func TestClientMetadataDocumentIsServed(t *testing.T) {
	e := newConnEnv(t)
	p := e.browser().get("/oauth/client-metadata.json")
	if p.StatusCode != http.StatusOK || p.Header.Get("Content-Type") != "application/json" ||
		!strings.Contains(p.Body, `"client_id":"https://app.example.com/oauth/client-metadata.json"`) ||
		!strings.Contains(p.Body, `"redirect_uris":["https://app.example.com/connect/callback"]`) {
		t.Fatalf("client metadata: %d %s", p.StatusCode, p.Body)
	}
	// Without the connections wiring there is no such route.
	if p := newEnv(t).browser().get("/oauth/client-metadata.json"); p.StatusCode != http.StatusNotFound {
		t.Fatalf("served without upstream OAuth: %d", p.StatusCode)
	}
}

func TestWhereEachRoleLands(t *testing.T) {
	e := newConnEnv(t)
	ob, _ := e.signedIn(ghUser{ID: 1, Login: "boss"}, "boss", store.RoleOwner)
	if p := ob.get("/admin/"); p.Header.Get("Location") != "/admin/servers" {
		t.Errorf("owner lands on %q", p.Header.Get("Location"))
	}
	audb, _ := e.signedIn(ghUser{ID: 3002, Login: "aud"}, "aud", store.RoleAuditor)
	if p := audb.get("/admin/"); p.Header.Get("Location") != "/admin/activity" {
		t.Errorf("auditor lands on %q", p.Header.Get("Location"))
	}
	b, _ := e.signedIn(ada, "ada", store.RoleMember)
	if p := b.get("/admin/"); p.Header.Get("Location") != "/admin/start" {
		t.Errorf("member lands on %q", p.Header.Get("Location"))
	}
	ab, _ := e.signedIn(ghUser{ID: 3001, Login: "appr"}, "appr", store.RoleApprover)
	if p := ab.get("/admin/"); p.Header.Get("Location") != "/admin/approvals" {
		t.Errorf("approver lands on %q", p.Header.Get("Location"))
	}
}

func TestDynamicRegistrationRetriedWhenRejected(t *testing.T) {
	e := newConnEnv(t)
	o := *e.osrv.Config.OAuth
	o.Registration, o.RegistrationEndpoint = store.RegistrationDynamic, e.as.srv.URL+"/register"
	if err := e.st.UpdateServerOAuth(context.Background(), e.org, e.osrv.ID, o, nil); err != nil {
		t.Fatal(err)
	}
	b, u := e.signedIn(ada, "ada", store.RoleMember)
	e.as.invalidClient = true
	cb := e.connect(b, "ada")
	if cb.StatusCode != http.StatusConflict || !strings.Contains(cb.Body, "register itself again") {
		t.Fatalf("callback after invalid_client: %d %s", cb.StatusCode, cb.Body)
	}
	srv, _ := e.st.ServerByAlias(context.Background(), e.org, "linear")
	if e.as.registered != 1 || srv.Config.OAuth.ClientID != "re-registered-1" {
		t.Fatalf("registered %d times, client id %q", e.as.registered, srv.Config.OAuth.ClientID)
	}
	if _, err := e.grant(u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a grant was stored")
	}
	// A server the admin registered by hand is not re-registered.
	e2 := newConnEnv(t)
	b2, _ := e2.signedIn(ada, "ada", store.RoleMember)
	e2.as.invalidClient = true
	if cb := e2.connect(b2, "ada"); cb.StatusCode != http.StatusBadRequest || e2.as.registered != 0 {
		t.Fatalf("admin-registered client: %d, registered %d", cb.StatusCode, e2.as.registered)
	}
}

// F26 review R-01: a person sees and connects only the servers their teams
// reach; a server outside them answers like one that does not exist, and a
// connection started before the server left their teams stores nothing.
// Admins connect any server: the first one provides its tool list.
func TestConnectFollowsTeams(t *testing.T) {
	ctx := context.Background()
	e := newConnEnv(t)
	b, u := e.signedIn(ada, "ada", store.RoleMember)
	ob, _ := e.signedIn(ghUser{ID: 1, Login: "boss"}, "boss", store.RoleOwner)
	if err := e.st.CreateTeam(ctx, e.org, store.Team{Slug: "designers", Name: "Designers", CreatedAt: e.clock.now()}, e.event("team.create:designers")); err != nil {
		t.Fatal(err)
	}

	// The page is open and the connection started while linear is everyone's.
	p := b.get("/connect/linear")
	start := b.post("/connect/linear/start", url.Values{"csrf": {csrfOf(t, p.Body)}})
	if start.StatusCode != http.StatusSeeOther {
		t.Fatalf("start: %d %s", start.StatusCode, start.Body)
	}
	if err := e.st.SetServerTeams(ctx, e.org, e.osrv.ID, []string{"designers"}, e.event("server.teams=designers")); err != nil {
		t.Fatal(err)
	}
	code, state := e.as.authorize(start.Header.Get("Location"), "ada")
	if cb := b.get("/connect/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state) + "&iss=" + url.QueryEscape(e.as.srv.URL)); cb.StatusCode == http.StatusSeeOther {
		t.Fatalf("a connection finished for a server outside her teams: %q", cb.Header.Get("Location"))
	}
	if _, err := e.grant(u.ID); err == nil {
		t.Fatal("a grant was stored for a server outside her teams")
	}

	// Now outside her teams: not listed, and the page answers like an unknown server.
	if mine := myConnectionsOf(t, b); len(mine) != 0 {
		t.Fatalf("ada lists %+v", mine)
	}
	hidden, unknown := b.get("/connect/linear"), b.get("/connect/nosuch")
	if hidden.StatusCode != http.StatusNotFound || hidden.StatusCode != unknown.StatusCode || strings.Contains(hidden.Body, "linear") {
		t.Fatalf("outside her teams: %d, unknown: %d\n%s", hidden.StatusCode, unknown.StatusCode, hidden.Body)
	}
	if res := b.post("/connect/linear/start", url.Values{"csrf": {b.csrf()}}); res.StatusCode != http.StatusNotFound {
		t.Fatalf("start outside her teams: %d", res.StatusCode)
	}

	// The owner, in no team, still connects it; Ada joins and can again.
	if cb := e.connect(ob, "boss"); cb.StatusCode != http.StatusSeeOther {
		t.Fatalf("owner connect: %d %s", cb.StatusCode, cb.Body)
	}
	if err := e.st.AddTeamMember(ctx, e.org, "designers", u.ID, e.clock.now(), e.event("team.member.add:designers:ada")); err != nil {
		t.Fatal(err)
	}
	if mine := myConnectionsOf(t, b); len(mine) != 1 || mine[0].Alias != "linear" {
		t.Fatalf("ada in designers lists %+v", mine)
	}
	if cb := e.connect(b, "ada"); cb.StatusCode != http.StatusSeeOther {
		t.Fatalf("ada in designers: %d %s", cb.StatusCode, cb.Body)
	}
}
