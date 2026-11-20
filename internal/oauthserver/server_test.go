package oauthserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/store"
)

// ---- which clients are accepted (ADR 0022 points 11 and 12)

func TestClientDocumentMustMatchItsURL(t *testing.T) {
	e := newEnv(t, func(_ *Options, c *Clients) { c.Policy = PolicyAny })
	ctx := context.Background()
	good := e.docs.put("/good.json", "  Good <b>Client</b>\n", "https://good.example/cb")
	c, err := e.clients.Lookup(ctx, good)
	if err != nil || c.Kind != store.ClientDocument || c.Name != "Good bClientb" || len(c.RedirectURIs) != 1 {
		t.Fatalf("a well-formed document: %+v, %v", c, err)
	}

	other := e.docs.srv.URL + "/other.json"
	bad := map[string]string{
		"/other.json":    `{"client_id":"` + good + `","redirect_uris":["https://x.example/cb"]}`, // names another client
		"/secret.json":   `{"client_id":"ID","redirect_uris":["https://x.example/cb"],"token_endpoint_auth_method":"client_secret_basic"}`,
		"/secret2.json":  `{"client_id":"ID","redirect_uris":["https://x.example/cb"],"client_secret":"s"}`,
		"/nocb.json":     `{"client_id":"ID"}`,
		"/httpcb.json":   `{"client_id":"ID","redirect_uris":["http://attacker.example/cb"]}`,
		"/fragcb.json":   `{"client_id":"ID","redirect_uris":["https://x.example/cb#frag"]}`,
		"/usercb.json":   `{"client_id":"ID","redirect_uris":["https://user@x.example/cb"]}`,
		"/implicit.json": `{"client_id":"ID","redirect_uris":["https://x.example/cb"],"response_types":["token"]}`,
		"/grant.json":    `{"client_id":"ID","redirect_uris":["https://x.example/cb"],"grant_types":["client_credentials"]}`,
		"/notjson.json":  `<html>`,
		"/big.json":      `{"client_id":"ID","redirect_uris":["https://x.example/cb"],"pad":"` + strings.Repeat("x", 70<<10) + `"}`,
	}
	for path, body := range bad {
		id := e.docs.srv.URL + path
		e.docs.raw(path, strings.ReplaceAll(body, `"ID"`, `"`+id+`"`))
		if _, err := e.clients.Lookup(ctx, id); err == nil {
			t.Errorf("%s was accepted", path)
		}
	}
	_ = other
	// Not JSON by its type, a redirect, an error status, and no document.
	typed := e.docs.put("/typed.json", "T", "https://x.example/cb")
	e.docs.ctype["/typed.json"] = "text/html"
	moved := e.docs.put("/moved.json", "M", "https://x.example/cb")
	e.docs.status["/moved.json"] = http.StatusFound
	down := e.docs.put("/down.json", "D", "https://x.example/cb")
	e.docs.status["/down.json"] = http.StatusServiceUnavailable
	for _, id := range []string{typed, moved, down, e.docs.srv.URL + "/missing.json"} {
		if _, err := e.clients.Lookup(ctx, id); err == nil {
			t.Errorf("%s was accepted", id)
		}
	}
	// Ids that cannot be a document's address.
	e.clients.AllowHTTP = false
	for _, id := range []string{"", "claude", "http://plain.example/c.json", "https://nopath.example", "https://nopath.example/", "https://u:p@x.example/c.json",
		"https://x.example/c.json#frag", "https://x.example/a/../c.json", "ftp://x.example/c.json", "https:///c.json", "https://x.example/" + strings.Repeat("a", 3000)} {
		if _, err := e.clients.Lookup(ctx, id); err == nil {
			t.Errorf("client id %q was accepted", id)
		}
	}
}

