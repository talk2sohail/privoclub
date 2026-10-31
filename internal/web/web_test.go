package web

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/store"
)

func location(p page) string { return p.Header.Get("Location") }

func sessionCookieName(e *env) string { return e.srv.sessionCookie }

// ---- not configured

func TestDisabledWithoutConfiguration(t *testing.T) {
	srv := New(Options{Missing: []string{"PRIVO_GITHUB_CLIENT_ID", "PRIVO_PUBLIC_URL"}})
	mux := http.NewServeMux()
	srv.Routes(mux)
	b := &browser{t: t, h: mux, pub: "http://localhost", cookies: map[string]*http.Cookie{}}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/admin/signin"}, {"GET", "/admin/approvals"}, {"GET", "/admin/setup/privo_setup_x"}, {"POST", "/auth/github/start"},
		{"GET", "/auth/github/callback?code=a&state=b"}, {"POST", "/admin/signout"}, {"GET", "/admin/"},
	} {
		var form url.Values
		if tc.method == "POST" {
			form = url.Values{}
		}
		res := b.do(tc.method, tc.path, form, nil)
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status %d, want 503", tc.method, tc.path, res.StatusCode)
		}
		if !strings.Contains(res.Body, "PRIVO_GITHUB_CLIENT_ID") || !strings.Contains(res.Body, "PRIVO_PUBLIC_URL") {
			t.Errorf("%s %s: the page does not name the missing settings", tc.method, tc.path)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s %s: Cache-Control %q", tc.method, tc.path, res.Header.Get("Cache-Control"))
		}
	}
}

// ---- setup link

func TestSetupTokenFormat(t *testing.T) {
	plain, hash, err := NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := NewSetupToken()
	if plain == other {
		t.Error("two tokens are equal")
	}
	if !strings.HasPrefix(plain, "privo_setup_") || len(plain) != len("privo_setup_")+43 {
		t.Errorf("token %q", plain)
	}
	sum := sha256.Sum256([]byte(plain))
	if hash != hex.EncodeToString(sum[:]) || HashSecret(plain) != hash {
		t.Errorf("hash %q is not the hex SHA-256 of the token", hash)
	}
	if !validSetupToken(plain) || validSetupToken("") || validSetupToken(plain+"x") || validSetupToken("privo_setup_<script>") {
		t.Error("validSetupToken is wrong")
	}
}

func TestSetupFlowCreatesOwner(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	token := e.setupLink("")
	res := e.setupAs(b, token, octo)
	if res.StatusCode != http.StatusSeeOther || location(res) != "/admin/approvals" {
		t.Fatalf("callback: %d %s\n%s", res.StatusCode, location(res), res.Body)
	}
	if b.cookie(sessionCookieName(e)) == nil {
		t.Fatal("no session cookie after setup")
	}
	u, err := e.st.UserByIdentity(context.Background(), e.org, store.ProviderGitHub, "1001")
	if err != nil || u.Role != store.RoleOwner || u.Person != "octocat" || u.DisplayName != "The Octocat" {
		t.Fatalf("user %+v, %v", u, err)
	}
	page := b.get("/admin/approvals")
	if page.StatusCode != http.StatusOK || !strings.Contains(page.Body, "octocat") {
		t.Fatalf("approvals page: %d\n%s", page.StatusCode, page.Body)
	}
	if !contains(e.adminActions(), "setup.claim") {
		t.Errorf("no setup.claim audit event: %v", e.adminActions())
	}
	// The link is spent: every way of using it again looks the same as an unknown one.
	again := e.browser().get("/admin/setup/" + token)
	unknown := e.browser().get("/admin/setup/" + func() string { p, _, _ := NewSetupToken(); return p }())
	if again.StatusCode != http.StatusNotFound || unknown.StatusCode != http.StatusNotFound || again.Body != unknown.Body {
		t.Errorf("spent link: %d, unknown link: %d (bodies equal: %v)", again.StatusCode, unknown.StatusCode, again.Body == unknown.Body)
	}
}

func TestSetupLinkPersonOverridesLogin(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	res := e.setupAs(b, e.setupLink("alice"), octo)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("%d %s", res.StatusCode, res.Body)
	}
	u, _ := e.st.UserByIdentity(context.Background(), e.org, store.ProviderGitHub, "1001")
	if u.Person != "alice" {
		t.Errorf("person = %q, want alice", u.Person)
	}
}

func TestSetupFailedExchangeKeepsLink(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	token := e.setupLink("")
	e.gh.failExchange = true
	res := e.setupAs(b, token, octo)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a refused code: status %d\n%s", res.StatusCode, res.Body)
	}
	if b.cookie(sessionCookieName(e)) != nil {
		t.Error("a session was started although GitHub refused")
	}
	if _, err := e.st.UserByIdentity(context.Background(), e.org, store.ProviderGitHub, "1001"); err == nil {
		t.Error("a user was created although GitHub refused")
	}
	if _, err := e.st.SetupLink(context.Background(), e.org, HashSecret(token), e.clock.now()); err != nil {
		t.Errorf("the link was consumed by a failed sign-in: %v", err)
	}
	e.gh.failExchange = false
	if res := e.setupAs(e.browser(), token, octo); res.StatusCode != http.StatusSeeOther {
		t.Errorf("retry after the failure: %d\n%s", res.StatusCode, res.Body)
	}
}

