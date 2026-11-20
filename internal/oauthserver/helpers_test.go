package oauthserver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
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

	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

const (
	issuer   = "https://privo.example"
	resource = issuer + "/mcp"
	// A loopback client, as Claude Code is one.
	loopbackCB = "http://localhost:3118/callback"
)

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
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// docs serves client metadata documents, as a vendor's site does.
type docs struct {
	srv  *httptest.Server
	mu   sync.Mutex
	body map[string]string // path -> body
	hits map[string]int
	// headers and status for a path, when a test changes them
	ctype  map[string]string
	status map[string]int
	cache  map[string]string
}

func newDocs(t *testing.T) *docs {
	d := &docs{body: map[string]string{}, hits: map[string]int{}, ctype: map[string]string{}, status: map[string]int{}, cache: map[string]string{}}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.hits[r.URL.Path]++
		body, ok := d.body[r.URL.Path]
		ct, status, cc := d.ctype[r.URL.Path], d.status[r.URL.Path], d.cache[r.URL.Path]
		d.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		if cc != "" {
			w.Header().Set("Cache-Control", cc)
		}
		if status != 0 {
			if status/100 == 3 {
				w.Header().Set("Location", "/elsewhere.json")
			}
			w.WriteHeader(status)
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(d.srv.Close)
	return d
}

// put publishes a well-formed document at path and returns its client id.
func (d *docs) put(path, name string, redirects ...string) string {
	id := d.srv.URL + path
	b, _ := json.Marshal(map[string]any{"client_id": id, "client_name": name, "redirect_uris": redirects,
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none"})
	d.raw(path, string(b))
	return id
}

func (d *docs) raw(path, body string) {
	d.mu.Lock()
	d.body[path] = body
	d.mu.Unlock()
}

func (d *docs) count(path string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hits[path]
}

type env struct {
	t       *testing.T
	st      *store.Store
	org     store.OrgID
	kr      *keyring.Keyring
	aw      *audit.Writer
	clk     *clock
	docs    *docs
	clients *Clients
	srv     *Server
	mux     *http.ServeMux
	logs    *strings.Builder
	dir     string
	opts    Options
	// The well-known client of most tests: a loopback client on the built-in list.
	clientID string
}

func newEnv(t *testing.T, edit ...func(*Options, *Clients)) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, err := st.EnsureOrg(ctx, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	kek, _ := vault.NewKey()
	kr := keyring.New(st, vault.StaticKeyProvider(kek, nil))
	if err := kr.EnsureOrgKey(ctx, org.ID); err != nil {
		t.Fatal(err)
	}
	logs := &strings.Builder{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	aw := audit.NewWriter(st, audit.Options{Logger: logger})
	t.Cleanup(aw.Close)
	clk := &clock{t: time.Now().UTC().Truncate(time.Second)}
	d := newDocs(t)
	e := &env{t: t, st: st, org: org.ID, kr: kr, aw: aw, clk: clk, docs: d, logs: logs, dir: dir}
	e.clientID = d.put("/known.json", "Known Client", loopbackCB, "https://app.example/cb")
	e.clients = &Clients{Org: org.ID, Store: st, Policy: PolicyKnown, HTTP: noRedirects(d.srv.Client()), Now: clk.now,
		Known: []KnownClient{{URL: e.clientID, Name: "Known Client"}}, AllowHTTP: true}
	e.opts = Options{Org: org.ID, Store: st, Keys: kr, Audit: aw, Clients: e.clients, Issuer: issuer, Logger: logger, Now: clk.now}
	for _, f := range edit {
		f(&e.opts, e.clients)
	}
	e.restart()
	return e
}

func noRedirects(c *http.Client) *http.Client {
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

// restart builds the server again on the same database, as a deploy does.
func (e *env) restart() {
	e.t.Helper()
	srv, err := New(context.Background(), e.opts)
	if err != nil {
		e.t.Fatal(err)
	}
	e.srv, e.mux = srv, http.NewServeMux()
	srv.Routes(e.mux)
}

func (e *env) member(person, role string) store.User {
	e.t.Helper()
	ctx := context.Background()
	now := e.clk.now()
	ev := func(a string) store.AuditEvent {
		return store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: now, Actor: "test", ActorID: "test", Action: a, Decision: "allow", Outcome: "ok"}
	}
	linkID := ids.New()
	if err := e.st.CreateSetupLink(ctx, e.org, store.NewSetupLink{ID: linkID, SecretHash: "hash-" + linkID, CreatedBy: "test", Now: now, ExpiresAt: now.Add(time.Hour)}, ev("setup_link.create")); err != nil {
		e.t.Fatal(err)
	}
	u, err := e.st.ClaimSetup(ctx, e.org, store.Claim{LinkID: linkID, UserID: ids.New(), Person: person, Subject: "gh-" + person, Login: person, DisplayName: person,
		Session: store.Session{ID: "sess-" + linkID, CSRF: "c", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}, Now: now}, ev("setup.claim"))
	if err != nil {
		e.t.Fatal(err)
	}
	if role != store.RoleOwner {
		if _, err := e.st.SetUserRoles(ctx, e.org, u.ID, []string{role}, ev("role")); err != nil {
			e.t.Fatal(err)
		}
		u.Roles = []string{role}
	}
	return u
}

func (e *env) db() *sql.DB {
	e.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(e.dir, "privo.db")+"?mode=ro")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = db.Close() })
	return db
}

func (e *env) rows(table string) (n int) {
	e.t.Helper()
	if err := e.db().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// actions are the audit actions written so far, oldest first.
func (e *env) actions() []string {
	e.t.Helper()
	rows, err := e.db().Query(`SELECT action FROM audit_events WHERE action LIKE 'oauth.%' ORDER BY rowid`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		out = append(out, a)
	}
	return out
}

// pkce returns a verifier and its S256 challenge.
func pkce(seed string) (verifier, challenge string) {
	verifier = strings.Repeat("v", 43) + seed
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// query is a complete, valid authorization request; edit changes it.
func (e *env) query(edit ...func(url.Values)) url.Values {
	_, challenge := pkce("")
	q := url.Values{"response_type": {"code"}, "client_id": {e.clientID}, "redirect_uri": {loopbackCB}, "scope": {"mcp"},
		"state": {"state-0123456789"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "resource": {resource}}
	for _, f := range edit {
		f(q)
	}
	return q
}

func set(k, v string) func(url.Values) { return func(q url.Values) { q.Set(k, v) } }
func del(k string) func(url.Values)    { return func(q url.Values) { q.Del(k) } }

// allow runs a valid request through consent and returns the code and the
// redirect the client's browser got.
func (e *env) allow(u store.User, q url.Values) (code string, loc *url.URL) {
	e.t.Helper()
	req, aerr := e.srv.ParseAuthorize(context.Background(), q)
	if aerr != nil {
		e.t.Fatalf("ParseAuthorize: %+v", aerr)
	}
	rec := httptest.NewRecorder()
	e.srv.Allow(rec, req, u)
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || rec.Code/100 != 3 {
		e.t.Fatalf("Allow: status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	return loc.Query().Get("code"), loc
}

type tokens struct {
	Status       int
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	Raw          string
}

func (e *env) post(path string, form url.Values) tokens {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	out := tokens{Status: rec.Code, Raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// exchange trades a code for tokens as the known client.
func (e *env) exchange(code string, edit ...func(url.Values)) tokens {
	verifier, _ := pkce("")
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {loopbackCB}, "client_id": {e.clientID},
		"code_verifier": {verifier}, "resource": {resource}}
	for _, f := range edit {
		f(form)
	}
	return e.post("/oauth/token", form)
}

func (e *env) refresh(token string, edit ...func(url.Values)) tokens {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {e.clientID}}
	for _, f := range edit {
		f(form)
	}
	return e.post("/oauth/token", form)
}

// signedIn gives a member's first tokens.
func (e *env) signedIn(u store.User) tokens {
	e.t.Helper()
	code, _ := e.allow(u, e.query())
	tok := e.exchange(code)
	if tok.Status != http.StatusOK || tok.AccessToken == "" || tok.RefreshToken == "" {
		e.t.Fatalf("code exchange: %d %s", tok.Status, tok.Raw)
	}
	return tok
}

func (e *env) verifies(access string) bool {
	_, _, _, err := e.srv.VerifyAccessToken(context.Background(), access)
	return err == nil
}

func must(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if !cond {
		t.Errorf(format, args...)
	}
}

var _ = fmt.Sprint