func TestClientDocumentIsCachedAndAFailureIsNotRepeated(t *testing.T) {
	e := newEnv(t, func(_ *Options, c *Clients) { c.Policy = PolicyAny })
	ctx := context.Background()
	id := e.docs.put("/c.json", "C", "https://x.example/cb")
	e.docs.cache["/c.json"] = "max-age=600"
	for i := 0; i < 5; i++ {
		if _, err := e.clients.Lookup(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.docs.count("/c.json"); n != 1 {
		t.Errorf("%d fetches of a cached document, want 1", n)
	}
	e.clk.advance(11 * time.Minute)
	_, _ = e.clients.Lookup(ctx, id)
	if n := e.docs.count("/c.json"); n != 2 {
		t.Errorf("%d fetches after the document's ten minutes, want 2", n)
	}
	// A document that asks to be kept for a year is kept for a day.
	long := e.docs.put("/long.json", "L", "https://x.example/cb")
	e.docs.cache["/long.json"] = "public, max-age=31536000"
	_, _ = e.clients.Lookup(ctx, long)
	e.clk.advance(25 * time.Hour)
	_, _ = e.clients.Lookup(ctx, long)
	if n := e.docs.count("/long.json"); n != 2 {
		t.Errorf("%d fetches over 25 hours, want 2", n)
	}
	// A failure is remembered for a minute: requests without a session
	// cannot make Privo fetch an address over and over.
	missing := e.docs.srv.URL + "/missing.json"
	for i := 0; i < 20; i++ {
		_, _ = e.clients.Lookup(ctx, missing)
	}
	if n := e.docs.count("/missing.json"); n != 1 {
		t.Errorf("%d fetches of a failing address within a minute, want 1", n)
	}
	e.clk.advance(61 * time.Second)
	_, _ = e.clients.Lookup(ctx, missing)
	if n := e.docs.count("/missing.json"); n != 2 {
		t.Errorf("%d fetches after the minute, want 2", n)
	}
	for age, want := range map[string]time.Duration{"": time.Hour, "max-age=5": 5 * time.Minute, "no-store": 5 * time.Minute, "max-age=7200": 2 * time.Hour, "max-age=x": time.Hour} {
		if got := cacheAge(age); got != want {
			t.Errorf("cacheAge(%q) = %s, want %s", age, got, want)
		}
	}
}

func TestClientDocumentFetchRefusesPrivateAddresses(t *testing.T) {
	// The real fetcher: the egress policy with nothing allowed beyond public
	// addresses. The document server of the test is on 127.0.0.1.
	e := newEnv(t, func(_ *Options, c *Clients) {
		c.Policy = PolicyAny
		c.HTTP = NewDocumentClient(egress.Policy{})
	})
	id := e.docs.put("/c.json", "C", "https://x.example/cb")
	_, err := e.clients.Lookup(context.Background(), id)
	if err == nil {
		t.Fatal("a document on a loopback address was fetched")
	}
	if n := e.docs.count("/c.json"); n != 0 {
		t.Errorf("the private address was requested %d times", n)
	}
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("the refusal names the internal address: %v", err)
	}
}

func TestLoopbackRedirectIgnoresThePort(t *testing.T) {
	declared := []string{"http://localhost/callback", "http://127.0.0.1:33418/", "http://[::1]/cb?x=1", "https://app.example/cb"}
	for _, ok := range []string{"http://localhost:3118/callback", "http://localhost/callback", "http://localhost:65535/callback",
		"http://127.0.0.1:1/", "http://127.0.0.1/", "http://[::1]:8080/cb?x=1"} {
		if !MatchRedirect(declared, ok) {
			t.Errorf("%s was refused", ok)
		}
	}
	for _, bad := range []string{
		"http://localhost:3118/other",         // another path
		"http://localhost:3118/callback?x=1",  // a query the client did not declare
		"http://localhost:3118/callback#frag", // a fragment
		"http://127.0.0.1:3118/callback",      // localhost is not 127.0.0.1
		"http://localhost.evil.example/callback",
		"http://user@localhost:3118/callback",
		"https://localhost:3118/callback", // the scheme counts
		"http://[::1]:8080/cb",            // the query counts
		"http://[::1]:8080/cb?x=2",
		"http://127.0.0.2:1/",
		"",
	} {
		if MatchRedirect(declared, bad) {
			t.Errorf("%s was accepted", bad)
		}
	}
}

func TestOtherRedirectsMatchExactly(t *testing.T) {
	declared := []string{"https://app.example/cb", "http://localhost/callback"}
	if !MatchRedirect(declared, "https://app.example/cb") {
		t.Error("the declared address was refused")
	}
	for _, bad := range []string{"https://app.example:8443/cb", "https://app.example/cb/", "https://app.example/cb?x=1", "https://APP.example/cb",
		"https://app.example/cb/../cb", "https://app.example.evil.example/cb", "http://app.example/cb", "https://app.example/cb#x", "https://app.example/"} {
		if MatchRedirect(declared, bad) {
			t.Errorf("%s was accepted", bad)
		}
	}
	// What the library is then given is the one address that passed.
	e := newEnv(t)
	u := e.member("ada", store.RoleMember)
	_, loc := e.allow(u, e.query(set("redirect_uri", "http://localhost:50123/callback")))
	if loc.Host != "localhost:50123" || loc.Path != "/callback" {
		t.Errorf("sent back to %s, want the port the client asked for", loc)
	}
}

func TestUnknownClientIsRefusedUntilApproved(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	unknown := e.docs.put("/unknown.json", "Unknown", "https://unknown.example/cb")
	q := e.query(set("client_id", unknown), set("redirect_uri", "https://unknown.example/cb"))

	_, aerr := e.srv.ParseAuthorize(ctx, q)
	if aerr == nil || !aerr.Page || aerr.Redirect != nil || aerr.Host == "" || !strings.Contains(aerr.Detail, "administrator can approve") {
		t.Fatalf("an unknown client: %+v", aerr)
	}
	if n := e.docs.count("/unknown.json"); n != 0 {
		t.Errorf("an unknown client's document was fetched %d times before anyone approved it", n)
	}
	// Approved by an administrator: accepted, under every policy.
	if err := e.st.SaveOAuthClient(ctx, e.org, store.OAuthClient{ClientID: unknown, Kind: store.ClientApproved, Name: "Unknown", CreatedAt: e.clk.now()}, 0, nil); err != nil {
		t.Fatal(err)
	}
	req, aerr := e.srv.ParseAuthorize(ctx, q)
	if aerr != nil || req.Client.Kind != store.ClientApproved {
		t.Fatalf("after approval: %+v, %+v", req, aerr)
	}
	// Policy "approved": the built-in list alone is not enough.
	e.clients.Policy = PolicyApproved
	if _, aerr := e.srv.ParseAuthorize(ctx, e.query()); aerr == nil || !aerr.Page {
		t.Errorf("a known client under the approved-only policy: %+v", aerr)
	}
	if _, aerr := e.srv.ParseAuthorize(ctx, q); aerr != nil {
		t.Errorf("an approved client under the approved-only policy: %+v", aerr)
	}
	// Policy "any": every document, labelled as such.
	e.clients.Policy = PolicyAny
	unlisted := e.docs.put("/any.json", "Any", "https://any.example/cb")
	req, aerr = e.srv.ParseAuthorize(ctx, e.query(set("client_id", unlisted), set("redirect_uri", "https://any.example/cb")))
	if aerr != nil || req.Client.Kind != store.ClientDocument {
		t.Errorf("under the any policy: %+v, %+v", req, aerr)
	}
	// A client that is neither a document nor registered here.
	if _, aerr := e.srv.ParseAuthorize(ctx, e.query(set("client_id", "privo_client_nobody"))); aerr == nil || !aerr.Page {
		t.Errorf("an unregistered id: %+v", aerr)
	}
}

// ---- the authorization request (points 6, 8, 9)

func TestAuthorizeErrorsRedirectOnlyToAnAcceptedURI(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// Shown as a page, sent nowhere: the client or its address is not accepted.
	for name, q := range map[string]url.Values{
		"no client":            e.query(del("client_id")),
		"another redirect":     e.query(set("redirect_uri", "https://evil.example/cb")),
		"no redirect":          e.query(del("redirect_uri")),
		"loopback, other path": e.query(set("redirect_uri", "http://localhost:3118/steal")),
		"a parameter twice":    func() url.Values { q := e.query(); q.Add("redirect_uri", "https://evil.example/cb"); return q }(),
		"client id twice":      func() url.Values { q := e.query(); q.Add("client_id", "https://evil.example/c.json"); return q }(),
		// The library refuses a request object before it has accepted an address.
		"a request object": e.query(set("request", "eyJhbGciOiJub25lIn0.e30.")),
	} {
		_, aerr := e.srv.ParseAuthorize(ctx, q)
		if aerr == nil || !aerr.Page || aerr.Redirect != nil {
			t.Errorf("%s: %+v, want a page and no redirect", name, aerr)
		}
	}
	// Sent to the client's own address, with the error and the issuer.
	for name, tc := range map[string]struct {
		q    url.Values
		want string
	}{
		"no challenge":           {e.query(del("code_challenge")), "invalid_request"},
		"plain challenge":        {e.query(set("code_challenge_method", "plain")), "invalid_request"},
		"no method":              {e.query(del("code_challenge_method")), "invalid_request"},
		"short challenge":        {e.query(set("code_challenge", "short")), "invalid_request"},
		"unknown scope":          {e.query(set("scope", "mcp admin")), "invalid_scope"},
		"no resource":            {e.query(del("resource")), "invalid_target"},
		"another resource":       {e.query(set("resource", "https://other.example/mcp")), "invalid_target"},
		"the issuer as resource": {e.query(set("resource", issuer)), "invalid_target"},
		"implicit":               {e.query(set("response_type", "token")), "unsupported_response_type"},
	} {
		_, aerr := e.srv.ParseAuthorize(ctx, tc.q)
		if aerr == nil || aerr.Page || aerr.Redirect == nil {
			t.Errorf("%s: %+v, want a redirect", name, aerr)
			continue
		}
		rec := httptest.NewRecorder()
		aerr.Redirect(rec)
		loc, _ := url.Parse(rec.Header().Get("Location"))
		if loc.Scheme+"://"+loc.Host+loc.Path != loopbackCB || loc.Query().Get("error") != tc.want || loc.Query().Get("state") != "state-0123456789" || loc.Query().Get("code") != "" {
			t.Errorf("%s: redirected to %s, want %s with error=%s and the state", name, loc, loopbackCB, tc.want)
		}
	}
}

func TestAuthorizeWritesNothingBeforeConsent(t *testing.T) {
	e := newEnv(t)
	u := e.member("ada", store.RoleMember)
	count := func() string {
		return fmt.Sprint(e.rows("oauth_grants"), e.rows("oauth_tokens"), e.rows("oauth_clients"), e.rows("audit_events"), e.rows("oauth_states"))
	}
	before := count()
	for i := 0; i < 50; i++ {
		if _, aerr := e.srv.ParseAuthorize(context.Background(), e.query()); aerr != nil {
			t.Fatal(aerr)
		}
		_, _ = e.srv.ParseAuthorize(context.Background(), e.query(del("code_challenge")))
		_, _ = e.srv.ParseAuthorize(context.Background(), e.query(set("client_id", "https://nobody.example/c.json")))
	}
	if after := count(); after != before {
		t.Errorf("rows changed by requests nobody allowed: %s, was %s", after, before)
	}
	// Deny writes an audit row and nothing else; Allow is the first write.
	req, _ := e.srv.ParseAuthorize(context.Background(), e.query())
	rec := httptest.NewRecorder()
	e.srv.Deny(rec, req, u)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("error") != "access_denied" || loc.Query().Get("iss") != issuer || e.rows("oauth_grants") != 0 {
		t.Errorf("deny: %s, %d grants", loc, e.rows("oauth_grants"))
	}
	code, loc := e.allow(u, e.query())
	if code == "" || loc.Query().Get("iss") != issuer || loc.Query().Get("state") != "state-0123456789" {
		t.Errorf("allow: %s", loc)
	}
	if e.rows("oauth_grants") != 1 || e.rows("oauth_tokens") != 2 {
		t.Errorf("after allow: %d grants, %d rows (want the code and its PKCE session)", e.rows("oauth_grants"), e.rows("oauth_tokens"))
	}
}

func TestGrantBelongsToTheMemberWhoConsented(t *testing.T) {
	e := newEnv(t)
	ada := e.member("ada", store.RoleMember)
	tok := e.signedIn(ada)
	g, u, exp, err := e.srv.VerifyAccessToken(context.Background(), tok.AccessToken)
	if err != nil || u.ID != ada.ID || g.UserID != ada.ID || g.ClientID != e.clientID || g.ClientName != "Known Client" || g.ClientKind != store.ClientKnown || g.Scope != "mcp" {
		t.Fatalf("VerifyAccessToken = %+v, %+v, %v", g, u, err)
	}
	if d := exp.Sub(e.clk.now()); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("the access token runs %s, want an hour", d)
	}
	if tok.TokenType != "bearer" || tok.ExpiresIn < 3500 || tok.ExpiresIn > 3600 || tok.Scope != "mcp" {
		t.Errorf("token response: %+v", tok)
	}
	if !strings.HasPrefix(tok.AccessToken, "privo_at_") || !strings.HasPrefix(tok.RefreshToken, "privo_rt_") {
		t.Errorf("token shapes: %.12s… %.12s…", tok.AccessToken, tok.RefreshToken)
	}
	// An approver or auditor cannot be acted as, whoever presses Allow.
	for _, role := range []string{store.RoleApprover, store.RoleAuditor} {
		who := e.member("p-"+role, role)
		req, _ := e.srv.ParseAuthorize(context.Background(), e.query())
		rec := httptest.NewRecorder()
		e.srv.Allow(rec, req, who)
		loc, _ := url.Parse(rec.Header().Get("Location"))
		if loc.Query().Get("code") != "" || loc.Query().Get("error") == "" {
			t.Errorf("%s was given a code: %s", role, loc)
		}
	}
	if n := e.rows("oauth_grants"); n != 1 {
		t.Errorf("%d grants, want ada's one", n)
	}
	got := strings.Join(e.actions(), " ")
	if !strings.Contains(got, "oauth.grant.allow") || !strings.Contains(got, "oauth.token.exchange") {
		t.Errorf("audit: %s", got)
	}
}

