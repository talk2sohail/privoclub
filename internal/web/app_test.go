package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
)

// Tests for serving the admin app and its bridge to the admin API (ADR 0019).
// The API is faked: the bridge's job is to authenticate the browser and pass
// the request on as the signed-in person. The API's own rules are tested in
// internal/control and end to end in the acceptance suite.

const testScript = "console.log('privo app');" // compresses well enough to be gzipped

var testApp = fstest.MapFS{
	"index.html":              {Data: []byte(`<!doctype html><title>Privo</title><script type="module" src="/admin/app/assets/index-abc123.js"></script><div id="app"></div>`)},
	"assets/index-abc123.js":  {Data: []byte(strings.Repeat(testScript, 40))},
	"assets/index-def456.css": {Data: []byte("body{margin:0}")},
}

type apiCall struct {
	P      auth.Principal
	Method string
	Path   string
	Body   string
}

type fakeAPI struct {
	mu      sync.Mutex
	answers map[string]fakeAnswer // "METHOD path"
	calls   []apiCall
}

type fakeAnswer struct {
	status int
	body   string
}

func newFakeAPI() *fakeAPI { return &fakeAPI{answers: map[string]fakeAnswer{}} }

func (f *fakeAPI) Serve(_ context.Context, p auth.Principal, method, path string, body any) (int, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := apiCall{P: p, Method: method, Path: path}
	if body != nil {
		b, _ := json.Marshal(body)
		c.Body = string(b)
	}
	f.calls = append(f.calls, c)
	if a, ok := f.answers[method+" "+path]; ok {
		return a.status, []byte(a.body)
	}
	return http.StatusNotFound, []byte(`{"error":"not found"}`)
}

func (f *fakeAPI) answer(method, path string, status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[method+" "+path] = fakeAnswer{status, body}
}

func (f *fakeAPI) all() []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]apiCall(nil), f.calls...)
}

func (e *env) api() *fakeAPI { return e.srv.o.API.(*fakeAPI) }

// appSignedIn makes a member with role, signs them in and returns their browser
// and the session's CSRF token, read the way the app reads it.
func (e *env) appSignedIn(u ghUser, person, role string) (*browser, string) {
	e.t.Helper()
	e.member(u, person, role)
	b := e.browser()
	if res := e.signIn(b, u); res.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("sign in: %d %s", res.StatusCode, res.Body)
	}
	res := b.get("/admin/api/session")
	var s sessionAnswer
	if res.StatusCode != http.StatusOK || json.Unmarshal([]byte(res.Body), &s) != nil || s.CSRF == "" {
		e.t.Fatalf("session: %d %s", res.StatusCode, res.Body)
	}
	return b, s.CSRF
}

// csrf reads the session's CSRF token the way the app does.
func (b *browser) csrf() string {
	b.t.Helper()
	var s sessionAnswer
	if res := b.get("/admin/api/session"); res.StatusCode != http.StatusOK || json.Unmarshal([]byte(res.Body), &s) != nil {
		b.t.Fatalf("session: %d %s", res.StatusCode, res.Body)
	}
	return s.CSRF
}

