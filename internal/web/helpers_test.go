package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // for the read-only look into the database

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/githubauth"
	"privo.club/privo/internal/grants"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

const (
	ghClientID     = "Iv1.testclient"
	ghClientSecret = "gh-client-secret-NEVER-LEAK-0123456789"
)

// ---- a fake clock

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// ---- a fake GitHub

type ghUser struct {
	ID    int64
	Login string
	Name  string
}

func (u ghUser) json() string {
	// Extra fields a real profile carries; Privo must ignore them.
	name := "null"
	if u.Name != "" {
		b, _ := json.Marshal(u.Name)
		name = string(b)
	}
	return fmt.Sprintf(`{"login":%q,"id":%d,"name":%s,"email":"private@example.com","avatar_url":"https://x/y.png","bio":"b"}`, u.Login, u.ID, name)
}

type ghGrant struct {
	challenge, redirect string
	user                ghUser
	used                bool
}

type fakeGitHub struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	grants map[string]*ghGrant // by code
	tokens map[string]ghUser
	// failExchange makes the token endpoint refuse every code.
	failExchange bool
	exchanges    int
	profiles     int
	issuedTokens []string
	issuedCodes  []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, grants: map[string]*ghGrant{}, tokens: map[string]ghUser{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", f.accessToken)
	mux.HandleFunc("GET /api/user", f.user)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// authorize plays GitHub's side of the browser leg: it checks the authorization
// URL Privo sent the browser to, and returns the code and state the browser
// would bring back after the person signed in as u.
func (f *fakeGitHub) authorize(location string, u ghUser) (code, state string) {
	f.t.Helper()
	loc, err := url.Parse(location)
	if err != nil {
		f.t.Fatal(err)
	}
	if loc.Scheme+"://"+loc.Host != f.srv.URL || loc.Path != "/login/oauth/authorize" {
		f.t.Fatalf("the browser was sent to %s, not GitHub's authorize endpoint", location)
	}
	q := loc.Query()
	if q.Get("client_id") != ghClientID || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("state") == "" || q.Get("allow_signup") != "false" || q.Get("redirect_uri") == "" {
		f.t.Fatalf("authorize parameters: %v", q)
	}
	if _, has := q["scope"]; has {
		f.t.Fatalf("a scope was requested: %v", q)
	}
	if strings.Contains(location, ghClientSecret) {
		f.t.Fatal("the client secret is in the authorization URL")
	}
	code = "code-" + randHex(12)
	f.mu.Lock()
	f.grants[code] = &ghGrant{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), user: u}
	f.issuedCodes = append(f.issuedCodes, code)
	f.mu.Unlock()
	return code, q.Get("state")
}