// ---- the token endpoint

func TestPKCERequired(t *testing.T) {
	e := newEnv(t)
	u := e.member("ada", store.RoleMember)
	for name, edit := range map[string]func(url.Values){
		"no verifier":      del("code_verifier"),
		"another verifier": set("code_verifier", strings.Repeat("w", 43)),
		"a short verifier": set("code_verifier", "short"),
	} {
		code, _ := e.allow(u, e.query())
		if tok := e.exchange(code, edit); tok.Status == http.StatusOK || tok.AccessToken != "" {
			t.Errorf("%s: %d %s", name, tok.Status, tok.Raw)
		}
	}
	// The exchange is bound to the client and the redirect address too.
	other := e.docs.put("/other.json", "Other", loopbackCB)
	e.clients.Known = append(e.clients.Known, KnownClient{URL: other, Name: "Other"})
	for name, edit := range map[string]func(url.Values){
		"another client":   set("client_id", other),
		"another redirect": set("redirect_uri", "http://localhost:3118/other"),
		"another port":     set("redirect_uri", "http://localhost:9999/callback"),
		"no client":        del("client_id"),
		"an unknown code":  set("code", "privo_ac_nothing.nothing"),
	} {
		code, _ := e.allow(u, e.query())
		if tok := e.exchange(code, edit); tok.Status == http.StatusOK || tok.AccessToken != "" {
			t.Errorf("%s: %d %s", name, tok.Status, tok.Raw)
		}
	}
	code, _ := e.allow(u, e.query())
	if tok := e.exchange(code); tok.Status != http.StatusOK {
		t.Errorf("a fresh code with its own verifier: %d %s", tok.Status, tok.Raw)
	}
	// A code runs out (a minute by default; a second here, on the real clock).
	short := newEnv(t, func(o *Options, _ *Clients) { o.CodeTTL, o.Now = time.Second, time.Now })
	code, _ = short.allow(short.member("ada", store.RoleMember), short.query())
	time.Sleep(1100 * time.Millisecond)
	if tok := short.exchange(code); tok.Status == http.StatusOK {
		t.Error("a code was exchanged after its time")
	}
}