// call is a request as the app makes it: JSON, the CSRF header, same origin.
func (b *browser) call(method, target, csrf, body string, hdr map[string]string) page {
	b.t.Helper()
	h := map[string]string{"Origin": b.pub, csrfHeader: csrf}
	if body != "" {
		h["Content-Type"] = "application/json"
	}
	for k, v := range hdr {
		h[k] = v
	}
	req := httptest.NewRequest(method, b.pub+target, strings.NewReader(body))
	for k, v := range h {
		if v != "" {
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
	return page{Response: res, Body: string(raw)}
}

func TestAppPagesServeTheShellToSignedIn(t *testing.T) {
	e := newEnvWith(t, true)
	b, _ := e.appSignedIn(octo, "octo", store.RoleOwner)
	for _, p := range appPages {
		path := strings.Replace(p, "{alias}", "github", 1)
		res := b.get(path)
		if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, `<div id="app">`) {
			t.Errorf("GET %s: %d\n%s", path, res.StatusCode, res.Body)
			continue
		}
		if csp := res.Header.Get("Content-Security-Policy"); csp != appCSP {
			t.Errorf("GET %s: CSP %q", path, csp)
		}
		if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("X-Frame-Options") != "DENY" {
			t.Errorf("GET %s: headers %v", path, res.Header)
		}
	}
	for _, bad := range []string{"script-src 'self' 'unsafe", "unsafe-inline", "unsafe-eval", "*"} {
		if strings.Contains(appCSP, bad) {
			t.Errorf("app CSP allows %q", bad)
		}
	}
}

func TestAppPagesRedirectWithoutSession(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	for _, p := range appPages {
		path := strings.Replace(p, "{alias}", "github", 1)
		if res := b.get(path); res.StatusCode != http.StatusSeeOther || location(res) != "/admin/signin" {
			t.Errorf("GET %s without a session: %d %q", path, res.StatusCode, location(res))
		}
	}
}

func TestAppAssetsAreCachedForever(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	res := b.get("/admin/app/assets/index-abc123.js")
	if res.StatusCode != http.StatusOK || res.Body != strings.Repeat(testScript, 40) {
		t.Fatalf("script: %d %q", res.StatusCode, res.Body)
	}
	if res.Header.Get("Cache-Control") != immutable || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/javascript") ||
		res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("script headers: %v", res.Header)
	}
	if css := b.get("/admin/app/assets/index-def456.css"); css.StatusCode != http.StatusOK || !strings.HasPrefix(css.Header.Get("Content-Type"), "text/css") {
		t.Errorf("stylesheet: %d %v", css.StatusCode, css.Header)
	}
	gz := b.do(http.MethodGet, "/admin/app/assets/index-abc123.js", nil, map[string]string{"Accept-Encoding": "br, gzip;q=0.8"})
	if gz.Header.Get("Content-Encoding") != "gzip" || gz.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("gzip: %v", gz.Header)
	}
	zr, err := gzip.NewReader(bytes.NewReader([]byte(gz.Body)))
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(zr); string(plain) != strings.Repeat(testScript, 40) {
		t.Errorf("gzip body differs")
	}
	for _, path := range []string{"/admin/app/assets/nope.js", "/admin/app/assets/..%2Findex.html", "/admin/app/index.html"} {
		if res := b.get(path); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, res.StatusCode)
		}
	}
}

func TestAppNeedsItsBuild(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"no index":  {"assets/index-abc123.js": testApp["assets/index-abc123.js"]},
		"no script": {"index.html": testApp["index.html"], "assets/index-def456.css": testApp["assets/index-def456.css"]},
	} {
		e := newEnv(t, func(o *Options) { o.App = fsys })
		res := e.browser().get("/admin/signin")
		if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(res.Body, "make ui") {
			t.Errorf("%s: %d\n%s", name, res.StatusCode, res.Body)
		}
	}
	e := newEnv(t, func(o *Options) { o.API = nil })
	if res := e.browser().get("/admin/signin"); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("without the API: %d", res.StatusCode)
	}
}

func TestSessionEndpointSaysWhoAndWhatTheyMayDo(t *testing.T) {
	e := newEnvWith(t, true)
	if res := e.browser().get("/admin/api/session"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a session: %d", res.StatusCode)
	}
	owner, csrf := e.appSignedIn(octo, "octo", store.RoleOwner)
	var s sessionAnswer
	_ = json.Unmarshal([]byte(owner.get("/admin/api/session").Body), &s)
	sess, _, err := e.st.SessionByHash(context.Background(), e.org, HashSecret(owner.cookie(sessionCookieName(e)).Value), e.clock.now(), time.Hour)
	if err != nil || sess.CSRF != csrf {
		t.Fatalf("csrf %q is not the session's (%v)", csrf, err)
	}
	if s.User.Person != "octo" || !slices.Equal(s.User.Roles, []string{store.RoleOwner}) || s.User.Name != "The Octocat" || s.MCPURL != e.pub+"/mcp" || s.Landing != "/admin/servers" {
		t.Errorf("owner: %+v", s)
	}
	for _, k := range []string{"decide", "servers", "rules", "people", "manage_tokens", "use_gateway", "activity", "connections", "oauth_servers"} {
		if !s.Can[k] {
			t.Errorf("owner cannot %s", k)
		}
	}
	if len(s.ChangeRoles) != 5 || len(s.InviteRoles) != 4 || s.TokenRoles[0] != store.RoleOwner {
		t.Errorf("owner roles: %v %v %v", s.ChangeRoles, s.InviteRoles, s.TokenRoles)
	}

	ada := ghUser{ID: 2002, Login: "ada-gh", Name: "Ada"}
	member, _ := e.appSignedIn(ada, "ada", store.RoleMember)
	s = sessionAnswer{}
	_ = json.Unmarshal([]byte(member.get("/admin/api/session").Body), &s)
	if s.Landing != "/admin/start" || !s.Can["use_gateway"] || !s.Can["connections"] {
		t.Errorf("member: %+v", s)
	}
	for _, k := range []string{"decide", "servers", "rules", "people", "manage_tokens", "activity"} {
		if s.Can[k] {
			t.Errorf("member can %s", k)
		}
	}
}

