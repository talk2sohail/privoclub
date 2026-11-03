//go:build acceptance

package acceptance

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// F05: sign-in with GitHub and the approvals page, against the real binary and
// a fake GitHub. The fake checks the same things GitHub does (client secret,
// redirect URI, PKCE) and plays the person's consent screen by redirecting
// straight back.

const (
	webClientID     = "Iv1.acceptance"
	webClientSecret = "acceptance-client-secret-NEVER-LEAK-4711"
)

type fakeGitHubUser struct {
	ID    int
	Login string
	Name  string
}

type acceptanceGitHub struct {
	srv *httptest.Server
	mu  sync.Mutex
	// as is who the next authorization is for.
	as      fakeGitHubUser
	grants  map[string]fakeGrant
	tokens  map[string]fakeGitHubUser
	secrets []string // codes and access tokens it handed out
}

type fakeGrant struct {
	challenge, redirect string
	user                fakeGitHubUser
}

func newAcceptanceGitHub(t *testing.T) *acceptanceGitHub {
	t.Helper()
	f := &acceptanceGitHub{grants: map[string]fakeGrant{}, tokens: map[string]fakeGitHubUser{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != webClientID || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
			http.Error(w, "bad authorize request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		code := "code-" + randomHex(10)
		f.grants[code] = fakeGrant{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), user: f.as}
		f.secrets = append(f.secrets, code)
		f.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		bq := back.Query()
		bq.Set("code", code)
		bq.Set("state", q.Get("state"))
		back.RawQuery = bq.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		g, ok := f.grants[r.PostForm.Get("code")]
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || r.PostForm.Get("client_id") != webClientID || r.PostForm.Get("client_secret") != webClientSecret ||
			r.PostForm.Get("redirect_uri") != g.redirect || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
			return
		}
		delete(f.grants, r.PostForm.Get("code"))
		tok := "gho_" + randomHex(16)
		f.tokens[tok] = g.user
		f.secrets = append(f.secrets, tok)
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"bearer","scope":""}`, tok)
	})
	mux.HandleFunc("GET /api/user", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		u, ok := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprintf(w, `{"login":%q,"id":%d,"name":%q}`, u.Login, u.ID, u.Name)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// webGateway starts the gateway with web sign-in configured against gh.
func webGateway(t *testing.T, gh *acceptanceGitHub) *gateway {
	t.Helper()
	secretFile := filepath.Join(t.TempDir(), "github-client-secret")
	if err := os.WriteFile(secretFile, []byte(webClientSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return startGateway(t,
		"PRIVO_GITHUB_CLIENT_ID="+webClientID,
		"PRIVO_GITHUB_CLIENT_SECRET_FILE="+secretFile,
		"PRIVO_GITHUB_BASE_URL="+gh.srv.URL,
		"PRIVO_GITHUB_API_URL="+gh.srv.URL+"/api",
		"PRIVO_EGRESS_ALLOW_CIDRS=127.0.0.0/8", // the fake GitHub is on loopback
	)
}

// person is a browser: a cookie jar that does not follow redirects by itself.
type person struct {
	t    *testing.T
	g    *gateway
	http *http.Client
}

func newPerson(t *testing.T, g *gateway) *person {
	jar, _ := cookiejar.New(nil)
	return &person{t: t, g: g, http: &http.Client{Jar: jar, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (p *person) do(method, target string, form url.Values, hdr map[string]string) (*http.Response, string) {
	p.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		p.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == http.MethodPost {
		req.Header.Set("Origin", p.g.base)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := p.http.Do(req)
	if err != nil {
		p.t.Fatalf("%s %s: %v", method, target, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res, string(raw)
}

func (p *person) approvals() (*http.Response, string) {
	p.t.Helper()
	return p.do(http.MethodGet, p.g.base+"/admin/approvals", nil, nil)
}

// csrf reads the session's CSRF token the way the admin app does (ADR 0019).
func (p *person) csrf() string {
	p.t.Helper()
	res, body := p.do(http.MethodGet, p.g.base+"/admin/api/session", nil, nil)
	var s struct {
		CSRF string `json:"csrf"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &s) != nil || s.CSRF == "" {
		p.t.Fatalf("session: %d %s", res.StatusCode, body)
	}
	return s.CSRF
}