func TestTokenRequestNeedsTheSameResource(t *testing.T) {
	e := newEnv(t)
	u := e.member("ada", store.RoleMember)
	for name, edit := range map[string]func(url.Values){
		"no resource":      del("resource"),
		"another resource": set("resource", "https://other.example/mcp"),
		"the issuer":       set("resource", issuer),
		"two resources":    func(f url.Values) { f.Add("resource", "https://other.example/mcp") },
	} {
		code, _ := e.allow(u, e.query())
		tok := e.exchange(code, edit)
		if tok.Status != http.StatusBadRequest || tok.Error != "invalid_target" || tok.AccessToken != "" {
			t.Errorf("%s: %d %s", name, tok.Status, tok.Raw)
		}
		// The refusal was Privo's, in front of the library: the code is not
		// used up by it.
		if tok := e.exchange(code); tok.Status != http.StatusOK {
			t.Errorf("%s: the code was used up by the refused request: %d %s", name, tok.Status, tok.Raw)
		}
	}
	// A refresh may leave the resource out; a wrong one is refused and
	// costs nothing.
	tok := e.signedIn(u)
	if r := e.refresh(tok.RefreshToken, set("resource", "https://other.example/mcp")); r.Status != http.StatusBadRequest || r.Error != "invalid_target" {
		t.Errorf("refresh for another resource: %d %s", r.Status, r.Raw)
	}
	if r := e.refresh(tok.RefreshToken, set("resource", resource)); r.Status != http.StatusOK {
		t.Errorf("refresh naming this gateway: %d %s", r.Status, r.Raw)
	}
}

