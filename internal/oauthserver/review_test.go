package oauthserver

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/store"
)

// Findings of the independent review of 2026-11-15 (S-01 to S-10), each with
// a test that fails without its fix.

// S-02: the grant type is taken exactly as written.
func TestPaddedGrantTypeIsRefused(t *testing.T) {
	e := newEnv(t)
	u := e.member("ada", store.RoleMember)
	code, _ := e.allow(u, e.query())
	for _, gt := range []string{"authorization_code ", " authorization_code", "authorization_code refresh_token", "client_credentials", ""} {
		tok := e.exchange(code, func(f url.Values) { f.Set("grant_type", gt); f.Del("resource") })
		if tok.Status != http.StatusBadRequest || tok.Error != "unsupported_grant_type" || tok.AccessToken != "" {
			t.Errorf("grant_type %q: %d %s", gt, tok.Status, tok.Raw)
		}
	}
	twice := e.exchange(code, func(f url.Values) { f.Add("grant_type", "authorization_code") })
	if twice.Status != http.StatusBadRequest || twice.AccessToken != "" {
		t.Errorf("grant_type given twice: %d %s", twice.Status, twice.Raw)
	}
	// The code is still unused, and its exchange is audited.
	if tok := e.exchange(code); tok.Status != http.StatusOK {
		t.Fatalf("the real exchange: %d %s", tok.Status, tok.Raw)
	}
	if got := strings.Join(e.actions(), " "); !strings.Contains(got, "oauth.token.exchange") {
		t.Errorf("the exchange is not audited: %s", got)
	}
}

// S-03: a used code that comes back after its expiry is unknown; it does not
// end the grant. Within its lifetime it still does (RFC 6749 4.1.2).
func TestAUsedCodeCannotEndTheGrantAfterItExpired(t *testing.T) {
	e := newEnv(t)
	u := e.member("ada", store.RoleMember)
	code, _ := e.allow(u, e.query())
	first := e.exchange(code)
	if first.Status != http.StatusOK {
		t.Fatalf("exchange: %d %s", first.Status, first.Raw)
	}
	// The library stamps the code with the real clock; the test clock started
	// at the second before it.
	e.clk.advance(DefaultCodeTTL + codeReuseGrace + 2*time.Second)
	replay := e.post("/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {e.clientID}, "resource": {resource}})
	if replay.Status != http.StatusBadRequest || replay.AccessToken != "" {
		t.Fatalf("replay: %d %s", replay.Status, replay.Raw)
	}
	if !e.verifies(first.AccessToken) {
		t.Fatalf("a replay of an expired, used code ended the grant: %s", replay.Raw)
	}
	if strings.Contains(strings.Join(e.actions(), " "), "oauth.grant.reuse") {
		t.Error("an expired code was recorded as reuse")
	}
}

// S-04: a client that registered itself is accepted only while registration
// is on, and never under the approved policy.
func TestRegisteredClientNeedsRegistrationOn(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *Clients) { o.Registration = true })
	status, out := e.registerFrom("203.0.113.9:4000", `{"client_name":"Tool","redirect_uris":["http://127.0.0.1/cb"]}`)
	id, _ := out["client_id"].(string)
	if status != http.StatusCreated || id == "" {
		t.Fatalf("registration: %d %v", status, out)
	}
	q := e.query(set("client_id", id), set("redirect_uri", "http://127.0.0.1/cb"))
	if _, aerr := e.srv.ParseAuthorize(context.Background(), q); aerr != nil {
		t.Fatalf("while registration is on: %+v", aerr)
	}
	e.clients.Policy = PolicyApproved
	if _, aerr := e.srv.ParseAuthorize(context.Background(), q); aerr == nil || !aerr.Page {
		t.Error("a registered client was accepted under the approved policy")
	}
	e.clients.Policy = PolicyKnown
	e.opts.Registration = false
	e.restart()
	if _, aerr := e.srv.ParseAuthorize(context.Background(), q); aerr == nil || !aerr.Page {
		t.Error("a registered client was accepted after registration was switched off")
	}
}