func TestBridgeRunsAsThePerson(t *testing.T) {
	e := newEnv(t)
	b, csrf := e.appSignedIn(octo, "octo", store.RoleAdmin)
	user, _ := e.st.UserByPerson(context.Background(), e.org, "octo")
	e.api().answer(http.MethodGet, "/api/v1/audit/events?limit=5&kind=admin", http.StatusOK, `{"events":[]}`)
	e.api().answer(http.MethodPost, "/api/v1/members/ada%20b/remove", http.StatusConflict, `{"error":"\"ada b\" is the last owner"}`)
	e.api().answer(http.MethodPost, "/api/v1/policies", http.StatusCreated, `{"policy":{"id":"p1"}}`)

	if res := b.call(http.MethodGet, "/admin/api/v1/audit/events?limit=5&kind=admin", csrf, "", nil); res.StatusCode != http.StatusOK || res.Body != `{"events":[]}` {
		t.Fatalf("GET: %d %s", res.StatusCode, res.Body)
	}
	if res := b.call(http.MethodPost, "/admin/api/v1/members/ada%20b/remove", csrf, "", nil); res.StatusCode != http.StatusConflict || !strings.Contains(res.Body, "last owner") {
		t.Fatalf("API refusal not passed on: %d %s", res.StatusCode, res.Body)
	}
	res := b.call(http.MethodPost, "/admin/api/v1/policies", csrf, `{"server":"*","action":"ask"}`, nil)
	if res.StatusCode != http.StatusCreated || res.Header.Get("Content-Type") != "application/json" || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("POST: %d %v", res.StatusCode, res.Header)
	}
	calls := e.api().all()
	if len(calls) != 3 {
		t.Fatalf("calls %+v", calls)
	}
	for _, c := range calls {
		if c.P.Kind != kindSession || c.P.UserID != user.ID || c.P.Person != "octo" || c.P.Name != "octo" || !slices.Equal(c.P.Roles, []string{store.RoleAdmin}) || c.P.OrgID != e.org {
			t.Errorf("principal %+v", c.P)
		}
	}
	if calls[1].Path != "/api/v1/members/ada%20b/remove" || calls[1].Body != "" || calls[2].Body != `{"server":"*","action":"ask"}` {
		t.Errorf("calls %+v", calls)
	}
}