func TestCodeWorksOnceAndASecondUseEndsTheGrant(t *testing.T) {
	e := newEnv(t)
	u := e.member("ada", store.RoleMember)
	code, _ := e.allow(u, e.query())
	first := e.exchange(code)
	if first.Status != http.StatusOK || !e.verifies(first.AccessToken) {
		t.Fatalf("first use: %d %s", first.Status, first.Raw)
	}
	second := e.exchange(code)
	if second.Status != http.StatusBadRequest || second.Error != "invalid_grant" || second.AccessToken != "" {
		t.Errorf("second use: %d %s", second.Status, second.Raw)
	}
	if e.verifies(first.AccessToken) {
		t.Error("the tokens of the first use still work after the code was used again")
	}
	if r := e.refresh(first.RefreshToken); r.Status == http.StatusOK {
		t.Error("the refresh token of the first use still works")
	}
	if e.rows("oauth_grants") != 0 || e.rows("oauth_tokens") != 0 {
		t.Errorf("%d grants and %d rows left", e.rows("oauth_grants"), e.rows("oauth_tokens"))
	}
	if got := strings.Join(e.actions(), " "); !strings.Contains(got, "oauth.grant.reuse") {
		t.Errorf("the reuse is not in the audit: %s", got)
	}
}

func TestRefreshRotates(t *testing.T) {
	e := newEnv(t)
	u := e.member("ada", store.RoleMember)
	tok := e.signedIn(u)
	e.clk.advance(50 * time.Minute)
	next := e.refresh(tok.RefreshToken)
	if next.Status != http.StatusOK || next.AccessToken == "" || next.RefreshToken == "" {
		t.Fatalf("refresh: %d %s", next.Status, next.Raw)
	}
	if next.AccessToken == tok.AccessToken || next.RefreshToken == tok.RefreshToken {
		t.Error("a refresh returned a token it had issued before")
	}
	if e.verifies(tok.AccessToken) {
		t.Error("the old access token still works after the refresh")
	}
	if !e.verifies(next.AccessToken) {
		t.Error("the new access token does not work")
	}
	g, _, _, _ := e.srv.VerifyAccessToken(context.Background(), next.AccessToken)
	if g.UserID != u.ID || g.ClientID != e.clientID {
		t.Errorf("the grant after a refresh: %+v", g)
	}
	// A refresh as another client is refused and leaves the grant alone.
	other := e.docs.put("/other.json", "Other", loopbackCB)
	e.clients.Known = append(e.clients.Known, KnownClient{URL: other, Name: "Other"})
	if r := e.refresh(next.RefreshToken, set("client_id", other)); r.Status == http.StatusOK {
		t.Error("another client refreshed with this client's token")
	}
	if !e.verifies(next.AccessToken) {
		t.Error("another client's attempt ended the grant")
	}
	// One grant, one live refresh token, however often it is refreshed.
	for i := 0; i < 5; i++ {
		next = e.refresh(next.RefreshToken)
		if next.Status != http.StatusOK {
			t.Fatalf("refresh %d: %d %s", i, next.Status, next.Raw)
		}
	}
	list, _ := e.st.ListClientGrants(context.Background(), e.org, u.ID, e.clk.now())
	if len(list) != 1 || e.rows("oauth_grants") != 1 {
		t.Errorf("%d clients listed, %d grants", len(list), e.rows("oauth_grants"))
	}
}

// ADR 0022 point 9, the owner's decision of 2026-11-12: no grace window.
func TestRotatedRefreshTokenIsRefusedAndRevokesTheGrant(t *testing.T) {
	e := newEnv(t)
	ada, bob := e.member("ada", store.RoleMember), e.member("bob", store.RoleMember)
	tok, bobs := e.signedIn(ada), e.signedIn(bob)
	next := e.refresh(tok.RefreshToken)
	if next.Status != http.StatusOK {
		t.Fatal(next.Raw)
	}
	// The old one again, at once: refused, and everything after it stops.
	again := e.refresh(tok.RefreshToken)
	if again.Status != http.StatusBadRequest || again.Error != "invalid_grant" || again.AccessToken != "" || again.RefreshToken != "" {
		t.Fatalf("a rotated refresh token: %d %s", again.Status, again.Raw)
	}
	if e.verifies(next.AccessToken) {
		t.Error("the access token issued after the reused one still works")
	}
	if r := e.refresh(next.RefreshToken); r.Status != http.StatusBadRequest || r.Error != "invalid_grant" {
		t.Errorf("the successor refresh token: %d %s", r.Status, r.Raw)
	}
	// Somebody else's client is not touched.
	if !e.verifies(bobs.AccessToken) {
		t.Error("another member's client was ended too")
	}
	if r := e.refresh(bobs.RefreshToken); r.Status != http.StatusOK {
		t.Errorf("another member's refresh: %d %s", r.Status, r.Raw)
	}
	var client, userID, result string
	if err := e.db().QueryRow(`SELECT client, user_id, outcome FROM audit_events WHERE action = 'oauth.grant.reuse'`).Scan(&client, &userID, &result); err != nil {
		t.Fatalf("the reuse is not in the audit: %v", err)
	}
	if client != "Known Client" || userID != ada.ID || !strings.Contains(result, "refresh token") {
		t.Errorf("audit of the reuse: client %q, member %q, %q", client, userID, result)
	}
	// A dead or made-up refresh token is invalid_grant, as clients expect.
	for _, bad := range []string{"privo_rt_nothing.nothing", "nonsense", tok.AccessToken} {
		if r := e.refresh(bad); r.Status != http.StatusBadRequest || r.Error != "invalid_grant" {
			t.Errorf("refresh with %q: %d %s", bad, r.Status, r.Raw)
		}
	}
}