// S-01: behind trusted proxies the limit counts the client, IPv6 by /64, and
// registrations nobody allowed cannot keep a new client out.
func TestRegistrationLimitsCountTheClientBehindProxies(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *Clients) {
		o.Registration = true
		o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	})
	reg := func(xff string) int {
		req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(`{"redirect_uris":["http://127.0.0.1/cb"]}`))
		req.RemoteAddr = "10.0.0.2:4000" // the proxy
		req.Header.Set("X-Forwarded-For", xff)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < registerPerAddress; i++ {
		if c := reg("198.51.100.7"); c != http.StatusCreated {
			t.Fatalf("registration %d: %d", i, c)
		}
	}
	if c := reg("198.51.100.7"); c != http.StatusTooManyRequests {
		t.Fatalf("the eleventh from one client: %d", c)
	}
	// Another client behind the same proxy is not affected; a forged left
	// part of the header does not help the first.
	if c := reg("198.51.100.8"); c != http.StatusCreated {
		t.Fatalf("another client behind the proxy: %d", c)
	}
	if c := reg("1.2.3.4, 198.51.100.7"); c != http.StatusTooManyRequests {
		t.Fatalf("a forged hop: %d", c)
	}
	// One IPv6 /64 is one client.
	for i := 0; i < registerPerAddress; i++ {
		if c := reg(fmt.Sprintf("2001:db8:1:2::%x", i+1)); c != http.StatusCreated {
			t.Fatalf("IPv6 registration %d: %d", i, c)
		}
	}
	if c := reg("2001:db8:1:2::ffff"); c != http.StatusTooManyRequests {
		t.Fatalf("an eleventh address in the same /64: %d", c)
	}
}

func TestRegistrationsNobodyAllowedMakeRoom(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *Clients) { o.Registration = true })
	for i := 0; i < maxRegistered; i++ {
		// A different /64 each time, as from a large IPv6 range.
		if status, out := e.registerFrom(fmt.Sprintf("[2001:db8:%x::1]:4000", i), `{"redirect_uris":["http://127.0.0.1/cb"]}`); status != http.StatusCreated {
			t.Fatalf("registration %d: %d %v", i, status, out)
		}
	}
	status, out := e.registerFrom("203.0.113.9:4000", `{"client_name":"Real","redirect_uris":["http://127.0.0.1/cb"]}`)
	id, _ := out["client_id"].(string)
	if status != http.StatusCreated || id == "" {
		t.Fatalf("a real client after a flood: %d %v", status, out)
	}
	if n := e.rows("oauth_clients"); n != maxRegistered {
		t.Errorf("%d registrations kept, want the bound %d", n, maxRegistered)
	}
	if _, err := e.st.OAuthClient(context.Background(), e.org, id); err != nil {
		t.Errorf("the real client is gone: %v", err)
	}
}

// S-05: every fetch failure reads the same, and fetches of documents that are
// not cached are bounded.
func TestDocumentFetchFailuresReadTheSameAndAreBounded(t *testing.T) {
	e := newEnv(t, func(_ *Options, c *Clients) { c.Policy = PolicyAny; c.FetchesPerMinute = 3 })
	e.docs.raw("/html.json", "<html>")
	reasons := map[string]bool{}
	for _, path := range []string{"/missing.json", "/html.json"} {
		_, aerr := e.srv.ParseAuthorize(context.Background(), e.query(set("client_id", e.docs.srv.URL+path), set("redirect_uri", loopbackCB)))
		if aerr == nil || !aerr.Page {
			t.Fatalf("%s accepted", path)
		}
		reasons[aerr.Detail] = true
	}
	if len(reasons) != 1 {
		t.Errorf("fetch failures read differently, which tells what answered: %v", reasons)
	}
	// The third fetch is the last one this minute; the fourth is refused
	// without a request.
	before := e.docs.count("/x4.json")
	_, _ = e.srv.ParseAuthorize(context.Background(), e.query(set("client_id", e.docs.srv.URL+"/x3.json")))
	_, aerr := e.srv.ParseAuthorize(context.Background(), e.query(set("client_id", e.docs.srv.URL+"/x4.json")))
	if aerr == nil || !strings.Contains(aerr.Detail, "Too many") || e.docs.count("/x4.json") != before {
		t.Fatalf("over the bound: %+v, fetched %d times", aerr, e.docs.count("/x4.json")-before)
	}
	e.clk.advance(time.Minute)
	if _, aerr := e.srv.ParseAuthorize(context.Background(), e.query(set("client_id", e.clientID))); aerr != nil {
		t.Fatalf("the next minute: %+v", aerr)
	}
}

