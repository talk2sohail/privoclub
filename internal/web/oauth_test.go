package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"privo.club/privo/internal/oauthserver"
	"privo.club/privo/internal/store"
)

// Tests for the authorization endpoint and the consent page (F06, ADR 0022
// points 6 and 7). The server behind them is real (internal/oauthserver).

const oauthCB = "http://localhost:3118/callback"

type oauthEnv struct {
	*env
	as       *oauthserver.Server
	clients  *oauthserver.Clients
	clientID string // a known client on the person's own computer
	webID    string // a known client with an https redirect address
	docs     *httptest.Server
}

func newOAuthEnv(t *testing.T) *oauthEnv {
	t.Helper()
	e := newConnEnv(t)
	o := &oauthEnv{env: e}
	docs := map[string]string{}
	o.docs = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := docs[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(o.docs.Close)
	put := func(path, name string, redirects ...string) string {
		id := o.docs.URL + path
		b, _ := json.Marshal(map[string]any{"client_id": id, "client_name": name, "redirect_uris": redirects, "token_endpoint_auth_method": "none"})
		docs[path] = string(b)
		return id
	}
	o.clientID = put("/code.json", "Claude Code", oauthCB)
	o.webID = put("/web.json", "Claude", "https://claude.example/api/mcp/auth_callback")
	other := put("/other.json", "Some <b>Tool</b>", "https://tool.example/cb")
	doc := o.docs.Client()
	doc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	o.clients = &oauthserver.Clients{Org: e.org, Store: e.st, Policy: oauthserver.PolicyKnown, HTTP: doc, Now: e.clock.now, AllowHTTP: true,
		Known: []oauthserver.KnownClient{{URL: o.clientID, Name: "Claude Code"}, {URL: o.webID, Name: "Claude"}}}
	_ = other
	as, err := oauthserver.New(context.Background(), oauthserver.Options{Org: e.org, Store: e.st, Keys: e.keys, Audit: e.audit, Clients: o.clients,
		Issuer: e.pub, Now: e.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	o.as = as
	opts := e.opts
	opts.AuthServer = as
	e.srv = New(opts)
	mux := http.NewServeMux()
	e.srv.Routes(mux)
	as.Routes(mux)
	e.handler = mux
	return o
}

func oauthPKCE() (verifier, challenge string) {
	verifier = strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// request is a valid authorization request of the loopback client.
func (o *oauthEnv) request(edit ...func(url.Values)) string {
	_, challenge := oauthPKCE()
	q := url.Values{"response_type": {"code"}, "client_id": {o.clientID}, "redirect_uri": {oauthCB}, "scope": {"mcp"},
		"state": {"client-state-123"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "resource": {o.pub + "/mcp"}}
	for _, f := range edit {
		f(q)
	}
	return "/oauth/authorize?" + q.Encode()
}

func hidden(t *testing.T, body, name string) string {
	t.Helper()
	marker := `name="` + name + `" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no %s field in the page:\n%s", name, body)
	}
	value, _, found := strings.Cut(body[i+len(marker):], `"`)
	if !found {
		t.Fatalf("the %s field has no end", name)
	}
	return value
}

func (o *oauthEnv) grants() (n int) {
	o.t.Helper()
	if err := o.db().QueryRow(`SELECT (SELECT COUNT(*) FROM oauth_grants) + (SELECT COUNT(*) FROM oauth_tokens)`).Scan(&n); err != nil {
		o.t.Fatal(err)
	}
	return n
}

// The whole way for someone who is not signed in: nothing is written until
// they press Allow, and the request waits in a sealed cookie meanwhile.
func TestAuthorizeRequestSurvivesSignInInASealedCookie(t *testing.T) {
	o := newOAuthEnv(t)
	o.member(ada, "ada", store.RoleMember)
	o.audit.Close()
	db := o.db()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	version := dataVersion(t, conn)

	b := o.browser()
	res := b.get(o.request())
	if res.StatusCode != http.StatusSeeOther || location(res) != "/admin/signin?next=%2Foauth%2Fauthorize" {
		t.Fatalf("without a session: %d %q", res.StatusCode, location(res))
	}
	cookie := b.cookie(o.srv.authzCookie)
	if cookie == nil || strings.Contains(cookie.Value, "client-state-123") || strings.Contains(cookie.Value, "localhost") {
		t.Fatalf("the request cookie: %v", cookie)
	}
	page := b.get(location(res))
	if page.StatusCode != http.StatusOK || hidden(t, page.Body, "return_to") != "/oauth/authorize" {
		t.Fatalf("the sign-in page: %d\n%s", page.StatusCode, page.Body)
	}
	start := b.post("/auth/github/start", url.Values{"return_to": {"/oauth/authorize"}})
	if got := dataVersion(t, conn); got != version {
		t.Errorf("the database was written before anyone signed in (data_version %d, was %d)", got, version)
	}
	code, state := o.gh.authorize(location(start), ada)
	back := b.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
	if back.StatusCode != http.StatusSeeOther || location(back) != "/oauth/authorize" {
		t.Fatalf("after signing in: %d %q", back.StatusCode, location(back))
	}
	consent := b.get("/oauth/authorize")
	if consent.StatusCode != http.StatusOK || !strings.Contains(consent.Body, "Claude Code") {
		t.Fatalf("the consent page: %d\n%s", consent.StatusCode, consent.Body)
	}
	if b.cookie(o.srv.authzCookie) != nil {
		t.Error("the request cookie is still set after it was used")
	}
	if n := o.grants(); n != 0 {
		t.Fatalf("%d grant and token rows before Allow", n)
	}
	done := b.post("/oauth/authorize", url.Values{"csrf": {csrfOf(t, consent.Body)}, "request": {hidden(t, consent.Body, "request")}, "decision": {"allow"}})
	loc, _ := url.Parse(location(done))
	if done.StatusCode/100 != 3 || loc.Host != "localhost:3118" || loc.Path != "/callback" || loc.Query().Get("code") == "" ||
		loc.Query().Get("state") != "client-state-123" || loc.Query().Get("iss") != o.pub {
		t.Fatalf("after Allow: %d %q", done.StatusCode, location(done))
	}
	// The code works at the token endpoint, and the token is ada's.
	verifier, _ := oauthPKCE()
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"redirect_uri": {oauthCB}, "client_id": {o.clientID}, "code_verifier": {verifier}, "resource": {o.pub + "/mcp"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	o.handler.ServeHTTP(rec, req)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tok)
	_, u, _, err := o.as.VerifyAccessToken(context.Background(), tok.AccessToken)
	if err != nil || u.Person != "ada" {
		t.Fatalf("the token after the whole flow: %v, member %q (%d %s)", err, u.Person, rec.Code, rec.Body.String())
	}

	// Coming back with no request waiting is not an error of the server.
	if res := b.get("/oauth/authorize"); res.StatusCode != http.StatusBadRequest || !strings.Contains(res.Body, "Start again from the client") {
		t.Errorf("/oauth/authorize with nothing waiting: %d", res.StatusCode)
	}
	// A cookie somebody else made, or a changed one, is nothing waiting.
	other := o.browser()
	other.get(o.request())
	stolen := other.cookie(o.srv.authzCookie).Value
	for name, value := range map[string]string{"changed": stolen[:len(stolen)-3] + "AAA", "another id": strings.Repeat("A", 43) + stolen[43:], "no dot": "x"} {
		b.cookies[o.srv.authzCookie] = &http.Cookie{Name: o.srv.authzCookie, Value: value}
		if res := b.get("/oauth/authorize"); res.StatusCode != http.StatusBadRequest {
			t.Errorf("a request cookie that is %s: %d", name, res.StatusCode)
		}
	}
	// It runs out after ten minutes.
	late := o.browser()
	late.get(o.request())
	o.clock.advance(StateTTL)
	if res := late.get("/oauth/authorize"); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a request that waited more than ten minutes: %d", res.StatusCode)
	}
}

func TestConsentNamesClientRedirectHostAndPerson(t *testing.T) {
	o := newOAuthEnv(t)
	b, _ := o.signedIn(ada, "ada", store.RoleMember)
	page := b.get(o.request())
	if page.StatusCode != http.StatusOK {
		t.Fatalf("consent: %d %s", page.StatusCode, page.Body)
	}
	for _, want := range []string{"Claude Code", o.docs.Listener.Addr().String(), "localhost:3118", "ada", "Allow", "Deny"} {
		if !strings.Contains(page.Body, want) {
			t.Errorf("the consent page does not show %q", want)
		}
	}
	// The form's answer may be redirected to the client, and only there.
	if csp := page.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' http://localhost:3118;") {
		t.Errorf("CSP = %q", csp)
	}
	// An https client: no loopback warning, its own origin in the CSP.
	web := b.get(o.request(func(q url.Values) {
		q.Set("client_id", o.webID)
		q.Set("redirect_uri", "https://claude.example/api/mcp/auth_callback")
	}))
	if web.StatusCode != http.StatusOK || strings.Contains(web.Body, "runs on your computer") ||
		!strings.Contains(web.Header.Get("Content-Security-Policy"), "form-action 'self' https://claude.example;") {
		t.Errorf("an https client's consent: %d, CSP %q", web.StatusCode, web.Header.Get("Content-Security-Policy"))
	}
}

func TestLoopbackClientGetsAWarning(t *testing.T) {
	o := newOAuthEnv(t)
	b, _ := o.signedIn(ada, "ada", store.RoleMember)
	if page := b.get(o.request()); !strings.Contains(page.Body, "Any program on this computer can claim to be it") {
		t.Errorf("no warning for a client on the person's own computer:\n%s", page.Body)
	}
	// A client accepted only because every document is (policy "any") is
	// labelled, and its own name is shown as text.
	o.clients.Policy = oauthserver.PolicyAny
	page := b.get(o.request(func(q url.Values) {
		q.Set("client_id", o.docs.URL+"/other.json")
		q.Set("redirect_uri", "https://tool.example/cb")
	}))
	if page.StatusCode != http.StatusOK || !strings.Contains(page.Body, "not on Privo’s list") || strings.Contains(page.Body, "<b>Tool") {
		t.Errorf("an unlisted client's consent: %d\n%s", page.StatusCode, page.Body)
	}
}

func TestConsentNeedsCSRFAndASession(t *testing.T) {
	o := newOAuthEnv(t)
	b, _ := o.signedIn(ada, "ada", store.RoleMember)
	page := b.get(o.request())
	csrf, sealed := csrfOf(t, page.Body), hidden(t, page.Body, "request")
	post := func(br *browser, form url.Values, hdr map[string]string) int {
		t.Helper()
		res := br.do(http.MethodPost, "/oauth/authorize", form, hdr)
		if code := queryOf(location(res), "code"); code != "" {
			t.Errorf("a code was issued: %q", location(res))
		}
		return res.StatusCode
	}
	good := url.Values{"csrf": {csrf}, "request": {sealed}, "decision": {"allow"}}
	without := func(k string) url.Values {
		f := url.Values{}
		for key, v := range good {
			if key != k {
				f[key] = v
			}
		}
		return f
	}
	if got := post(b, without("csrf"), nil); got != http.StatusForbidden {
		t.Errorf("no CSRF token: %d", got)
	}
	if got := post(b, url.Values{"csrf": {"wrong"}, "request": {sealed}, "decision": {"allow"}}, nil); got != http.StatusForbidden {
		t.Errorf("a wrong CSRF token: %d", got)
	}
	if got := post(b, good, map[string]string{"Origin": "https://evil.example"}); got != http.StatusForbidden {
		t.Errorf("another site's form: %d", got)
	}
	if got := post(o.browser(), good, nil); got != http.StatusSeeOther {
		t.Errorf("no session: %d, want the sign-in redirect", got)
	}
	if got := post(b, without("request"), nil); got != http.StatusBadRequest {
		t.Errorf("no request: %d", got)
	}
	if got := post(b, url.Values{"csrf": {csrf}, "request": {sealed}, "decision": {"maybe"}}, nil); got != http.StatusBadRequest {
		t.Errorf("neither Allow nor Deny: %d", got)
	}
	// A GET never allows anything, whatever it carries.
	if res := b.get("/oauth/authorize?" + good.Encode()); queryOf(location(res), "code") != "" {
		t.Errorf("a GET issued a code: %q", location(res))
	}
	// The sealed request belongs to this session: another member cannot
	// answer it, and a request cookie is not a consent.
	other, _ := o.signedIn(ghUser{ID: 3003, Login: "bob"}, "bob", store.RoleMember)
	otherCSRF := csrfOf(t, other.get(o.request()).Body)
	if got := post(other, url.Values{"csrf": {otherCSRF}, "request": {sealed}, "decision": {"allow"}}, nil); got != http.StatusBadRequest {
		t.Errorf("another member's session with this member's request: %d", got)
	}
	anon := o.browser()
	anon.get(o.request())
	_, cookieSealed, _ := strings.Cut(anon.cookie(o.srv.authzCookie).Value, ".")
	if got := post(b, url.Values{"csrf": {csrf}, "request": {cookieSealed}, "decision": {"allow"}}, nil); got != http.StatusBadRequest {
		t.Errorf("a request cookie's value as the consent: %d", got)
	}
	if n := o.grants(); n != 0 {
		t.Fatalf("%d grant and token rows after refused answers", n)
	}
	// Deny goes back to the client with access_denied and writes no grant.
	res := b.post("/oauth/authorize", url.Values{"csrf": {csrf}, "request": {sealed}, "decision": {"deny"}})
	if queryOf(location(res), "error") != "access_denied" || queryOf(location(res), "state") != "client-state-123" || o.grants() != 0 {
		t.Errorf("deny: %d %q", res.StatusCode, location(res))
	}
	// And the real answer works.
	if res := b.post("/oauth/authorize", good); queryOf(location(res), "code") == "" {
		t.Errorf("allow: %d %q %s", res.StatusCode, location(res), res.Body)
	}
	// Ten minutes later the page is out of date.
	o.clock.advance(StateTTL)
	fresh, _ := o.signedIn(ghUser{ID: 4004, Login: "cy"}, "cy", store.RoleMember)
	p2 := fresh.get(o.request())
	o.clock.advance(StateTTL)
	if res := fresh.post("/oauth/authorize", url.Values{"csrf": {csrfOf(t, p2.Body)}, "request": {hidden(t, p2.Body, "request")}, "decision": {"allow"}}); queryOf(location(res), "code") != "" {
		t.Errorf("an answer ten minutes after the page: %q", location(res))
	}
}

func queryOf(loc, key string) string {
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	return u.Query().Get(key)
}

func TestApproverCannotAuthorizeAClient(t *testing.T) {
	o := newOAuthEnv(t)
	for _, role := range []string{store.RoleApprover, store.RoleAuditor} {
		b, _ := o.signedIn(ghUser{ID: int64(5000 + len(role)), Login: "p" + role}, "p-"+role, role)
		page := b.get(o.request())
		if page.StatusCode != http.StatusForbidden || !strings.Contains(page.Body, "cannot connect an AI client") || strings.Contains(page.Body, `name="decision"`) {
			t.Errorf("%s: %d\n%s", role, page.StatusCode, page.Body)
		}
	}
	// A member who is demoted between the page and the button.
	b, u := o.signedIn(ada, "ada", store.RoleMember)
	page := b.get(o.request())
	if err := o.st.SetUserRole(context.Background(), o.org, u.ID, store.RoleAuditor, o.event("role")); err != nil {
		t.Fatal(err)
	}
	res := b.post("/oauth/authorize", url.Values{"csrf": {csrfOf(t, page.Body)}, "request": {hidden(t, page.Body, "request")}, "decision": {"allow"}})
	if queryOf(location(res), "code") != "" || o.grants() != 0 {
		t.Errorf("a demoted member's Allow: %d %q", res.StatusCode, location(res))
	}
}

func TestAuthorizeRefusalsAreAPageOrARedirectToTheClient(t *testing.T) {
	o := newOAuthEnv(t)
	b, _ := o.signedIn(ada, "ada", store.RoleMember)
	// Not accepted: a page, and nothing in a Location header.
	for name, target := range map[string]string{
		"an unknown client":   o.request(func(q url.Values) { q.Set("client_id", o.docs.URL+"/other.json") }),
		"another redirect":    o.request(func(q url.Values) { q.Set("redirect_uri", "https://evil.example/cb") }),
		"no client":           o.request(func(q url.Values) { q.Del("client_id") }),
		"a very long request": o.request(func(q url.Values) { q.Set("state", strings.Repeat("s", 3000)) }),
	} {
		for _, br := range []*browser{b, o.browser()} {
			res := br.get(target)
			if res.StatusCode != http.StatusBadRequest || location(res) != "" {
				t.Errorf("%s: %d %q", name, res.StatusCode, location(res))
			}
			if br.cookie(o.srv.authzCookie) != nil {
				t.Errorf("%s: a refused request was put into a cookie", name)
			}
		}
	}
	// A request of an accepted client that is wrong: back to the client.
	res := o.browser().get(o.request(func(q url.Values) { q.Del("code_challenge") }))
	if queryOf(location(res), "error") != "invalid_request" || !strings.HasPrefix(location(res), oauthCB) {
		t.Errorf("no challenge: %d %q", res.StatusCode, location(res))
	}
	if strings.Contains(o.logs.String(), `"level":"ERROR"`) {
		t.Errorf("a refused request was logged as a server error:\n%s", o.logs.String())
	}
}

func TestOAuthEndpointsAnswer503WhenOff(t *testing.T) {
	e := newConnEnv(t) // sign-in is set up, the authorization server is off
	b, _ := e.signedIn(ada, "ada", store.RoleMember)
	for _, tc := range []struct{ method, path string }{{"GET", "/oauth/authorize?client_id=x"}, {"POST", "/oauth/authorize"}, {"POST", "/oauth/token"}, {"POST", "/oauth/revoke"}, {"POST", "/oauth/register"}} {
		var form url.Values
		if tc.method == "POST" {
			form = url.Values{}
		}
		if res := b.do(tc.method, tc.path, form, nil); res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s with the server off: %d, want 503", tc.method, tc.path, res.StatusCode)
		}
	}
	// Signing in cannot be made to return there either.
	if page := e.browser().get("/admin/signin?next=%2Foauth%2Fauthorize"); strings.Contains(page.Body, `value="/oauth/authorize"`) {
		t.Error("the sign-in page offers to return to /oauth/authorize while the server is off")
	}
	// Privo's own client document for upstream servers is still there.
	if res := b.get("/oauth/client-metadata.json"); res.StatusCode != http.StatusOK {
		t.Errorf("/oauth/client-metadata.json: %d", res.StatusCode)
	}
}