func TestGrantRunsOutIdleAndAbsolute(t *testing.T) {
	// Short lifetimes: the library reads the real clock for a token's time.
	e := newEnv(t, func(o *Options, _ *Clients) {
		o.AccessTTL, o.RefreshTTL, o.GrantTTL = 2*time.Second, time.Hour, 3*time.Second
		o.Now = time.Now
	})
	u := e.member("ada", store.RoleMember)
	tok := e.signedIn(u)
	if !e.verifies(tok.AccessToken) {
		t.Fatal("a fresh token does not verify")
	}
	time.Sleep(2100 * time.Millisecond)
	if e.verifies(tok.AccessToken) {
		t.Error("an access token works after its time")
	}
	next := e.refresh(tok.RefreshToken)
	if next.Status != http.StatusOK {
		t.Fatalf("refresh within the grant's time: %d %s", next.Status, next.Raw)
	}
	// Past the grant's absolute time nothing works, whatever was refreshed.
	time.Sleep(1100 * time.Millisecond)
	if e.verifies(next.AccessToken) {
		t.Error("an access token works after the grant ran out")
	}
	if r := e.refresh(next.RefreshToken); r.Status != http.StatusBadRequest || r.Error != "invalid_grant" {
		t.Errorf("refresh after the grant ran out: %d %s", r.Status, r.Raw)
	}

	// Idle: a refresh token nobody used for its lifetime is dead.
	e2 := newEnv(t, func(o *Options, _ *Clients) { o.RefreshTTL, o.Now = time.Second, time.Now })
	tok2 := e2.signedIn(e2.member("ada", store.RoleMember))
	time.Sleep(1100 * time.Millisecond)
	if r := e2.refresh(tok2.RefreshToken); r.Status != http.StatusBadRequest || r.Error != "invalid_grant" {
		t.Errorf("an idle refresh token: %d %s", r.Status, r.Raw)
	}
}

func TestRevocationEndsTheGrant(t *testing.T) {
	e := newEnv(t)
	u := e.member("ada", store.RoleMember)
	for _, which := range []string{"access", "refresh"} {
		tok := e.signedIn(u)
		token := tok.RefreshToken
		if which == "access" {
			token = tok.AccessToken
		}
		if r := e.post("/oauth/revoke", url.Values{"token": {token}, "client_id": {e.clientID}}); r.Status != http.StatusOK {
			t.Fatalf("revoking the %s token: %d %s", which, r.Status, r.Raw)
		}
		if e.verifies(tok.AccessToken) {
			t.Errorf("after revoking the %s token the access token still works", which)
		}
		if r := e.refresh(tok.RefreshToken); r.Status == http.StatusOK {
			t.Errorf("after revoking the %s token the refresh token still works", which)
		}
	}
	// An unknown token is not an error (RFC 7009), and tells nothing.
	if r := e.post("/oauth/revoke", url.Values{"token": {"privo_rt_nothing.nothing"}, "client_id": {e.clientID}}); r.Status != http.StatusOK {
		t.Errorf("revoking an unknown token: %d %s", r.Status, r.Raw)
	}
	if got := strings.Join(e.actions(), " "); strings.Count(got, "oauth.grant.revoke") != 2 {
		t.Errorf("audit: %s", got)
	}
}

// ---- verification