// S-08: no response_mode is offered; every one is refused as a page.
func TestResponseModesAreRefusedAsAPage(t *testing.T) {
	e := newEnv(t)
	for _, m := range []string{"jwt", "query.jwt", "form_post", "form_post.jwt", "fragment", "query", ""} {
		_, aerr := e.srv.ParseAuthorize(context.Background(), e.query(set("response_mode", m)))
		if aerr == nil || !aerr.Page || aerr.Status != http.StatusBadRequest {
			t.Errorf("response_mode %s: %+v", m, aerr)
		}
	}
	if _, aerr := e.srv.ParseAuthorize(context.Background(), e.query()); aerr != nil {
		t.Errorf("without response_mode: %+v", aerr)
	}
}

// S-09: a redirect host is a DNS name or an IP literal.
func TestRedirectHostsAreNamesOrAddresses(t *testing.T) {
	for _, bad := range []string{"https://evil.com;sandbox/cb", "https://a$b.example/cb", "https://a'b.example/cb", "https://-a.example/cb", "https://a.example:80x/cb"} {
		if err := checkRedirects([]string{bad}); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	for _, good := range []string{"https://app.example/cb", "https://APP.example.com:8443/cb", "http://127.0.0.1/cb", "http://[::1]:3000/cb", "http://localhost/cb"} {
		if err := checkRedirects([]string{good}); err != nil {
			t.Errorf("%s refused: %v", good, err)
		}
	}
}

// S-10, M13: a client id with user info is not a document address.
func TestClientIDWithUserInfoIsRefused(t *testing.T) {
	e := newEnv(t, func(_ *Options, c *Clients) { c.Policy = PolicyAny })
	u, _ := url.Parse(e.docs.srv.URL + "/known.json")
	u.User = url.User("x")
	_, aerr := e.srv.ParseAuthorize(context.Background(), e.query(set("client_id", u.String())))
	// Refused for its address, before anything is fetched.
	if aerr == nil || !aerr.Page || !strings.Contains(aerr.Detail, "not registered with this Privo") {
		t.Fatalf("a client id with user info: %+v", aerr)
	}
}

// S-10, M1 and M6: an access token whose stored session names another member,
// or another resource, is refused even though Privo never writes one.
func TestVerifyChecksTheStoredSubjectAndResource(t *testing.T) {
	for name, edit := range map[string]func(e *env, u store.User) (string, string){
		"another member":   func(_ *env, u store.User) (string, string) { return `"` + u.ID + `"`, `"u-someone-else"` },
		"another resource": func(_ *env, _ store.User) (string, string) { return resource, issuer + "/other" },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			u := e.member("ada", store.RoleMember)
			tok := e.signedIn(u)
			from, to := edit(e, u)
			rw, err := sql.Open("sqlite", "file:"+filepath.Join(e.dir, "privo.db")+"?_pragma=busy_timeout(5000)")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rw.Close() }()
			res, err := rw.Exec(`UPDATE oauth_tokens SET request = replace(request, ?, ?) WHERE kind = ? AND instr(request, ?) > 0`, from, to, store.OAuthAccess, from)
			if err != nil {
				t.Fatalf("edit the stored session: %v", err)
			}
			if n, _ := res.RowsAffected(); n != 1 {
				t.Fatalf("edit the stored session: %d rows", n)
			}
			if _, _, _, err := e.srv.VerifyAccessToken(context.Background(), tok.AccessToken); err == nil {
				t.Fatal("an access token whose stored session does not match its grant was accepted")
			}
		})
	}
}
