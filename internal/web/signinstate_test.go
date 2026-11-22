package web

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// Tests for the sign-in in flight (ADR 0016 amendment 1): it lives in a sealed
// cookie, so starting a sign-in writes nothing (F05 R-01), and a member holds
// at most one connection in flight per server (F07 R-03).

// tableCounts is the number of rows in every table of the database.
func tableCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	_ = rows.Close()
	if len(names) < 10 {
		t.Fatalf("only %d tables found: the check proves nothing", len(names))
	}
	out := map[string]int{}
	for _, n := range names {
		var c int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + n + `"`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		out[n] = c
	}
	return out
}

// dataVersion reads SQLite's data_version on one pinned connection: it changes
// whenever another connection commits a change to the database file.
func dataVersion(t *testing.T, c *sql.Conn) int {
	t.Helper()
	var v int
	if err := c.QueryRowContext(context.Background(), `PRAGMA data_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSignInStartWritesNothing(t *testing.T) {
	e := newConnEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	setup, invite := e.setupLink(""), e.invite("ada", store.RoleApprover)
	e.audit.Close() // nothing of the setup above is still on its way to the database

	db := e.db()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	version, counts := dataVersion(t, conn), tableCounts(t, db)

	for name, form := range map[string]url.Values{
		"plain":           nil,
		"setup link":      {"setup_token": {setup}},
		"invite":          {"invite_token": {invite}},
		"return to":       {"return_to": {"/connect/linear"}},
		"bad return to":   {"return_to": {"https://evil.example/"}},
		"unknown setup":   {"setup_token": {"privo_setup_" + strings.Repeat("A", 43)}},
		"unknown invite":  {"invite_token": {"privo_invite_" + strings.Repeat("A", 43)}},
		"malformed setup": {"setup_token": {"<script>"}},
	} {
		for i := 0; i < 20; i++ {
			b := e.browser()
			res := b.post("/auth/github/start", form)
			good := !strings.HasPrefix(name, "unknown") && !strings.HasPrefix(name, "malformed")
			if good && (res.StatusCode != http.StatusSeeOther || b.cookie(e.srv.signInCookie) == nil) {
				t.Fatalf("%s: status %d, cookie %v", name, res.StatusCode, b.cookie(e.srv.signInCookie))
			}
			if !good && (res.StatusCode != http.StatusNotFound || b.cookie(e.srv.signInCookie) != nil) {
				t.Fatalf("%s: status %d, cookie %v", name, res.StatusCode, b.cookie(e.srv.signInCookie))
			}
		}
	}
	// Callbacks nobody started, with and without a cookie of their own.
	b := e.browser()
	b.post("/auth/github/start", nil)
	for i := 0; i < 20; i++ {
		b.get("/auth/github/callback?code=x&state=" + strings.Repeat("B", 43))
		e.browser().get("/auth/github/callback?code=x&state=" + strings.Repeat("B", 43))
	}

	if got := dataVersion(t, conn); got != version {
		t.Errorf("the database file was written by requests without a session (data_version %d, was %d)", got, version)
	}
	after := tableCounts(t, db)
	for table, n := range counts {
		if after[table] != n {
			t.Errorf("table %s: %d rows, was %d", table, after[table], n)
		}
	}

	// The check can fail: a real sign-in does write.
	if res := e.signIn(e.browser(), octo); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in: %d", res.StatusCode)
	}
	if got := dataVersion(t, conn); got == version {
		t.Fatal("data_version did not move for a real sign-in: the check above proves nothing")
	}
}

// F05 R-01: anonymous starts took all 500 places of a table shared by every
// purpose, and everyone was locked out. Now there is nothing to fill.
func TestAnonymousStartsCannotBlockSetupInvitesOrSignIn(t *testing.T) {
	e := newConnEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	for i := 0; i < 1500; i++ { // three times the old cap
		if res := e.browser().post("/auth/github/start", nil); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("anonymous start %d: %d %s", i, res.StatusCode, res.Body)
		}
	}
	if res := e.signIn(e.browser(), octo); res.StatusCode != http.StatusSeeOther {
		t.Errorf("a member's sign-in: %d %s", res.StatusCode, res.Body)
	}
	if res := e.setupAs(e.browser(), e.setupLink(""), ghUser{ID: 2002, Login: "second-owner"}); res.StatusCode != http.StatusSeeOther {
		t.Errorf("claiming a setup link: %d %s", res.StatusCode, res.Body)
	}
	if res := e.claimInvite(e.browser(), e.invite("ada", store.RoleMember), ada); res.StatusCode != http.StatusSeeOther {
		t.Errorf("claiming an invite: %d %s", res.StatusCode, res.Body)
	}
	b := e.browser()
	if res := e.signIn(b, ada); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("the invited member's sign-in: %d", res.StatusCode)
	}
	if res := e.connect(b, "ada"); res.StatusCode != http.StatusSeeOther || !strings.Contains(location(res), "result=connected") {
		t.Errorf("connecting an account: %d %q", res.StatusCode, location(res))
	}
	var n int
	if err := e.db().QueryRow(`SELECT COUNT(*) FROM oauth_states`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d state rows left (%v), want 0", n, err)
	}
}

func TestSignInCookieIsBoundExpiresAndWorksOnce(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	cbURL := func(code, state string) string {
		return "/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
	}
	begin := func() (b *browser, cb string, cookie *http.Cookie) {
		b = e.browser()
		start := b.post("/auth/github/start", nil)
		code, state := e.gh.authorize(location(start), octo)
		return b, cbURL(code, state), b.cookie(e.srv.signInCookie)
	}
	refused := func(name string, b *browser, cb string) {
		t.Helper()
		before := e.gh.exchanges
		if res := b.get(cb); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, res.StatusCode)
		}
		if b.cookie(e.srv.sessionCookie) != nil {
			t.Errorf("%s: a session was started", name)
		}
		if e.gh.exchanges != before {
			t.Errorf("%s: GitHub was asked to exchange the code", name)
		}
	}
	with := func(c *http.Cookie, value string) *browser {
		b := e.browser()
		b.cookies[c.Name] = &http.Cookie{Name: c.Name, Value: value}
		return b
	}

	b, cb, cookie := begin()
	if len(cookie.Value) > 400 {
		t.Errorf("the sign-in cookie is %d characters", len(cookie.Value))
	}

	// Bound to the browser: no cookie, and the cookie under another name.
	refused("no cookie", e.browser(), cb)
	refused("the cookie under the connect cookie's name", with(&http.Cookie{Name: e.srv.connectCookie}, cookie.Value), cb)

	// Bound to the state: the cookie of another sign-in does not open for this
	// state, and this cookie does not open for another state.
	_, _, otherCookie := begin()
	refused("another sign-in's cookie", with(cookie, otherCookie.Value), cb)
	refused("another state", with(cookie, cookie.Value), cbURL("code", strings.Repeat("A", 43)))
	refused("a state of the wrong shape", with(cookie, cookie.Value), cbURL("code", "short"))

	// Only this Privo makes one: every changed character, a truncated value,
	// another key version, a value that is too long, and one sealed with
	// another installation's key for the same state.
	for i := 0; i < len(cookie.Value); i += 7 {
		ch := byte('A')
		if cookie.Value[i] == 'A' {
			ch = 'B'
		}
		refused(fmt.Sprint("character ", i, " changed"), with(cookie, cookie.Value[:i]+string(ch)+cookie.Value[i+1:]), cb)
	}
	refused("truncated", with(cookie, cookie.Value[:len(cookie.Value)-4]), cb)
	refused("empty", with(cookie, "x"), cb)
	refused("too long", with(cookie, cookie.Value+strings.Repeat("A", maxSealedCookie)), cb)
	raw, _ := base64.RawURLEncoding.DecodeString(cookie.Value)
	raw[3] ^= 0x7f // key version 1 becomes one that does not exist
	refused("unknown key version", with(cookie, base64.RawURLEncoding.EncodeToString(raw)), cb)
	state := cb[strings.LastIndex(cb, "=")+1:]
	foreign := newEnv(t)
	forged, err := foreign.srv.sealSignIn(context.Background(), state, signIn{Purpose: purposeSignIn, Verifier: "v", Expires: e.clock.now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	refused("sealed by another Privo", with(cookie, forged), cb)
	if strings.Contains(e.logs.String(), `"level":"ERROR"`) {
		t.Errorf("a cookie Privo did not make was logged as a server error:\n%s", e.logs.String())
	}

	// A link planted on another site brings the browser that is signing in to
	// the callback with a state of the attacker's choosing. The browser sends
	// its cookie along (SameSite=Lax), and keeps it (F05 R-03).
	refused("a planted callback in the browser that is signing in", b, cbURL("x", strings.Repeat("A", 43)))
	refused("a planted callback without a state", b, "/auth/github/callback?error=access_denied")
	if got := b.cookie(e.srv.signInCookie); got == nil || got.Value != cookie.Value {
		t.Fatal("a callback that was not this sign-in's took the cookie away")
	}

	// None of that used the sign-in up: it still works, once.
	if res := b.get(cb); res.StatusCode != http.StatusSeeOther || b.cookie(e.srv.sessionCookie) == nil {
		t.Fatalf("the real callback after the attempts: %d %s", res.StatusCode, res.Body)
	}
	if b.cookie(e.srv.signInCookie) != nil {
		t.Error("the sign-in cookie is still set after use")
	}
	exchanges := e.gh.exchanges
	replay := with(cookie, cookie.Value)
	if res := replay.get(cb); res.StatusCode != http.StatusBadRequest || replay.cookie(e.srv.sessionCookie) != nil {
		t.Errorf("replay with the cookie put back: %d", res.StatusCode)
	}
	if e.gh.exchanges != exchanges {
		t.Error("the replay reached GitHub")
	}

	// Expires: ten minutes, by the time inside the cookie, whatever the
	// browser does with Max-Age.
	b2, cb2, _ := begin()
	e.clock.advance(StateTTL - time.Second)
	b3, cb3, _ := begin()
	e.clock.advance(time.Second)
	refused("expired", b2, cb2)
	if res := b3.get(cb3); res.StatusCode != http.StatusSeeOther {
		t.Errorf("a sign-in one second old: %d", res.StatusCode)
	}
}

// The purpose, the link and where to go afterwards come from the sealed
// cookie, so the request cannot change them.
func TestSignInCookieCarriesPurposeLinkAndReturn(t *testing.T) {
	e := newConnEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	token := e.setupLink("")

	// A plain sign-in started in one browser; the callback adds what a setup
	// or invite start would have sent. Nothing but a sign-in happens.
	b := e.browser()
	start := b.post("/auth/github/start", nil)
	code, state := e.gh.authorize(location(start), ghUser{ID: 4004, Login: "stranger"})
	res := b.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state) +
		"&setup_token=" + url.QueryEscape(token) + "&purpose=setup&return_to=/connect/linear")
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("a stranger's sign-in with setup parameters on the callback: %d", res.StatusCode)
	}
	if _, err := e.st.SetupLink(context.Background(), e.org, HashSecret(token), e.clock.now()); err != nil {
		t.Errorf("the setup link was used: %v", err)
	}

	// return_to survives the trip only in its fixed shape.
	b = e.browser()
	start = b.post("/auth/github/start", url.Values{"return_to": {"/connect/linear"}})
	code, state = e.gh.authorize(location(start), octo)
	res = b.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state) + "&return_to=//evil.example")
	if res.StatusCode != http.StatusSeeOther || location(res) != "/connect/linear" {
		t.Errorf("return: %d %q", res.StatusCode, location(res))
	}
	// A cookie that names another path (it cannot be made without the key,
	// this is the second check) falls back to the landing page.
	sealed, err := e.srv.sealSignIn(context.Background(), strings.Repeat("C", 43), signIn{Purpose: purposeSignIn, Verifier: "v", ReturnTo: "https://evil.example/", Expires: e.clock.now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	in, ok := e.srv.openSignIn(context.Background(), strings.Repeat("C", 43), sealed, kindGitHub)
	if !ok || returnToRe.MatchString(in.ReturnTo) {
		t.Fatalf("openSignIn = %+v, %v", in, ok)
	}
	// A purpose Privo does not know is not a sign-in.
	sealed, _ = e.srv.sealSignIn(context.Background(), strings.Repeat("C", 43), signIn{Purpose: "connect", Verifier: "v", Expires: e.clock.now().Add(time.Minute).Unix()})
	if _, ok := e.srv.openSignIn(context.Background(), strings.Repeat("C", 43), sealed, kindGitHub); ok {
		t.Error("an unknown purpose was accepted")
	}
	sealed, _ = e.srv.sealSignIn(context.Background(), strings.Repeat("C", 43), signIn{Purpose: purposeSignIn, Expires: e.clock.now().Add(time.Minute).Unix()})
	if _, ok := e.srv.openSignIn(context.Background(), strings.Repeat("C", 43), sealed, kindGitHub); ok {
		t.Error("a sign-in without a verifier was accepted")
	}
}

// The cookie is sealed with the organisation's key, not a key of the process:
// a sign-in started before a restart (every deploy is one) finishes after it,
// and the cookie holds nothing readable.
func TestSignInCookieSurvivesARestartAndHidesItsContent(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	b := e.browser()
	start := b.post("/auth/github/start", nil)
	code, state := e.gh.authorize(location(start), octo)
	cookie := b.cookie(e.srv.signInCookie).Value

	raw, err := base64.RawURLEncoding.DecodeString(cookie)
	if err != nil {
		t.Fatalf("the cookie is not base64url: %v", err)
	}
	for _, clear := range []string{purposeSignIn, `"v"`, `"p"`, state} {
		if strings.Contains(string(raw), clear) || strings.Contains(cookie, clear) {
			t.Errorf("the cookie shows %q", clear)
		}
	}

	// The same database and master key, a new process.
	restarted := New(e.opts)
	mux := http.NewServeMux()
	restarted.Routes(mux)
	b.h = mux
	if res := b.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback after a restart: %d %s", res.StatusCode, res.Body)
	}

	// Another master key (a different installation, or a wrong key file)
	// opens nothing.
	kek, _ := vault.NewKey()
	o := e.opts
	o.Keys = keyring.New(e.st, vault.StaticKeyProvider(kek, nil))
	if _, ok := New(o).openSignIn(context.Background(), state, cookie, kindGitHub); ok {
		t.Error("the cookie opened under another master key")
	}
}

// F07 R-07: sign-in and connecting have a cookie each, and a callback reads
// only its own.
func TestSignInAndConnectFlowsDoNotEndEachOther(t *testing.T) {
	e := newConnEnv(t)
	b, _ := e.signedIn(ada, "ada", store.RoleMember)
	p := b.get("/connect/linear")
	start := b.post("/connect/linear/start", url.Values{"csrf": {csrfOf(t, p.Body)}})
	code, state := e.as.authorize(location(start), "ada")
	if b.cookie(e.srv.connectCookie) == nil || b.cookie(e.srv.signInCookie) != nil {
		t.Fatalf("cookies after a connect start: %v", b.cookies)
	}
	exchanges := e.gh.exchanges

	// The connect state on the sign-in callback: refused, GitHub never sees
	// the upstream's code or verifier, and the connection is not used up.
	if res := b.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a connect state on the sign-in callback: %d", res.StatusCode)
	}
	if e.gh.exchanges != exchanges {
		t.Error("a connect state reached GitHub's token endpoint")
	}
	// A sign-in started in the same browser meanwhile (another tab).
	b.post("/auth/github/start", nil)
	if b.cookie(e.srv.connectCookie) == nil {
		t.Fatal("starting a sign-in removed the connect cookie")
	}
	// A callback that is not this browser's connection leaves its cookie alone.
	if res := b.get("/connect/callback?code=x&state=" + strings.Repeat("A", 43)); res.StatusCode != http.StatusBadRequest || b.cookie(e.srv.connectCookie) == nil {
		t.Errorf("a foreign connect callback: %d, cookie %v", res.StatusCode, b.cookie(e.srv.connectCookie))
	}
	res := b.get("/connect/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state) + "&iss=" + url.QueryEscape(e.as.srv.URL))
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(location(res), "result=connected") {
		t.Fatalf("the connection after all that: %d %q %s", res.StatusCode, location(res), res.Body)
	}
	if b.cookie(e.srv.connectCookie) != nil {
		t.Error("the connect cookie is still set after use")
	}
	// And the sign-in callback does not take a sign-in cookie for a connection.
	if res := b.get("/connect/callback?code=x&state=" + strings.Repeat("A", 43)); res.StatusCode != http.StatusBadRequest {
		t.Errorf("connect callback with only a sign-in cookie: %d", res.StatusCode)
	}
}

// F07 R-03 at the website: a member's new start replaces their older one, and
// no number of starts gets anyone "too many sign-ins in progress".
func TestConnectStartReplacesTheMembersOlderState(t *testing.T) {
	e := newConnEnv(t)
	b, _ := e.signedIn(ada, "ada", store.RoleMember)
	csrf := csrfOf(t, b.get("/connect/linear").Body)
	states := func() (n int) {
		if err := e.db().QueryRow(`SELECT COUNT(*) FROM oauth_states`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	first := b.post("/connect/linear/start", url.Values{"csrf": {csrf}})
	firstCookie := b.cookie(e.srv.connectCookie).Value
	code1, state1 := e.as.authorize(location(first), "ada")
	for i := 0; i < 600; i++ { // more than the old org-wide cap
		if res := b.post("/connect/linear/start", url.Values{"csrf": {csrf}}); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("start %d: %d %s", i, res.StatusCode, res.Body)
		}
	}
	if n := states(); n != 1 {
		t.Fatalf("%d state rows after 601 starts by one member, want 1", n)
	}
	last := b.post("/connect/linear/start", url.Values{"csrf": {csrf}})
	code2, state2 := e.as.authorize(location(last), "ada")
	lastCookie := b.cookie(e.srv.connectCookie).Value

	// Someone else is not in the way of anything.
	other, _ := e.signedIn(ghUser{ID: 3003, Login: "bob"}, "bob", store.RoleOwner)
	if res := e.connect(other, "bob"); res.StatusCode != http.StatusSeeOther || !strings.Contains(location(res), "result=connected") {
		t.Errorf("another member's connection: %d %q", res.StatusCode, location(res))
	}

	// The replaced start is dead even with its own cookie; the newest works.
	b.cookies[e.srv.connectCookie].Value = firstCookie
	if res := b.get("/connect/callback?code=" + url.QueryEscape(code1) + "&state=" + url.QueryEscape(state1) + "&iss=" + url.QueryEscape(e.as.srv.URL)); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a replaced start: %d", res.StatusCode)
	}
	b.cookies[e.srv.connectCookie].Value = lastCookie
	res := b.get("/connect/callback?code=" + url.QueryEscape(code2) + "&state=" + url.QueryEscape(state2) + "&iss=" + url.QueryEscape(e.as.srv.URL))
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(location(res), "result=connected") {
		t.Fatalf("the newest start: %d %q %s", res.StatusCode, location(res), res.Body)
	}
	if n := states(); n != 0 {
		t.Errorf("%d state rows after both connections finished, want 0", n)
	}
}

func TestFinishedRemembersBoundsAndForgets(t *testing.T) {
	var f finished
	t0 := time.Unix(1_800_000_000, 0)
	if !f.first("a", t0, time.Minute) || f.first("a", t0, time.Minute) || f.first("a", t0.Add(59*time.Second), time.Minute) {
		t.Fatal("a state is first exactly once while it is remembered")
	}
	if !f.first("b", t0.Add(time.Second), time.Minute) {
		t.Fatal("another state was refused")
	}
	// After its time an entry is dropped (its cookie cannot open any more).
	if !f.first("a", t0.Add(time.Minute), time.Minute) {
		t.Error("an entry outlived its time")
	}
	if len(f.until) != 2 || len(f.order) != 2 {
		t.Errorf("%d entries, %d in order; want 2 and 2", len(f.until), len(f.order))
	}

	// It never grows past its bound: the oldest goes first, the newest stay.
	f = finished{}
	for i := 0; i < maxFinished+500; i++ {
		if !f.first(fmt.Sprint("s", i), t0, time.Minute) {
			t.Fatalf("state %d was not first", i)
		}
	}
	if len(f.until) != maxFinished || len(f.order) != maxFinished {
		t.Fatalf("%d entries, %d in order; want %d", len(f.until), len(f.order), maxFinished)
	}
	if f.first(fmt.Sprint("s", maxFinished+499), t0, time.Minute) {
		t.Error("the newest entry was forgotten")
	}
	if !f.first("s0", t0, time.Minute) {
		t.Error("the oldest entry was kept past the bound")
	}
	// A remembered state stays refused when the set is full.
	if f.first(fmt.Sprint("s", maxFinished+400), t0, time.Minute) {
		t.Error("a remembered state was accepted because the set was full")
	}

	// Safe for callbacks at the same time: one of many wins.
	f = finished{}
	var wg sync.WaitGroup
	wins := make(chan bool, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); wins <- f.first("same", t0, time.Minute) }()
	}
	wg.Wait()
	close(wins)
	n := 0
	for w := range wins {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d of 64 callbacks at once were first, want 1", n)
	}
}

// F05 R-02: a refused sign-in is the one write a person who is not a member
// can cause. One row per account in ten minutes, a bound over all accounts,
// and the rest is counted in the log.
func TestRefusedSignInsCannotFloodTheAudit(t *testing.T) {
	e := newEnv(t)
	e.member(octo, "octocat", store.RoleOwner)
	denied := func() (n int) {
		for _, a := range e.adminActions() {
			if a == "signin.denied" {
				n++
			}
		}
		return n
	}
	stranger := ghUser{ID: 4242, Login: "stranger"}
	for i := 0; i < 5; i++ {
		if res := e.signIn(e.browser(), stranger); res.StatusCode != http.StatusForbidden {
			t.Fatalf("attempt %d: %d", i, res.StatusCode)
		}
	}
	if n := denied(); n != 1 {
		t.Fatalf("%d audit rows for five refusals of one account, want 1", n)
	}
	for i := 0; i < 2*maxDenials; i++ {
		if res := e.signIn(e.browser(), ghUser{ID: int64(5000 + i), Login: fmt.Sprint("s", i)}); res.StatusCode != http.StatusForbidden {
			t.Fatalf("stranger %d: %d", i, res.StatusCode)
		}
	}
	if n := denied(); n != maxDenials {
		t.Fatalf("%d audit rows after %d accounts were refused, want the bound %d", n, 2*maxDenials+1, maxDenials)
	}
	// A member is never in the way of this, and the next window records again
	// and says what the last one left out.
	if res := e.signIn(e.browser(), octo); res.StatusCode != http.StatusSeeOther {
		t.Errorf("a member's sign-in during the flood: %d", res.StatusCode)
	}
	e.clock.advance(denialWindow)
	e.signIn(e.browser(), stranger)
	if n := denied(); n != maxDenials+1 {
		t.Errorf("%d audit rows in the next window, want %d", n, maxDenials+1)
	}
	if !strings.Contains(e.logs.String(), fmt.Sprintf(`"count":%d`, 4+maxDenials+1)) {
		t.Errorf("the log does not say how many refusals were left out:\n%s", e.logs.String())
	}
	// The count is in the audit too, not only in the log (S-06).
	if !slices.Contains(e.adminActions(), fmt.Sprintf("signin.denied.skipped:%d", 4+maxDenials+1)) {
		t.Errorf("the audit does not say how many refusals were left out: %v", e.adminActions())
	}
}

// Review of 2026-11-15, S-06: strangers cannot keep a disabled member's
// refusal out of the audit, and the left-out count reaches the audit when the
// window ends even if no refusal comes after it.
func TestRefusedMembersAreAlwaysAudited(t *testing.T) {
	e := newEnv(t)
	gone := ghUser{ID: 777, Login: "former"}
	u := e.member(gone, "former", store.RoleMember)
	if err := e.st.DisableUser(context.Background(), e.org, u.ID, e.clock.now(), e.event("disable")); err != nil {
		t.Fatal(err)
	}
	count := func(action string) (n int) {
		for _, a := range e.adminActions() {
			if a == action {
				n++
			}
		}
		return n
	}
	for i := 0; i < maxDenials+5; i++ {
		e.signIn(e.browser(), ghUser{ID: int64(9000 + i), Login: fmt.Sprint("x", i)})
	}
	if res := e.signIn(e.browser(), gone); res.StatusCode != http.StatusForbidden {
		t.Fatalf("a disabled member signed in: %d", res.StatusCode)
	}
	if n := count("signin.denied"); n != maxDenials+1 {
		t.Fatalf("%d refusals audited, want the %d strangers the budget allows and the member", n, maxDenials)
	}
	// Once per window for the member too.
	e.signIn(e.browser(), gone)
	if n := count("signin.denied"); n != maxDenials+1 {
		t.Fatalf("the member's second refusal in the window was audited again (%d)", n)
	}
	// No refusal after the window: the prune writes the count.
	if err := e.srv.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(e.adminActions(), func(a string) bool { return strings.HasPrefix(a, "signin.denied.skipped") }) {
		t.Fatal("the count was written before the window ended")
	}
	e.clock.advance(denialWindow)
	if err := e.srv.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := count("signin.denied.skipped:6"); n != 1 {
		t.Fatalf("the left-out count (5 strangers and the member's repeat) was written %d times: %v", n, e.adminActions())
	}
}