func TestSetupRefusedForExistingMemberKeepsLink(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	token := e.setupLink("")
	res := e.setupAs(e.browser(), token, octo)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status %d\n%s", res.StatusCode, res.Body)
	}
	if _, err := e.st.SetupLink(context.Background(), e.org, HashSecret(token), e.clock.now()); err != nil {
		t.Errorf("the link was consumed: %v", err)
	}
}

func TestSetupPersonTakenKeepsLink(t *testing.T) {
	e := newEnv(t)
	e.member(ghUser{ID: 7, Login: "someone"}, "octocat", store.RoleOwner) // the person name is taken
	token := e.setupLink("")
	res := e.setupAs(e.browser(), token, octo)
	if res.StatusCode != http.StatusConflict || !strings.Contains(res.Body, "--person") {
		t.Fatalf("status %d\n%s", res.StatusCode, res.Body)
	}
	if _, err := e.st.SetupLink(context.Background(), e.org, HashSecret(token), e.clock.now()); err != nil {
		t.Errorf("the link was consumed: %v", err)
	}
}

func TestStartWithBadSetupToken(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	for name, tok := range map[string]string{"unknown": "privo_setup_" + strings.Repeat("A", 43), "malformed": "<script>", "empty-but-present": " "} {
		res := b.post("/auth/github/start", url.Values{"setup_token": {tok}})
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", name, res.StatusCode)
		}
	}
	if b.cookie(e.srv.stateCookie) != nil {
		t.Error("a state cookie was set for a bad setup token")
	}
}

// ---- sign-in

func TestSignInPage(t *testing.T) {
	e := newEnv(t)
	res := e.browser().get("/admin/signin")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, `action="/auth/github/start"`) || !strings.Contains(res.Body, "GitHub") {
		t.Fatalf("%d\n%s", res.StatusCode, res.Body)
	}
	if loc := location(e.browser().get("/admin/")); loc != "/admin/signin" {
		t.Errorf("/admin/ for a stranger redirects to %q, want the sign-in page", loc)
	}
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	e.signIn(b, octo)
	if loc := location(b.get("/admin/")); loc != "/admin/approvals" {
		t.Errorf("/admin/ for a member redirects to %q", loc)
	}
	if loc := location(b.get("/admin/signin")); loc != "/admin/approvals" {
		t.Errorf("/admin/signin for a member redirects to %q", loc)
	}
}

func TestSignInOfAMember(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleApprover)
	b := e.browser()
	res := e.signIn(b, octo)
	if res.StatusCode != http.StatusSeeOther || location(res) != "/admin/approvals" {
		t.Fatalf("%d %s\n%s", res.StatusCode, location(res), res.Body)
	}
	if page := b.get("/admin/approvals"); page.StatusCode != http.StatusOK {
		t.Errorf("approvals: %d", page.StatusCode)
	}
	if !contains(e.adminActions(), "signin") {
		t.Errorf("no signin audit event: %v", e.adminActions())
	}
	if e.gh.exchanges != 1 || e.gh.profiles != 1 {
		t.Errorf("GitHub was called %d/%d times, want once each", e.gh.exchanges, e.gh.profiles)
	}
	// A login is not the identity: another GitHub account with the same login is a stranger.
	imposter := ghUser{ID: 9999, Login: octo.Login}
	deny := e.signIn(e.browser(), imposter)
	if deny.StatusCode != http.StatusForbidden {
		t.Errorf("same login, other id: status %d, want 403", deny.StatusCode)
	}
}

func TestStateCookieRequired(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	victim := e.browser()
	attacker := e.browser()
	start := attacker.post("/auth/github/start", nil)
	code, state := e.gh.authorize(location(start), octo)
	cb := "/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)

	// The victim's browser never started this sign-in: no cookie.
	if res := victim.get(cb); res.StatusCode != http.StatusBadRequest {
		t.Errorf("no state cookie: status %d, want 400", res.StatusCode)
	}
	// A cookie from another sign-in does not match either.
	other := e.browser()
	other.post("/auth/github/start", nil)
	if res := other.get(cb); res.StatusCode != http.StatusBadRequest {
		t.Errorf("another sign-in's cookie: status %d, want 400", res.StatusCode)
	}
	if e.gh.exchanges != 0 {
		t.Errorf("GitHub was asked to exchange the code %d times although the browser was not the one that started", e.gh.exchanges)
	}
	// A state parameter that was not issued never matches (and the real one was
	// not burned by the failures above: it is keyed to the cookie too).
	forged := "/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state+"x")
	if res := attacker.get(forged); res.StatusCode != http.StatusBadRequest {
		t.Errorf("forged state: status %d, want 400", res.StatusCode)
	}
}

func TestStateReplayRefused(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	start := b.post("/auth/github/start", nil)
	code, state := e.gh.authorize(location(start), octo)
	cb := "/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
	stateCookie := b.cookie(e.srv.stateCookie)

	if res := b.get(cb); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("first use: %d\n%s", res.StatusCode, res.Body)
	}
	if b.cookie(e.srv.stateCookie) != nil {
		t.Error("the state cookie is still set after use")
	}
	// Replay with the cookie put back (a captured callback URL and cookie).
	b.cookies[e.srv.stateCookie] = stateCookie
	if res := b.get(cb); res.StatusCode != http.StatusBadRequest {
		t.Errorf("replay: status %d, want 400", res.StatusCode)
	}
	if e.gh.exchanges != 1 {
		t.Errorf("%d exchanges, want 1", e.gh.exchanges)
	}
}