func TestVerifyReadsTheMembersRoleLive(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ada := e.member("ada", store.RoleMember)
	tok := e.signedIn(ada)
	ev := store.AuditEvent{ID: "e1", CallID: "c1", Time: e.clk.now(), Actor: "t", ActorID: "t", Action: "role", Decision: "allow", Outcome: "ok"}

	// Promoted: the same token now carries the new role.
	if err := e.st.SetUserRole(ctx, e.org, ada.ID, store.RoleAdmin, ev); err != nil {
		t.Fatal(err)
	}
	if _, u, _, err := e.srv.VerifyAccessToken(ctx, tok.AccessToken); err != nil || u.Role != store.RoleAdmin {
		t.Errorf("after a promotion: role %q, %v", u.Role, err)
	}
	// A role that may not use the gateway: refused, and the grant is gone.
	ev.ID, ev.CallID = "e2", "c2"
	if err := e.st.SetUserRole(ctx, e.org, ada.ID, store.RoleAuditor, ev); err != nil {
		t.Fatal(err)
	}
	if e.verifies(tok.AccessToken) {
		t.Error("an auditor's token works on the gateway")
	}
	if r := e.refresh(tok.RefreshToken); r.Status == http.StatusOK {
		t.Error("an auditor's client refreshed")
	}
	// Removed.
	bob := e.member("bob", store.RoleMember)
	bobs := e.signedIn(bob)
	ev.ID, ev.CallID = "e3", "c3"
	if err := e.st.DisableUser(ctx, e.org, bob.ID, e.clk.now(), ev); err != nil {
		t.Fatal(err)
	}
	if e.verifies(bobs.AccessToken) {
		t.Error("a removed member's token works")
	}
	// The store ends a member's grants when their role or membership ends,
	// so the checks above pass by that alone. Verification does not rely on
	// it: with the grant still there (the rows changed behind the store's
	// back), the member's role and membership are still read.
	for _, change := range []string{`role = 'auditor'`, `role = 'approver'`, `disabled_at = '2026-01-01T00:00:00Z'`} {
		who := e.member("w"+fmt.Sprint(len(change)), store.RoleMember)
		theirs := e.signedIn(who)
		rw, err := sql.Open("sqlite", "file:"+filepath.Join(e.dir, "privo.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rw.Exec(`UPDATE users SET `+change+` WHERE id = ?`, who.ID); err != nil {
			t.Fatal(err)
		}
		_ = rw.Close()
		if e.rows("oauth_grants") == 0 {
			t.Fatal("the grant is gone: this check proves nothing")
		}
		if e.verifies(theirs.AccessToken) {
			t.Errorf("with %s and the grant still stored, the token works", change)
		}
	}
}

func TestVerifyRefusesEverythingThatIsNotALiveAccessToken(t *testing.T) {
	e := newEnv(t)
	u := e.member("ada", store.RoleMember)
	tok := e.signedIn(u)
	other := newEnv(t)
	foreign := other.signedIn(other.member("ada", store.RoleMember))
	dot := strings.LastIndex(tok.AccessToken, ".")
	for name, bearer := range map[string]string{
		"empty":                        "",
		"a personal token's shape":     "privo_pat_abc.def",
		"the refresh token":            tok.RefreshToken,
		"the refresh token renamed":    "privo_at_" + strings.TrimPrefix(tok.RefreshToken, "privo_rt_"),
		"another installation's token": foreign.AccessToken,
		"the signature alone":          "privo_at_" + tok.AccessToken[dot+1:],
		"another key, this signature":  "privo_at_" + strings.Repeat("A", 43) + tok.AccessToken[dot:],
		"a changed signature":          tok.AccessToken[:len(tok.AccessToken)-2] + "AA",
		"too long":                     tok.AccessToken + strings.Repeat("A", 600),
		"no prefix":                    strings.TrimPrefix(tok.AccessToken, "privo_at_"),
	} {
		if e.verifies(bearer) {
			t.Errorf("%s verified", name)
		}
	}
	if !e.verifies(tok.AccessToken) {
		t.Error("the real token no longer verifies")
	}
}

func TestTokensAreStoredBySignatureOnlyAndSurviveARestart(t *testing.T) {
	e := newEnv(t)
	u := e.member("ada", store.RoleMember)
	code, _ := e.allow(u, e.query())
	tok := e.exchange(code)
	// No table holds a token, a code or the signing secret in the clear.
	rows, err := e.db().Query(`SELECT signature || ' ' || request FROM oauth_tokens UNION ALL SELECT hex(secret) FROM oauth_keys`)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		all.WriteString(s + "\n")
	}
	_ = rows.Close()
	key := func(token, prefix string) string {
		return strings.SplitN(strings.TrimPrefix(token, prefix), ".", 2)[0]
	}
	for name, secret := range map[string]string{"access token": tok.AccessToken, "refresh token": tok.RefreshToken, "code": code,
		"access key": key(tok.AccessToken, "privo_at_"), "refresh key": key(tok.RefreshToken, "privo_rt_"), "code key": key(code, "privo_ac_")} {
		if strings.Contains(all.String(), secret) {
			t.Errorf("the database holds the %s", name)
		}
	}
	for _, line := range strings.Split(e.logs.String(), "\n") {
		if strings.Contains(line, tok.AccessToken) || strings.Contains(line, tok.RefreshToken) || strings.Contains(line, code) {
			t.Errorf("a log line holds a token: %.80s", line)
		}
	}
	// The signing secret is made once: after a restart the same tokens work.
	e.restart()
	if !e.verifies(tok.AccessToken) {
		t.Error("an access token does not survive a restart")
	}
	if r := e.refresh(tok.RefreshToken); r.Status != http.StatusOK {
		t.Errorf("a refresh after a restart: %d %s", r.Status, r.Raw)
	}
	if n := e.rows("oauth_keys"); n != 1 {
		t.Errorf("%d signing keys, want 1", n)
	}
}

func TestMetadata(t *testing.T) {
	e := newEnv(t)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("metadata: %d %v", rec.Code, err)
	}
	list := func(k string) string { return fmt.Sprint(m[k]) }
	must(t, m["issuer"] == issuer, "issuer = %v", m["issuer"])
	must(t, m["authorization_endpoint"] == issuer+"/oauth/authorize" && m["token_endpoint"] == issuer+"/oauth/token" && m["revocation_endpoint"] == issuer+"/oauth/revoke", "endpoints: %v", m)
	must(t, list("code_challenge_methods_supported") == "[S256]", "challenge methods: %s", list("code_challenge_methods_supported"))
	// The two things Claude needs before it uses a metadata document.
	must(t, m["client_id_metadata_document_supported"] == true, "client_id_metadata_document_supported = %v", m["client_id_metadata_document_supported"])
	must(t, list("token_endpoint_auth_methods_supported") == "[none]", "auth methods: %s", list("token_endpoint_auth_methods_supported"))
	must(t, list("grant_types_supported") == "[authorization_code refresh_token]", "grants: %s", list("grant_types_supported"))
	must(t, list("response_types_supported") == "[code]", "response types: %s", list("response_types_supported"))
	must(t, m["authorization_response_iss_parameter_supported"] == true, "iss parameter: %v", m["authorization_response_iss_parameter_supported"])
	if _, ok := m["registration_endpoint"]; ok {
		t.Error("the metadata names a registration endpoint while registration is off")
	}
	for _, bad := range []string{"https://privo.example/", "privo.example", "https://privo.example?x=1", ""} {
		o := e.opts
		o.Issuer = bad
		if _, err := New(context.Background(), o); err == nil {
			t.Errorf("issuer %q was accepted", bad)
		}
	}
}

// ---- dynamic registration (point 13)

func TestRegistrationIsOffByDefault(t *testing.T) {
	e := newEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(`{"redirect_uris":["http://localhost/cb"]}`))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /oauth/register with registration off: %d", rec.Code)
	}
	if e.rows("oauth_clients") != 0 {
		t.Error("a client was registered")
	}
}