// app calls /admin/api{path} as the admin app does: JSON, same origin, the
// session's CSRF token in a header. hdr overrides headers ("" removes one).
func (p *person) app(method, path string, body any, hdr map[string]string) (int, map[string]any) {
	p.t.Helper()
	var r io.Reader
	h := map[string]string{"Origin": p.g.base, "X-Privo-CSRF": p.csrf(), "Accept": "application/json"}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			p.t.Fatal(err)
		}
		r = bytes.NewReader(b)
		h["Content-Type"] = "application/json"
	}
	for k, v := range hdr {
		h[k] = v
	}
	req, err := http.NewRequest(method, p.g.base+"/admin/api"+path, r)
	if err != nil {
		p.t.Fatal(err)
	}
	for k, v := range h {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	res, err := p.http.Do(req)
	if err != nil {
		p.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

// follow resolves a redirect Location against the page that sent it.
func (p *person) follow(from string, res *http.Response) (*http.Response, string) {
	p.t.Helper()
	loc, err := res.Location()
	if err != nil {
		p.t.Fatalf("no redirect from %s (status %d)", from, res.StatusCode)
	}
	return p.do(http.MethodGet, loc.String(), nil, nil)
}

// signInThroughGitHub plays the whole browser leg after the start request: to
// GitHub, back to the callback. It returns the callback's response.
func (p *person) signInThroughGitHub(start *http.Response) (*http.Response, string) {
	p.t.Helper()
	if start.StatusCode != http.StatusSeeOther {
		p.t.Fatalf("start: status %d", start.StatusCode)
	}
	toGitHub, _ := p.follow("start", start) // GitHub's authorize endpoint: consent, then back
	if toGitHub.StatusCode != http.StatusFound {
		p.t.Fatalf("GitHub authorize: status %d", toGitHub.StatusCode)
	}
	return p.follow("GitHub", toGitHub)
}

var (
	setupTokenField = regexp.MustCompile(`name="setup_token" value="([^"]+)"`)
	csrfField       = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
)

// pendingID is the id of the one pending approval, read through the app's API.
func (p *person) pendingID(want string) string {
	p.t.Helper()
	code, out := p.app(http.MethodGet, "/v1/approvals?status=pending", nil, nil)
	list, _ := out["approvals"].([]any)
	if code != http.StatusOK || len(list) != 1 {
		p.t.Fatalf("pending approvals: %d %v", code, out)
	}
	a, _ := list[0].(map[string]any)
	if b, _ := json.Marshal(a); want != "" && !strings.Contains(string(b), want) {
		p.t.Fatalf("approval %s lacks %q", b, want)
	}
	id, _ := a["id"].(string)
	return id
}

func TestWebSetupAndApprovals(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	readyEcho(t, g)
	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{"server": "echo", "tool": "echo", "action": "ask"}, http.StatusCreated)

	// First owner: the CLI prints a link, the person opens it and signs in.
	out, err := g.command("admin", "setup-link", "--person", "boss").Output()
	if err != nil {
		t.Fatalf("admin setup-link: %v", err)
	}
	link := strings.TrimSpace(string(out))
	if !strings.HasPrefix(link, g.base+"/admin/setup/privo_setup_") {
		t.Fatalf("setup link %q", link)
	}
	boss := newPerson(t, g)
	setupPage, body := boss.do(http.MethodGet, link, nil, nil)
	m := setupTokenField.FindStringSubmatch(body)
	if setupPage.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("setup page: %d\n%s", setupPage.StatusCode, body)
	}
	if csp := setupPage.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' "+gh.srv.URL) || strings.Contains(csp, "unsafe") {
		t.Errorf("CSP %q", csp)
	}
	gh.as = fakeGitHubUser{ID: 424242, Login: "BossHuman", Name: "The Boss"}
	start, _ := boss.do(http.MethodPost, g.base+"/auth/github/start", url.Values{"setup_token": {m[1]}}, nil)
	cb, body := boss.signInThroughGitHub(start)
	if cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/admin/servers" { // owners land on Servers (ADR 0018)
		t.Fatalf("callback: %d %s\n%s", cb.StatusCode, cb.Header.Get("Location"), body)
	}
	// The same link is spent.
	if res, _ := newPerson(t, g).do(http.MethodGet, link, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("a used setup link: status %d, want 404", res.StatusCode)
	}

	// A call that needs approval waits; the owner sees it on the page and decides.
	member := g.connect(g.issue("ada"))
	if text, isErr := call(t, member, "echo__echo", map[string]any{"text": "deploy-to-prod"}); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("first call: %q", text)
	}
	// The approvals page is the app; it reads and decides through the session.
	if res, page := boss.approvals(); res.StatusCode != http.StatusOK || !strings.Contains(page, `<div id="app">`) ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "script-src 'self';") {
		t.Fatalf("approvals page: %d %v\n%s", res.StatusCode, res.Header, page)
	}
	id := boss.pendingID("deploy-to-prod")
	approve := "/v1/approvals/" + id + "/approve"
	if code, _ := boss.app(http.MethodPost, approve, nil, map[string]string{"X-Privo-CSRF": "wrong"}); code != http.StatusForbidden {
		t.Errorf("approve with a wrong CSRF token: %d, want 403", code)
	}
	if code, _ := boss.app(http.MethodPost, approve, nil, map[string]string{"Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Errorf("approve from another origin: %d, want 403", code)
	}
	// The token API never takes the session cookie.
	if res, _ := boss.do(http.MethodPost, g.base+"/api/v1"+approve[3:], nil, map[string]string{"Content-Type": "application/json"}); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("token API with only the session cookie: %d, want 401", res.StatusCode)
	}
	if text, isErr := call(t, member, "echo__echo", map[string]any{"text": "deploy-to-prod"}); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("refused approvals let the call through: %q", text)
	}
	if code, out := boss.app(http.MethodPost, approve, nil, nil); code != http.StatusOK {
		t.Fatalf("approve: %d %v", code, out)
	}
	if text, isErr := call(t, member, "echo__echo", map[string]any{"text": "deploy-to-prod"}); isErr || text != "deploy-to-prod" {
		t.Fatalf("approved retry: %q (error=%v)", text, isErr)
	}
	g.eventually("approved call audited with the person who approved", 5*time.Second, func() bool {
		for _, e := range auditEvents(g) {
			if d, _ := e["decision"].(string); strings.HasPrefix(d, "approved:") && e["outcome"] == "ok" && strings.Contains(fmt.Sprint(e["approved_by"]), "boss (user ") {
				return true
			}
		}
		return false
	})
	g.eventually("the decision is in the admin audit", 5*time.Second, func() bool {
		code, raw := g.raw(http.MethodGet, "/api/v1/audit/events?kind=admin", g.owner, nil)
		s := string(raw)
		return code == http.StatusOK && strings.Contains(s, "setup.claim") && strings.Contains(s, "approval.approve:") && strings.Contains(s, "approvals.view:")
	})

	// A GitHub account nobody added gets the same plain refusal, and no session.
	gh.as = fakeGitHubUser{ID: 7, Login: "stranger", Name: "Stranger"}
	stranger := newPerson(t, g)
	start, _ = stranger.do(http.MethodPost, g.base+"/auth/github/start", url.Values{}, nil)
	cb, body = stranger.signInThroughGitHub(start)
	if cb.StatusCode != http.StatusForbidden || strings.Contains(body, "stranger") {
		t.Errorf("stranger: %d\n%s", cb.StatusCode, body)
	}
	if res, _ := stranger.approvals(); res.StatusCode != http.StatusSeeOther {
		t.Errorf("stranger on the approvals page: %d, want a redirect to sign-in", res.StatusCode)
	}

	// The owner signs in again later, from a new browser, then signs out.
	gh.as = fakeGitHubUser{ID: 424242, Login: "BossHuman", Name: "The Boss"}
	again := newPerson(t, g)
	start, _ = again.do(http.MethodPost, g.base+"/auth/github/start", url.Values{}, nil)
	if cb, _ = again.signInThroughGitHub(start); cb.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign in again: %d", cb.StatusCode)
	}
	if res, _ := again.do(http.MethodPost, g.base+"/admin/signout", url.Values{"csrf": {again.csrf()}}, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign out: %d", res.StatusCode)
	}
	if res, _ := again.approvals(); res.StatusCode != http.StatusSeeOther {
		t.Errorf("after sign-out: %d, want a redirect to sign-in", res.StatusCode)
	}
	if res, _ := again.do(http.MethodGet, g.base+"/admin/api/session", nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("session after sign-out: %d, want 401", res.StatusCode)
	}

	// Nothing secret reached the logs: not the client secret, the setup token,
	// a code or an access token.
	logs := g.logs.String()
	for _, secret := range append([]string{webClientSecret, m[1], strings.TrimPrefix(link, g.base+"/admin/setup/")}, gh.secrets...) {
		if strings.Contains(logs, secret) {
			t.Errorf("the gateway's logs contain a secret (%d chars, starts %q)", len(secret), secret[:4])
		}
	}
}

func TestWebDisabledByDefault(t *testing.T) {
	g := startGateway(t) // no GitHub settings
	for _, path := range []string{"/admin/signin", "/admin/approvals", "/admin/"} {
		code, body := g.raw(http.MethodGet, path, "", nil)
		if code != http.StatusServiceUnavailable || !strings.Contains(string(body), "PRIVO_GITHUB_CLIENT_ID") || !strings.Contains(string(body), "PRIVO_GITHUB_CLIENT_SECRET") {
			t.Errorf("GET %s: %d %s", path, code, body)
		}
	}
	// The rest of Privo does not care.
	g.api(http.MethodGet, "/api/v1/servers", g.owner, nil, http.StatusOK)
	// The command still prints a link, and says what is missing.
	cmd := g.command("admin", "setup-link")
	var errs strings.Builder
	cmd.Stderr = &errs
	if out, err := cmd.Output(); err != nil || !strings.Contains(string(out), "/admin/setup/privo_setup_") || !strings.Contains(errs.String(), "PRIVO_GITHUB_CLIENT_ID") {
		t.Errorf("setup-link without GitHub settings: %v %q %q", err, out, errs.String())
	}
}