func TestStateExpired(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	start := b.post("/auth/github/start", nil)
	code, state := e.gh.authorize(location(start), octo)
	e.clock.advance(11 * time.Minute)
	if res := b.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)); res.StatusCode != http.StatusBadRequest {
		t.Errorf("expired state: status %d, want 400", res.StatusCode)
	}
	if e.gh.exchanges != 0 {
		t.Error("GitHub was asked to exchange a code for an expired sign-in")
	}
}

func TestCallbackErrorsAndBounds(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	// The person pressed Cancel at GitHub.
	b := e.browser()
	start := b.post("/auth/github/start", nil)
	_, state := e.gh.authorize(location(start), octo)
	res := b.get("/auth/github/callback?error=access_denied&state=" + url.QueryEscape(state))
	if res.StatusCode != http.StatusBadRequest || b.cookie(sessionCookieName(e)) != nil {
		t.Errorf("cancelled: %d", res.StatusCode)
	}
	// An absurd code is refused before GitHub hears about it.
	b2 := e.browser()
	start2 := b2.post("/auth/github/start", nil)
	_, state2 := e.gh.authorize(location(start2), octo)
	res2 := b2.get("/auth/github/callback?code=" + strings.Repeat("a", 5000) + "&state=" + url.QueryEscape(state2))
	if res2.StatusCode != http.StatusBadRequest || e.gh.exchanges != 0 {
		t.Errorf("long code: status %d, exchanges %d", res2.StatusCode, e.gh.exchanges)
	}
	// No parameters at all.
	if r := e.browser().get("/auth/github/callback"); r.StatusCode != http.StatusBadRequest {
		t.Errorf("bare callback: %d", r.StatusCode)
	}
}

func TestUnknownAccountGenericDenial(t *testing.T) {
	e := newEnv(t)
	member := e.member(octo, "octocat", store.RoleOwner)
	stranger := ghUser{ID: 4242, Login: "stranger-login", Name: "Mallory"}

	b := e.browser()
	res := e.signIn(b, stranger)
	if res.StatusCode != http.StatusForbidden || b.cookie(sessionCookieName(e)) != nil {
		t.Fatalf("stranger: status %d\n%s", res.StatusCode, res.Body)
	}
	if strings.Contains(res.Body, "stranger-login") || strings.Contains(res.Body, "Mallory") {
		t.Error("the denial page repeats what GitHub said about the stranger")
	}
	if !contains(e.adminActions(), "signin.denied") {
		t.Errorf("no signin.denied audit event: %v", e.adminActions())
	}
	evs, _ := e.st.RecentAdminEvents(context.Background(), e.org, 50)
	for _, ev := range evs {
		if ev.Action == "signin.denied" && (ev.ActorID != "github:4242" || ev.Outcome != "denied") {
			t.Errorf("denied event %+v", ev)
		}
	}

	// A disabled member gets exactly the same answer.
	if err := e.st.DisableUser(context.Background(), e.org, member.ID, e.clock.now(), e.event("disable")); err != nil {
		t.Fatal(err)
	}
	b2 := e.browser()
	res2 := e.signIn(b2, octo)
	if res2.StatusCode != http.StatusForbidden || b2.cookie(sessionCookieName(e)) != nil {
		t.Fatalf("disabled: status %d", res2.StatusCode)
	}
	if res2.Body != res.Body {
		t.Error("the page for a disabled member differs from the page for a stranger")
	}
}

func TestExchangeFailureAndGitHubDown(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	e.gh.failExchange = true
	res := e.signIn(e.browser(), octo)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("refused code: %d", res.StatusCode)
	}
	if strings.Contains(res.Body, "bad_verification_code") || strings.Contains(res.Body, "incorrect or expired") {
		t.Error("GitHub's words reached the page")
	}
	e.gh.failExchange = false
	b := e.browser()
	start := b.post("/auth/github/start", nil)
	code, state := e.gh.authorize(location(start), octo)
	e.gh.srv.Close() // GitHub goes away
	down := b.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
	if down.StatusCode != http.StatusBadGateway {
		t.Errorf("GitHub unreachable: status %d, want 502", down.StatusCode)
	}
}

func TestStartCapReturns503(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.StateCap = 3 })
	b := e.browser()
	for i := 0; i < 3; i++ {
		if res := b.post("/auth/github/start", nil); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("start %d: %d", i, res.StatusCode)
		}
	}
	if res := b.post("/auth/github/start", nil); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("start over the cap: %d, want 503", res.StatusCode)
	}
	e.clock.advance(11 * time.Minute)
	if res := b.post("/auth/github/start", nil); res.StatusCode != http.StatusSeeOther {
		t.Errorf("start after they expired: %d", res.StatusCode)
	}
}

// ---- cookies and sessions