func TestBridgeNeedsSessionHeaderTokenOriginAndJSON(t *testing.T) {
	e := newEnv(t)
	b, csrf := e.appSignedIn(octo, "octo", store.RoleOwner)
	big := `{"note":"` + strings.Repeat("x", maxAPIBody) + `"}`
	cases := []struct {
		name         string
		b            *browser
		method, body string
		hdr          map[string]string
		want         int
	}{
		{"no session", e.browser(), http.MethodGet, "", nil, http.StatusUnauthorized},
		{"read without the header", b, http.MethodGet, "", map[string]string{csrfHeader: ""}, http.StatusForbidden},
		{"wrong header", b, http.MethodPost, `{}`, map[string]string{csrfHeader: "nope"}, http.StatusForbidden},
		{"token in the form, not the header", b, http.MethodPost, "csrf=" + csrf, map[string]string{csrfHeader: "", "Content-Type": "application/x-www-form-urlencoded"}, http.StatusUnsupportedMediaType},
		{"token in a JSON body, not the header", b, http.MethodPost, `{"csrf":"` + csrf + `"}`, map[string]string{csrfHeader: ""}, http.StatusForbidden},
		{"another origin", b, http.MethodPost, `{}`, map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"no origin, cross-site fetch", b, http.MethodDelete, "", map[string]string{"Origin": "", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"a form body", b, http.MethodPost, "a=b", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, http.StatusUnsupportedMediaType},
		{"text/plain body", b, http.MethodPost, `{}`, map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		{"broken JSON", b, http.MethodPost, `{"a":`, nil, http.StatusBadRequest},
		{"too large", b, http.MethodPost, big, nil, http.StatusRequestEntityTooLarge},
		{"PUT", b, http.MethodPut, `{}`, nil, http.StatusMethodNotAllowed},
		// F14 R-06: a read or a delete carries no body, and a body that is
		// not JSON is refused even when it is empty.
		{"a GET with a body", b, http.MethodGet, `{}`, nil, http.StatusBadRequest},
		{"a DELETE with a body", b, http.MethodDelete, `{}`, nil, http.StatusBadRequest},
		{"an empty body sent as a form", b, http.MethodPost, "", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, http.StatusUnsupportedMediaType},
		{"an empty body sent as text", b, http.MethodPost, "", map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
	}
	for _, c := range cases {
		res := c.b.call(c.method, "/admin/api/v1/policies", csrf, c.body, c.hdr)
		if res.StatusCode != c.want {
			t.Errorf("%s: %d, want %d (%s)", c.name, res.StatusCode, c.want, res.Body)
		}
	}
	if calls := e.api().all(); len(calls) != 0 {
		t.Errorf("refused requests reached the API: %+v", calls)
	}
	// Same-origin without an Origin header is accepted by Sec-Fetch-Site.
	e.api().answer(http.MethodDelete, "/api/v1/policies/p1", http.StatusOK, `{}`)
	if res := b.call(http.MethodDelete, "/admin/api/v1/policies/p1", csrf, "", map[string]string{"Origin": "", "Sec-Fetch-Site": "same-origin"}); res.StatusCode != http.StatusOK {
		t.Errorf("same-origin delete: %d %s", res.StatusCode, res.Body)
	}
}

func TestSignedOutSessionIsRefusedByTheBridge(t *testing.T) {
	e := newEnv(t)
	b, csrf := e.appSignedIn(octo, "octo", store.RoleOwner)
	form := map[string][]string{"csrf": {csrf}}
	if res := b.post("/admin/signout", form); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign out: %d", res.StatusCode)
	}
	if res := b.call(http.MethodGet, "/admin/api/v1/servers", csrf, "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("after sign-out: %d", res.StatusCode)
	}
}

// F14 R-03: only a request that passed every check keeps a session alive. A
// refused one, or a page opened from another site, does not slide the idle
// timeout.
func TestOnlyAcceptedRequestsKeepTheSessionAlive(t *testing.T) {
	e := newEnv(t)
	b, csrf := e.appSignedIn(octo, "octo", store.RoleOwner)
	e.api().answer(http.MethodGet, "/api/v1/policies", http.StatusOK, `{"policies":[]}`)
	idle := e.opts.IdleTimeout

	// Refused requests close to the end of the idle window...
	e.clock.advance(idle - time.Minute)
	b.call(http.MethodGet, "/admin/api/v1/policies", "wrong", "", nil)
	b.call(http.MethodPost, "/admin/api/v1/policies", csrf, `{}`, map[string]string{"Origin": "https://evil.example"})
	b.do(http.MethodGet, "/admin/servers", nil, map[string]string{"Sec-Fetch-Site": "cross-site"})
	b.do(http.MethodGet, "/admin/servers", nil, map[string]string{"Sec-Fetch-Site": "same-site"})
	// A browser without Fetch Metadata, following a link from another site.
	b.do(http.MethodGet, "/admin/servers", nil, map[string]string{"Referer": "https://evil.example/page"})
	b.do(http.MethodPost, "/admin/signout", url.Values{"csrf": {"stale"}}, map[string]string{"Origin": b.pub})
	// ...did not keep it: two minutes later it is gone.
	e.clock.advance(2 * time.Minute)
	if res := b.call(http.MethodGet, "/admin/api/v1/policies", csrf, "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refused requests kept the session alive: %d %s", res.StatusCode, res.Body)
	}

	// An accepted request does keep it.
	e2 := newEnv(t)
	b2, csrf2 := e2.appSignedIn(octo, "octo", store.RoleOwner)
	e2.api().answer(http.MethodGet, "/api/v1/policies", http.StatusOK, `{"policies":[]}`)
	e2.clock.advance(idle - time.Minute)
	if res := b2.call(http.MethodGet, "/admin/api/v1/policies", csrf2, "", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("accepted: %d %s", res.StatusCode, res.Body)
	}
	e2.clock.advance(2 * time.Minute)
	if res := b2.call(http.MethodGet, "/admin/api/v1/policies", csrf2, "", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("an accepted request did not keep the session alive: %d %s", res.StatusCode, res.Body)
	}
}