func (f *fakeGitHub) accessToken(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchanges++
	_ = r.ParseForm()
	w.Header().Set("Content-Type", "application/json")
	g := f.grants[r.PostForm.Get("code")]
	switch {
	case f.failExchange, g == nil, g.used,
		r.PostForm.Get("client_id") != ghClientID, r.PostForm.Get("client_secret") != ghClientSecret,
		r.PostForm.Get("redirect_uri") != g.redirect,
		githubauth.Challenge(r.PostForm.Get("code_verifier")) != g.challenge:
		_, _ = w.Write([]byte(`{"error":"bad_verification_code","error_description":"The code passed is incorrect or expired."}`))
		return
	}
	g.used = true
	tok := "gho_" + randHex(16)
	f.tokens[tok] = g.user
	f.issuedTokens = append(f.issuedTokens, tok)
	_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"bearer","scope":""}`, tok)
}

func (f *fakeGitHub) user(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profiles++
	u, ok := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	_, _ = w.Write([]byte(u.json()))
}

// ---- a mini browser: a cookie store over a handler

type browser struct {
	t       *testing.T
	h       http.Handler
	pub     string // the public URL, no trailing slash
	cookies map[string]*http.Cookie
	// Header values sent with every request (for example a forged Host).
	host string
}

func (b *browser) cookie(name string) *http.Cookie { return b.cookies[name] }

type page struct {
	*http.Response
	Body string
}

func (b *browser) do(method, target string, form url.Values, hdr map[string]string) page {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, b.pub+target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == http.MethodPost {
		req.Header.Set("Origin", b.pub)
	}
	if b.host != "" {
		req.Host = b.host
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	for _, c := range b.cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, req)
	res := rec.Result()
	raw, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(b.cookies, c.Name)
			continue
		}
		b.cookies[c.Name] = c
	}
	return page{Response: res, Body: string(raw)}
}

func (b *browser) get(target string) page {
	b.t.Helper()
	return b.do(http.MethodGet, target, nil, nil)
}

func (b *browser) post(target string, form url.Values) page {
	b.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	return b.do(http.MethodPost, target, form, nil)
}

// ---- the environment

type env struct {
	t         *testing.T
	st        *store.Store
	org       store.OrgID
	dataDir   string
	approvals *approval.Service
	audit     *audit.Writer
	gh        *fakeGitHub
	srv       *Server
	handler   http.Handler
	clock     *clock
	pub       string
	logs      *syncBuffer
	keys      *keyring.Keyring
	opts      Options // what srv was built from
	// set by newConnEnv (ADR 0017)
	as   *fakeAS
	ups  *fakeUpstreams
	svc  *grants.Service
	osrv store.Server
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type envOpt func(*Options)

func newEnv(t *testing.T, opts ...envOpt) *env {
	t.Helper()
	return newEnvWith(t, false, opts...)
}

func newEnvWith(t *testing.T, conn bool, opts ...envOpt) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, err := st.EnsureOrg(ctx, "default", "Default")
	if err != nil {
		t.Fatal(err)
	}
	kek, _ := vault.NewKey()
	kr := keyring.New(st, vault.StaticKeyProvider(kek, nil))
	if err := kr.EnsureOrgKey(ctx, org.ID); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	aw := audit.NewWriter(st, audit.Options{Logger: logger})
	t.Cleanup(aw.Close)
	gh := newFakeGitHub(t)
	gu, _ := url.Parse(gh.srv.URL)
	au, _ := url.Parse(gh.srv.URL + "/api")
	clk := &clock{t: time.Now().UTC().Truncate(time.Second)}
	pub := "https://app.example.com"
	pu, _ := url.Parse(pub)
	svc := &approval.Service{Store: st, Keys: kr, Hold: 10 * time.Millisecond, Poll: 2 * time.Millisecond}
	o := Options{
		Store: st, Org: org.ID, Audit: aw, Logger: logger, Now: clk.now,
		API: newFakeAPI(), App: testApp, Keys: kr,
		GitHub:    &githubauth.Client{ClientID: ghClientID, ClientSecret: ghClientSecret, BaseURL: gu, APIURL: au, HTTP: gh.srv.Client()},
		PublicURL: pu, IdleTimeout: 2 * time.Hour, MaxAge: 12 * time.Hour,
	}
	var cx *connParts
	if conn {
		cx = newConnParts(t, st, org.ID, kr, pu, clk, logger)
		o.Grants, o.OAuth, o.Upstream = cx.svc, cx.oauth, cx.ups
	}
	for _, f := range opts {
		f(&o)
	}
	srv := New(o)
	mux := http.NewServeMux()
	srv.Routes(mux)
	e := &env{t: t, st: st, org: org.ID, dataDir: dir, approvals: svc, audit: aw, gh: gh, srv: srv, handler: mux, clock: clk, logs: logs, keys: kr, opts: o}
	if cx != nil {
		e.as, e.ups, e.svc, e.osrv = cx.as, cx.ups, cx.svc, cx.srv
	}
	if o.PublicURL != nil {
		e.pub = strings.TrimSuffix(o.PublicURL.String(), "/")
	}
	return e
}

func (e *env) browser() *browser {
	return &browser{t: e.t, h: e.handler, pub: e.pub, cookies: map[string]*http.Cookie{}}
}

// setupLink stores a setup link and returns the token that would be printed.
func (e *env) setupLink(person string) string {
	e.t.Helper()
	plain, hash, err := NewSetupToken()
	if err != nil {
		e.t.Fatal(err)
	}
	err = e.st.CreateSetupLink(context.Background(), e.org, store.NewSetupLink{ID: ids.New(), SecretHash: hash, Person: person, CreatedBy: "cli",
		Now: e.clock.now(), ExpiresAt: e.clock.now().Add(time.Hour)}, e.event("setup_link.create"))
	if err != nil {
		e.t.Fatal(err)
	}
	return plain
}

func (e *env) event(action string) store.AuditEvent {
	return store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: e.clock.now(), Actor: "test", ActorID: "test", Action: action, Decision: "allow", Outcome: "ok"}
}

// member creates a user the way the first owner is created, then sets the role.
func (e *env) member(u ghUser, person, role string) store.User {
	e.t.Helper()
	token := e.setupLink("")
	l, err := e.st.SetupLink(context.Background(), e.org, HashSecret(token), e.clock.now())
	if err != nil {
		e.t.Fatal(err)
	}
	user, err := e.st.ClaimSetup(context.Background(), e.org, store.Claim{LinkID: l.ID, UserID: ids.New(), Person: person, Subject: fmt.Sprint(u.ID), Login: u.Login,
		DisplayName: orLogin(u), Session: store.Session{ID: "seed-" + randHex(8), CSRF: "seed", CreatedAt: e.clock.now(), LastSeenAt: e.clock.now(), ExpiresAt: e.clock.now().Add(time.Hour)},
		Now: e.clock.now()}, e.event("setup.claim.seed"))
	if err != nil {
		e.t.Fatal(err)
	}
	if role != store.RoleOwner {
		roles, err := e.st.SetUserRoles(context.Background(), e.org, user.ID, strings.Split(role, ","), e.event("role.seed"))
		if err != nil {
			e.t.Fatal(err)
		}
		user.Roles = roles
	}
	return user
}

func orLogin(u ghUser) string {
	if u.Name != "" {
		return u.Name
	}
	return u.Login
}

// signIn runs the whole browser flow for an existing member and returns the
// callback's response. The browser keeps the session cookie on success.
func (e *env) signIn(b *browser, u ghUser) page {
	e.t.Helper()
	start := b.post("/auth/github/start", nil)
	if start.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("start: status %d: %s", start.StatusCode, start.Body)
	}
	code, state := e.gh.authorize(start.Header.Get("Location"), u)
	return b.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
}

// setupAs claims a setup link in the browser as GitHub user u.
func (e *env) setupAs(b *browser, token string, u ghUser) page {
	e.t.Helper()
	setup := b.get("/admin/setup/" + token)
	if setup.StatusCode != http.StatusOK {
		e.t.Fatalf("setup page: status %d", setup.StatusCode)
	}
	start := b.post("/auth/github/start", url.Values{"setup_token": {token}})
	if start.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("setup start: status %d: %s", start.StatusCode, start.Body)
	}
	code, state := e.gh.authorize(start.Header.Get("Location"), u)
	return b.get("/auth/github/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
}

// db opens the database read-only, next to the store's own connections.
func (e *env) db() *sql.DB {
	e.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(e.dataDir, "privo.db")+"?mode=ro")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = db.Close() })
	return db
}

func (e *env) adminActions() []string {
	e.t.Helper()
	evs, err := e.st.RecentAdminEvents(context.Background(), e.org, 500)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, ev := range evs {
		out = append(out, ev.Action)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// csrf extracts the session's CSRF token from a rendered page.
func csrfOf(t *testing.T, body string) string {
	t.Helper()
	const marker = `name="csrf" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no csrf field in the page:\n%s", body)
	}
	rest := body[i+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("unterminated csrf field in the page:\n%s", body)
	}
	return rest[:end]
}

var octo = ghUser{ID: 1001, Login: "OctoCat", Name: "The Octocat"}