func TestCookieFlagsHTTPS(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	start := b.post("/auth/github/start", nil)
	var stateC *http.Cookie
	for _, c := range start.Cookies() {
		stateC = c
	}
	if stateC == nil || stateC.Name != "__Host-privo_oauth" {
		t.Fatalf("state cookie %+v", stateC)
	}
	code, state := e.gh.authorize(location(start), octo)
	cb := b.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
	var sess *http.Cookie
	var cleared *http.Cookie
	for _, c := range cb.Cookies() {
		switch c.Name {
		case "__Host-privo_session":
			sess = c
		case "__Host-privo_oauth":
			cleared = c
		}
	}
	if sess == nil || cleared == nil {
		t.Fatalf("cookies: %v", cb.Cookies())
	}
	for _, c := range []*http.Cookie{stateC, sess} {
		if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
			t.Errorf("%s: Secure=%v HttpOnly=%v SameSite=%v Path=%q Domain=%q", c.Name, c.Secure, c.HttpOnly, c.SameSite, c.Path, c.Domain)
		}
	}
	if stateC.MaxAge <= 0 || stateC.MaxAge > 600 {
		t.Errorf("state cookie MaxAge %d, want 1 to 600", stateC.MaxAge)
	}
	if sess.MaxAge != int((12 * time.Hour).Seconds()) {
		t.Errorf("session cookie MaxAge %d, want the absolute lifetime", sess.MaxAge)
	}
	if cleared.MaxAge >= 0 {
		t.Errorf("the state cookie is not cleared at the callback: MaxAge %d", cleared.MaxAge)
	}
}

func TestCookieFlagsLoopback(t *testing.T) {
	pu, _ := url.Parse("http://127.0.0.1:8080")
	e := newEnv(t, func(o *Options) { o.PublicURL = pu })
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	start := b.post("/auth/github/start", nil)
	code, state := e.gh.authorize(location(start), octo)
	cb := b.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
	var sess *http.Cookie
	for _, c := range cb.Cookies() {
		if c.Name == "privo_session" {
			sess = c
		}
	}
	if sess == nil {
		t.Fatalf("cookies: %v", cb.Cookies())
	}
	if sess.Secure || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode || sess.Path != "/" {
		t.Errorf("loopback cookie: %+v", sess)
	}
	for _, c := range start.Cookies() {
		if strings.HasPrefix(c.Name, "__Host-") {
			t.Errorf("a __Host- cookie over plain http: %s", c.Name)
		}
	}
}

func TestSessionRotatedOnLogin(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	// A planted cookie (session fixation) is neither honoured nor kept.
	b.cookies[sessionCookieName(e)] = &http.Cookie{Name: sessionCookieName(e), Value: "attacker-chosen-value"}
	if res := b.get("/admin/approvals"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("planted cookie accepted: %d", res.StatusCode)
	}
	e.signIn(b, octo)
	first := b.cookie(sessionCookieName(e))
	if first == nil || first.Value == "attacker-chosen-value" {
		t.Fatalf("session cookie after login: %+v", first)
	}
	e.signIn(b, octo)
	second := b.cookie(sessionCookieName(e))
	if second.Value == first.Value {
		t.Error("two logins produced the same session secret")
	}
	if len(first.Value) < 43 {
		t.Errorf("session secret %q is shorter than 256 bits", first.Value)
	}
}

func TestIdleAndAbsoluteExpiry(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.IdleTimeout, o.MaxAge = 30*time.Minute, 2*time.Hour })
	e.member(octo, "octocat", store.RoleOwner)

	idle := e.browser()
	e.signIn(idle, octo)
	e.clock.advance(31 * time.Minute)
	if res := idle.get("/admin/approvals"); res.StatusCode != http.StatusSeeOther || location(res) != "/admin/signin" {
		t.Errorf("idle session: %d %s", res.StatusCode, location(res))
	}

	busy := e.browser()
	e.signIn(busy, octo)
	for i := 0; i < 5; i++ { // 5 x 20 minutes: active all the time
		e.clock.advance(20 * time.Minute)
		if res := busy.get("/admin/approvals"); res.StatusCode != http.StatusOK {
			t.Fatalf("active session dropped after %d minutes: %d", (i+1)*20, res.StatusCode)
		}
	}
	e.clock.advance(20 * time.Minute) // 2h in total: the absolute lifetime
	if res := busy.get("/admin/approvals"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("session past its absolute lifetime: %d", res.StatusCode)
	}
}

func TestRoleChangeAppliesAtOnce(t *testing.T) {
	e := newEnv(t)
	u := e.member(octo, "octocat", store.RoleApprover)
	b := e.browser()
	e.signIn(b, octo)
	if res := b.get("/admin/approvals"); res.StatusCode != http.StatusOK {
		t.Fatalf("approver: %d", res.StatusCode)
	}
	ctx := context.Background()
	if err := e.st.SetUserRole(ctx, e.org, u.ID, store.RoleMember, e.event("role")); err != nil {
		t.Fatal(err)
	}
	if res := b.get("/admin/approvals"); res.StatusCode != http.StatusForbidden {
		t.Errorf("after demotion: %d, want 403 on the same cookie", res.StatusCode)
	}
	if err := e.st.SetUserRole(ctx, e.org, u.ID, store.RoleAdmin, e.event("role2")); err != nil {
		t.Fatal(err)
	}
	if res := b.get("/admin/approvals"); res.StatusCode != http.StatusOK {
		t.Errorf("after promotion: %d", res.StatusCode)
	}
	if err := e.st.DisableUser(ctx, e.org, u.ID, e.clock.now(), e.event("disable")); err != nil {
		t.Fatal(err)
	}
	if res := b.get("/admin/approvals"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("after disabling: %d, want a redirect to sign-in", res.StatusCode)
	}
}