func (e *env) registerFrom(addr, body string) (int, map[string]any) {
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
	req.RemoteAddr = addr
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestRegistrationLimitsAndExpiry(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *Clients) { o.Registration = true })
	ctx := context.Background()
	if m := e.srv.Metadata(); m["registration_endpoint"] != issuer+"/oauth/register" {
		t.Errorf("registration endpoint in the metadata: %v", m["registration_endpoint"])
	}
	status, out := e.registerFrom("203.0.113.9:4000", `{"client_name":"Cursor","redirect_uris":["http://127.0.0.1/cb","cursor://x"]}`)
	if status != http.StatusBadRequest || out["error"] != "invalid_redirect_uri" {
		t.Errorf("a custom scheme: %d %v", status, out)
	}
	for name, body := range map[string]string{
		"a secret":        `{"redirect_uris":["http://127.0.0.1/cb"],"token_endpoint_auth_method":"client_secret_post"}`,
		"another grant":   `{"redirect_uris":["http://127.0.0.1/cb"],"grant_types":["client_credentials"]}`,
		"implicit":        `{"redirect_uris":["http://127.0.0.1/cb"],"response_types":["token"]}`,
		"not json":        `client_name=x`,
		"no redirects":    `{"client_name":"x"}`,
		"a plain http cb": `{"redirect_uris":["http://attacker.example/cb"]}`,
	} {
		if status, out := e.registerFrom("203.0.113.9:4000", body); status != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, status, out)
		}
	}
	if e.rows("oauth_clients") != 0 {
		t.Fatal("a refused registration left a row")
	}

	status, out = e.registerFrom("203.0.113.9:4000", `{"client_name":"Cursor <script>","redirect_uris":["http://127.0.0.1/cb"],"client_secret":"ignored"}`)
	id, _ := out["client_id"].(string)
	if status != http.StatusCreated || !strings.HasPrefix(id, "privo_client_") || out["token_endpoint_auth_method"] != "none" || out["client_name"] != "Cursor script" {
		t.Fatalf("registration: %d %v", status, out)
	}
	if _, has := out["client_secret"]; has {
		t.Error("a registration was given a secret")
	}
	// It can ask a member, labelled as registered, with its own addresses.
	u := e.member("ada", store.RoleMember)
	q := e.query(set("client_id", id), set("redirect_uri", "http://127.0.0.1:5555/cb"))
	req, aerr := e.srv.ParseAuthorize(ctx, q)
	if aerr != nil || req.Client.Kind != store.ClientRegistered || !req.Client.LoopbackOnly() {
		t.Fatalf("a registered client's request: %+v %+v", req, aerr)
	}
	if _, aerr := e.srv.ParseAuthorize(ctx, e.query(set("client_id", id), set("redirect_uri", loopbackCB))); aerr == nil || !aerr.Page {
		t.Error("a registered client used an address it did not register")
	}
	code, _ := e.allow(u, q)
	verifier, _ := pkce("")
	tok := e.post("/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://127.0.0.1:5555/cb"},
		"client_id": {id}, "code_verifier": {verifier}, "resource": {resource}})
	if tok.Status != http.StatusOK || !e.verifies(tok.AccessToken) {
		t.Fatalf("a registered client's exchange: %d %s", tok.Status, tok.Raw)
	}

	// Per address: ten in an hour. Another address is not affected.
	for i := 0; i < registerPerAddress-1; i++ {
		if status, out := e.registerFrom("203.0.113.9:4000", `{"redirect_uris":["http://127.0.0.1/cb"]}`); status != http.StatusCreated {
			t.Fatalf("registration %d from one address: %d %v", i+2, status, out)
		}
	}
	if status, _ := e.registerFrom("203.0.113.9:5000", `{"redirect_uris":["http://127.0.0.1/cb"]}`); status != http.StatusTooManyRequests {
		t.Errorf("the eleventh registration from one address: %d", status)
	}
	if status, _ := e.registerFrom("198.51.100.7:1", `{"redirect_uris":["http://127.0.0.1/cb"]}`); status != http.StatusCreated {
		t.Errorf("another address: %d", status)
	}
	e.clk.advance(registerWindow + time.Minute)
	if status, _ := e.registerFrom("203.0.113.9:4000", `{"redirect_uris":["http://127.0.0.1/cb"]}`); status != http.StatusCreated {
		t.Errorf("the same address an hour later: %d", status)
	}
	// Unused registrations go after an hour; the one a member allowed stays.
	if err := e.srv.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	left, _ := e.st.ListOAuthClients(ctx, e.org)
	if len(left) != 2 {
		t.Errorf("%d registrations after the prune, want the used one and the newest", len(left))
	}
	if list, _ := e.st.ListClientGrants(ctx, e.org, u.ID, e.clk.now()); len(list) != 1 || list[0].ClientKind != store.ClientRegistered {
		t.Errorf("the used registration's grant after the prune: %+v", list)
	}
	// In total: the bound on registered clients. Past it, the oldest
	// registration nobody allowed makes room (review of 2026-11-15, S-01), so
	// the table stays bounded and a new client is not kept out.
	for i := 0; i < maxRegistered+5; i++ {
		status, _ := e.registerFrom(fmt.Sprintf("192.0.2.%d:1", i%250), `{"redirect_uris":["http://127.0.0.1/cb"]}`)
		if status != http.StatusCreated {
			t.Fatalf("registration %d: %d", i, status)
		}
		if i%registerPerAddress == registerPerAddress-1 {
			e.clk.advance(registerWindow + time.Minute) // the per-address limit is not what is tested here
		}
	}
	if n := e.rows("oauth_clients"); n != maxRegistered {
		t.Errorf("%d registered clients, want the bound %d", n, maxRegistered)
	}
}