func TestSignOutDeletesSession(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	e.signIn(b, octo)
	old := *b.cookie(sessionCookieName(e))
	token := csrfOf(t, b.get("/admin/approvals").Body)

	if res := b.post("/admin/signout", nil); res.StatusCode != http.StatusForbidden {
		t.Errorf("sign-out without csrf: %d, want 403", res.StatusCode)
	}
	if res := b.get("/admin/approvals"); res.StatusCode != http.StatusOK {
		t.Fatal("a refused sign-out ended the session")
	}
	res := b.post("/admin/signout", url.Values{"csrf": {token}})
	if res.StatusCode != http.StatusSeeOther || location(res) != "/admin/signin" {
		t.Fatalf("sign-out: %d %s", res.StatusCode, location(res))
	}
	if b.cookie(sessionCookieName(e)) != nil {
		t.Error("the session cookie was not cleared")
	}
	// The old cookie is dead server-side too.
	b.cookies[old.Name] = &old
	if res := b.get("/admin/approvals"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("old cookie after sign-out: %d", res.StatusCode)
	}
	if !contains(e.adminActions(), "signout") {
		t.Errorf("no signout audit event: %v", e.adminActions())
	}
}

// ---- CSRF and origin

func TestCSRFMissingWrongCrossOrigin(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	e.signIn(b, octo)
	id := e.pending("ada", "delete_repo", `{"repo":"sandbox"}`)
	good := csrfOf(t, b.get("/admin/approvals").Body)
	path := "/admin/approvals/" + id + "/approve"

	refused := map[string]page{
		"no csrf":                      b.post(path, nil),
		"empty csrf":                   b.post(path, url.Values{"csrf": {""}}),
		"wrong csrf":                   b.post(path, url.Values{"csrf": {strings.Repeat("a", len(good))}}),
		"csrf in url":                  b.do("POST", path+"?csrf="+good, url.Values{}, nil),
		"other origin":                 b.do("POST", path, url.Values{"csrf": {good}}, map[string]string{"Origin": "https://evil.example"}),
		"null origin":                  b.do("POST", path, url.Values{"csrf": {good}}, map[string]string{"Origin": "null"}),
		"http origin":                  b.do("POST", path, url.Values{"csrf": {good}}, map[string]string{"Origin": "http://app.example.com"}),
		"no origin, no fetch metadata": b.do("POST", path, url.Values{"csrf": {good}}, map[string]string{"Origin": ""}),
		"cross-site fetch metadata":    b.do("POST", path, url.Values{"csrf": {good}}, map[string]string{"Origin": "", "Sec-Fetch-Site": "cross-site"}),
		"same-site fetch metadata":     b.do("POST", path, url.Values{"csrf": {good}}, map[string]string{"Origin": "", "Sec-Fetch-Site": "same-site"}),
	}
	for name, res := range refused {
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, res.StatusCode)
		}
	}
	if st := e.status(id); st != store.ApprovalPending {
		t.Fatalf("a refused request changed the approval: %s", st)
	}
	// Same-origin fetch metadata without Origin is how some browsers send it.
	ok := b.do("POST", path, url.Values{"csrf": {good}}, map[string]string{"Origin": "", "Sec-Fetch-Site": "same-origin"})
	if ok.StatusCode != http.StatusSeeOther || e.status(id) != store.ApprovalApproved {
		t.Errorf("same-origin fetch metadata: %d, status %s", ok.StatusCode, e.status(id))
	}
}

func TestOriginComesFromThePublicURLNotTheHost(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	e.signIn(b, octo)
	id := e.pending("ada", "delete_repo", `{}`)
	good := csrfOf(t, b.get("/admin/approvals").Body)

	// A request whose Host and Origin agree with each other but not with the
	// configured public URL is refused.
	b.host = "evil.example"
	res := b.do("POST", "/admin/approvals/"+id+"/approve", url.Values{"csrf": {good}}, map[string]string{"Origin": "https://evil.example"})
	if res.StatusCode != http.StatusForbidden || e.status(id) != store.ApprovalPending {
		t.Errorf("origin that matches the Host header: %d", res.StatusCode)
	}
	// The redirect URI never follows the Host header.
	anon := e.browser()
	anon.host = "evil.example"
	start := anon.post("/auth/github/start", nil)
	loc, _ := url.Parse(location(start))
	if got := loc.Query().Get("redirect_uri"); got != "https://app.example.com/auth/github/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
}

func TestSignInStartNeedsASameOriginPost(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	if res := b.do("POST", "/auth/github/start", url.Values{}, map[string]string{"Origin": "https://evil.example"}); res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin start: %d, want 403", res.StatusCode)
	}
	if res := b.do("POST", "/auth/github/start", url.Values{}, map[string]string{"Origin": ""}); res.StatusCode != http.StatusForbidden {
		t.Errorf("start without origin: %d, want 403", res.StatusCode)
	}
	if b.cookie(e.srv.stateCookie) != nil {
		t.Error("a refused start still set a state cookie")
	}
	if res := b.get("/auth/github/start"); res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET start: %d, want 405", res.StatusCode)
	}
}

func TestWrongMethodsAreRefused(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	e.signIn(b, octo)
	id := e.pending("ada", "t", `{}`)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/admin/approvals/" + id + "/approve"}, {"GET", "/admin/approvals/" + id + "/deny"}, {"GET", "/admin/signout"},
		{"POST", "/auth/github/callback"}, {"POST", "/admin/approvals"}, {"DELETE", "/admin/approvals"},
	} {
		var form url.Values
		if tc.method == "POST" {
			form = url.Values{"csrf": {"x"}}
		}
		if res := b.do(tc.method, tc.path, form, nil); res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d, want 405", tc.method, tc.path, res.StatusCode)
		}
	}
	if e.status(id) != store.ApprovalPending {
		t.Error("a wrong-method request changed the approval")
	}
}

// ---- the approvals page

func TestMemberCannotSeeOrDecideApprovals(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleMember)
	b := e.browser()
	e.signIn(b, octo)
	id := e.pending("ada", "delete_repo", `{"repo":"secret-repo"}`)
	res := b.get("/admin/approvals")
	if res.StatusCode != http.StatusForbidden || strings.Contains(res.Body, "secret-repo") || strings.Contains(res.Body, "delete_repo") {
		t.Fatalf("member saw approvals: %d\n%s", res.StatusCode, res.Body)
	}
	// Even with a valid CSRF token (it belongs to the session) a member cannot decide.
	csrf := csrfOf(t, b.get("/admin/approvals").Body) // the 403 page carries the sign-out form
	if res := b.post("/admin/approvals/"+id+"/approve", url.Values{"csrf": {csrf}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("member approve: %d, want 403", res.StatusCode)
	}
	if e.status(id) != store.ApprovalPending {
		t.Error("a member decided an approval")
	}
}

func TestApproveAndDeny(t *testing.T) {
	e := newEnv(t)
	u := e.member(octo, "octocat", store.RoleApprover)
	b := e.browser()
	e.signIn(b, octo)
	a, aReq := e.pendingReq("ada", "delete_repo", `{"repo":"sandbox","force":true}`)
	d := e.pending("grace", "drop_table", `{"table":"users"}`)

	list := b.get("/admin/approvals")
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list: %d", list.StatusCode)
	}
	for _, want := range []string{"delete_repo", "sandbox", "drop_table", "users", "ada", "grace", "needs a human", "/admin/approvals/" + a + "/approve", "/admin/approvals/" + d + "/deny"} {
		if !strings.Contains(list.Body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	csrf := csrfOf(t, list.Body)

	res := b.post("/admin/approvals/"+a+"/approve", url.Values{"csrf": {csrf}})
	if res.StatusCode != http.StatusSeeOther || location(res) != "/admin/approvals?result=approved" {
		t.Fatalf("approve: %d %s", res.StatusCode, location(res))
	}
	got, _ := e.st.GetApproval(context.Background(), e.org, a)
	if got.Status != store.ApprovalApproved || got.DecidedBy != "octocat (user "+u.ID+")" {
		t.Errorf("approval %+v", got)
	}
	// The gateway's retry of the same call finds it approved, consumes it and names the approver.
	out, err := e.approvals.Gate(context.Background(), aReq)
	if err != nil || out.Result != approval.Approved || out.DecidedBy != got.DecidedBy {
		t.Errorf("gateway retry = %+v, %v", out, err)
	}

	res = b.post("/admin/approvals/"+d+"/deny", url.Values{"csrf": {csrf}})
	if res.StatusCode != http.StatusSeeOther || location(res) != "/admin/approvals?result=denied" || e.status(d) != store.ApprovalDenied {
		t.Fatalf("deny: %d %s status %s", res.StatusCode, location(res), e.status(d))
	}
	// Deciding again, or an unknown id, is "gone", not an error page.
	for _, id := range []string{a, d, "no-such-approval"} {
		if r := b.post("/admin/approvals/"+id+"/approve", url.Values{"csrf": {csrf}}); r.StatusCode != http.StatusSeeOther || location(r) != "/admin/approvals?result=gone" {
			t.Errorf("deciding %s again: %d %s", id, r.StatusCode, location(r))
		}
	}
	// What was decided is not listed as pending any more, and its arguments are not shown.
	after := b.get("/admin/approvals?result=approved")
	if strings.Contains(after.Body, "sandbox") || strings.Contains(after.Body, "drop_table") {
		t.Error("decided approvals are still listed")
	}
	actions := e.adminActions()
	for _, want := range []string{"approval.approve:" + a, "approval.deny:" + d, "approvals.view:2"} {
		if !contains(actions, want) {
			t.Errorf("no audit event %q in %v", want, actions)
		}
	}
	evs, _ := e.st.RecentAdminEvents(context.Background(), e.org, 100)
	for _, ev := range evs {
		if ev.Action == "approval.approve:"+a && (ev.Actor != "octocat" || ev.ActorID != "user:"+u.ID) {
			t.Errorf("decision event %+v", ev)
		}
	}
}

func TestSelfApprovalBlocked(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	e.signIn(b, octo)
	id := e.pending("octocat", "delete_repo", `{}`) // the same person's own call
	csrf := csrfOf(t, b.get("/admin/approvals").Body)

	res := b.post("/admin/approvals/"+id+"/approve", url.Values{"csrf": {csrf}})
	if res.StatusCode != http.StatusSeeOther || location(res) != "/admin/approvals?result=self" || e.status(id) != store.ApprovalPending {
		t.Fatalf("self approval: %d %s status %s", res.StatusCode, location(res), e.status(id))
	}
	if page := b.get(location(res)); !strings.Contains(page.Body, "your own") {
		t.Error("the page does not explain the refusal")
	}
	if err := e.st.SetSetting(context.Background(), e.org, "approvals.allow_self", "true"); err != nil {
		t.Fatal(err)
	}
	if res := b.post("/admin/approvals/"+id+"/approve", url.Values{"csrf": {csrf}}); location(res) != "/admin/approvals?result=approved" {
		t.Errorf("with allow_self on: %s", location(res))
	}
}

func TestEscapesWhatOthersControl(t *testing.T) {
	e := newEnv(t)
	evil := ghUser{ID: 31337, Login: "ok-login", Name: `<script>alert("name")</script>`}
	e.member(evil, `x"><b>p`, store.RoleOwner)
	b := e.browser()
	e.signIn(b, evil)
	e.pending(`<img src=x onerror=alert(1)>`, `<svg onload=alert(2)>`, `{"cmd":"</pre><script>alert(3)</script>","k":"<script>alert(4)</script>"}`)
	res := b.get("/admin/approvals?result=<script>alert(5)</script>")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%d\n%s", res.StatusCode, res.Body)
	}
	for _, raw := range []string{"<script", "<img src=x", "<svg onload", "onerror=alert(1)>", "<b>p"} {
		if strings.Contains(res.Body, raw) {
			t.Errorf("the page contains unescaped %q", raw)
		}
	}
	if !strings.Contains(res.Body, "&lt;svg onload=alert(2)&gt;") {
		t.Error("the tool name is not shown (escaped)")
	}
}

func TestLongArgumentsAreShortened(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	e.signIn(b, octo)
	e.pending("ada", "write_file", `{"content":"`+strings.Repeat("A", 60000)+`"}`)
	res := b.get("/admin/approvals")
	if res.StatusCode != http.StatusOK || len(res.Body) > 64<<10 || !strings.Contains(res.Body, "shortened") {
		t.Errorf("status %d, %d bytes, shortened note %v", res.StatusCode, len(res.Body), strings.Contains(res.Body, "shortened"))
	}
	// An approver must see what they allow: with arguments cut short only deny is offered.
	if strings.Contains(res.Body, "/approve") || !strings.Contains(res.Body, "/deny") {
		t.Error("a call whose arguments are shortened can be approved from the page")
	}
}

func TestUnreadableArgumentsCanOnlyBeDenied(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	e.signIn(b, octo)
	id := e.pending("ada", "write_file", `{"path":"/etc/hosts"}`)
	rw, err := sql.Open("sqlite", "file:"+filepath.Join(e.dataDir, "privo.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rw.Close() }()
	if _, err := rw.Exec(`UPDATE approvals SET args = x'00000001deadbeef' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	res := b.get("/admin/approvals")
	if res.StatusCode != http.StatusOK || strings.Contains(res.Body, "/approve") || !strings.Contains(res.Body, "/deny") || !strings.Contains(res.Body, "cannot be shown") {
		t.Errorf("status %d; approve offered or no explanation:\n%s", res.StatusCode, res.Body)
	}
}

func TestPublicURLWithAPathDisablesTheSite(t *testing.T) {
	pu, _ := url.Parse("https://app.example.com/privo")
	e := newEnv(t, func(o *Options) { o.PublicURL = pu })
	b := e.browser()
	b.pub = "https://app.example.com" // the proxy strips the prefix before Privo sees the path
	res := b.get("/admin/signin")
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(res.Body, "PRIVO_PUBLIC_URL") {
		t.Errorf("%d\n%s", res.StatusCode, res.Body)
	}
}

func TestPruneRemovesWhatCannotBeUsed(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	e.signIn(b, octo)
	e.browser().post("/auth/github/start", nil)
	e.setupLink("")
	count := func(table string) (n int) {
		if err := e.db().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("sessions") < 2 || count("oauth_states") != 1 {
		t.Fatalf("setup: %d sessions, %d states", count("sessions"), count("oauth_states"))
	}
	if err := e.srv.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count("sessions") < 2 || count("oauth_states") != 1 {
		t.Errorf("Prune removed something still usable: %d sessions, %d states", count("sessions"), count("oauth_states"))
	}
	e.clock.advance(13 * time.Hour)
	if err := e.srv.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count("sessions") != 0 || count("oauth_states") != 0 {
		t.Errorf("after the lifetime: %d sessions, %d states", count("sessions"), count("oauth_states"))
	}
}

// ---- headers

var cspRe = regexp.MustCompile(`^default-src 'none'; style-src 'self'; form-action 'self' http://127\.0\.0\.1:\d+; frame-ancestors 'none'; base-uri 'none'$`)

func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	anon := e.browser()
	e.signIn(b, octo)
	e.pending("ada", "t", `{}`)
	token := e.setupLink("")

	pages := map[string]page{
		"signin":       anon.get("/admin/signin"),
		"setup":        anon.get("/admin/setup/" + token),
		"setup 404":    anon.get("/admin/setup/privo_setup_" + strings.Repeat("A", 43)),
		"approvals":    b.get("/admin/approvals"),
		"redirect":     anon.get("/admin/approvals"),
		"403":          b.post("/admin/signout", nil),
		"405":          anon.get("/auth/github/start"),
		"bad callback": anon.get("/auth/github/callback"),
		"start":        anon.post("/auth/github/start", nil),
	}
	for name, p := range pages {
		h := p.Header
		if !cspRe.MatchString(h.Get("Content-Security-Policy")) && p.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: Content-Security-Policy %q", name, h.Get("Content-Security-Policy"))
		}
		if p.StatusCode == http.StatusMethodNotAllowed {
			continue // the mux answers; nothing to render
		}
		// Not "no-referrer": browsers then post forms with "Origin: null", which the
		// origin check (rightly) refuses, and every button on the site stops working.
		for k, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "same-origin", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY"} {
			if h.Get(k) != want {
				t.Errorf("%s: %s = %q, want %q", name, k, h.Get(k), want)
			}
		}
		if strings.Contains(p.Body, "<script") || strings.Contains(p.Body, "<style") || regexp.MustCompile(`(?i)\sstyle=|\son[a-z]+=`).MatchString(p.Body) {
			t.Errorf("%s: inline script or style in the page", name)
		}
		if p.StatusCode == http.StatusOK && !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
			t.Errorf("%s: Content-Type %q", name, h.Get("Content-Type"))
		}
	}
	css := anon.get("/admin/assets/app.css")
	if css.StatusCode != http.StatusOK || !strings.HasPrefix(css.Header.Get("Content-Type"), "text/css") || css.Header.Get("X-Content-Type-Options") != "nosniff" || css.Body == "" {
		t.Errorf("stylesheet: %d %q", css.StatusCode, css.Header.Get("Content-Type"))
	}
	for name, p := range pages {
		if p.StatusCode == http.StatusOK && !strings.Contains(p.Body, `/admin/assets/app.css`) {
			t.Errorf("%s: does not link the stylesheet", name)
		}
	}
}

// ---- secrets

func TestSessionSecretNotStored(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	token := e.setupLink("")
	e.setupAs(e.browser(), token, ghUser{ID: 5, Login: "second"}) // another owner through a setup link
	e.signIn(b, octo)
	secret := b.cookie(sessionCookieName(e)).Value

	var asSecret, asHash int
	db := e.db()
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, secret).Scan(&asSecret); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, HashSecret(secret)).Scan(&asHash); err != nil {
		t.Fatal(err)
	}
	if asSecret != 0 || asHash != 1 {
		t.Errorf("the cookie secret is stored %d times, its SHA-256 %d times; want 0 and 1", asSecret, asHash)
	}
	// Nothing secret is anywhere in the database files, including the write-ahead log.
	files, _ := filepath.Glob(filepath.Join(e.dataDir, "privo.db*"))
	if len(files) == 0 {
		t.Fatal("no database files")
	}
	needles := append([]string{secret, token, ghClientSecret}, e.gh.issuedTokens...)
	needles = append(needles, e.gh.issuedCodes...)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range needles {
			if strings.Contains(string(raw), n) {
				t.Errorf("%s contains a secret (%d chars, starts %q)", filepath.Base(f), len(n), n[:4])
			}
		}
	}
}

func TestNothingSecretInLogsOrAudit(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	token := e.setupLink("")
	b := e.browser()
	e.setupAs(b, token, ghUser{ID: 6, Login: "second-owner"})
	e.gh.failExchange = true
	e.signIn(e.browser(), octo) // a failed sign-in
	e.gh.failExchange = false
	e.signIn(e.browser(), ghUser{ID: 99, Login: "stranger"}) // a denied one
	b2 := e.browser()
	e.signIn(b2, octo)
	id := e.pending("ada", "t", `{"password":"hunter2-in-arguments"}`)
	page := b2.get("/admin/approvals")
	csrf := csrfOf(t, page.Body)
	b2.post("/admin/approvals/"+id+"/approve", url.Values{"csrf": {csrf}})
	e.audit.Close()

	var haystack strings.Builder
	haystack.WriteString(e.logs.String())
	evs, _ := e.st.RecentAdminEvents(context.Background(), e.org, 500)
	for _, ev := range evs {
		haystack.WriteString(strings.Join([]string{ev.ID, ev.CallID, ev.Actor, ev.ActorID, ev.Client, ev.Server, ev.Action, ev.Decision, ev.Outcome}, "|") + "\n")
	}
	needles := []string{ghClientSecret, token, csrf, "hunter2-in-arguments"}
	needles = append(needles, e.gh.issuedTokens...)
	needles = append(needles, e.gh.issuedCodes...)
	for _, br := range []*browser{b, b2} {
		for _, c := range br.cookies {
			needles = append(needles, c.Value)
		}
	}
	for _, n := range needles {
		if n != "" && strings.Contains(haystack.String(), n) {
			t.Errorf("a secret (%d chars, starts %q) appears in the logs or the audit", len(n), n[:min(4, len(n))])
		}
	}
	if len(evs) == 0 {
		t.Fatal("nothing was audited: the check proves nothing")
	}
}

func TestExpiredStatesArePrunedOnStart(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	b.post("/auth/github/start", nil)
	e.clock.advance(15 * time.Minute)
	b.post("/auth/github/start", nil)
	var n int
	if err := e.db().QueryRow(`SELECT COUNT(*) FROM oauth_states`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d states stored, want only the unexpired one", n)
	}
}

func TestRunPrunesNowAndStopsWithItsContext(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	e.signIn(e.browser(), octo)
	e.clock.advance(13 * time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.srv.Run(ctx, 5*time.Millisecond); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		if err := e.db().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d sessions after Run", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop when its context was cancelled")
	}
}
