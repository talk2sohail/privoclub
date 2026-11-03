package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
)

// Tests for the admin pages (ADR 0018). The API is faked here: the pages'
// job is to translate forms into API requests as the signed-in person and to
// render the answers safely. The API's own rules are tested in
// internal/control and end to end in the acceptance suite.

type apiCall struct {
	P      auth.Principal
	Method string
	Path   string
	Body   map[string]any
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

func (f *fakeAPI) Serve(_ context.Context, p auth.Principal, method, path string, body any) (int, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := apiCall{P: p, Method: method, Path: path}
	if body != nil {
		b, _ := json.Marshal(body)
		_ = json.Unmarshal(b, &c.Body)
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

// writes are the calls that change something.
func (f *fakeAPI) writes() []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []apiCall
	for _, c := range f.calls {
		if c.Method != http.MethodGet {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeAPI) last() apiCall {
	w := f.writes()
	if len(w) == 0 {
		return apiCall{}
	}
	return w[len(w)-1]
}

func newUIEnv(t *testing.T) (*env, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{answers: map[string]fakeAnswer{}}
	api.answer("GET", "/api/v1/servers", 200, `{"servers":[{"alias":"deepwiki","transport":"http","enabled":true,"status":"up","target":"https://mcp.deepwiki.com/mcp","items":{"tool":3},"pending_review":2}]}`)
	api.answer("GET", "/api/v1/servers/deepwiki/pins", 200, `{"pins":[
		{"kind":"tool","name":"ask_question","hash":"h1","status":"pending","definition":{"name":"ask_question","description":"Ask <script>alert(1)</script> anything\nsecond line"}},
		{"kind":"tool","name":"read_wiki","hash":"h2","status":"pending","definition":{"name":"read_wiki","description":"Read the wiki"}},
		{"kind":"tool","name":"old","hash":"h0","status":"accepted","definition":{"name":"old","description":"Already fine"}}]}`)
	api.answer("GET", "/api/v1/policies", 200, `{"policies":[{"id":"p1","server":"deepwiki","tool":"read_wiki","subject":"*","action":"ask","created_by":"boss"}],"default":"allow"}`)
	api.answer("GET", "/api/v1/members", 200, `{"members":[{"person":"ada","name":"Ada","role":"member","github_login":"ada-gh"},{"person":"boss","name":"Boss","role":"owner","github_login":"boss"}]}`)
	api.answer("GET", "/api/v1/me/tokens", 200, `{"tokens":[{"id":"t1","name":"laptop","kind":"gateway","role":"owner","person":"boss"}]}`)
	api.answer("GET", "/api/v1/tokens", 200, `{"tokens":[{"id":"t1","name":"laptop","kind":"gateway","role":"owner","person":"boss"},{"id":"t2","name":"ci","kind":"control","role":"auditor","person":"ci"}]}`)
	api.answer("GET", "/api/v1/audit/events?limit=100", 200, `{"events":[{"time":"2026-11-03T10:00:00Z","actor":"ada","server":"deepwiki","item":"read_wiki","action":"tools/call","decision":"approved:x","outcome":"ok","approved_by":"boss (user 1)"}]}`)
	api.answer("GET", "/api/v1/audit/events?limit=100&kind=admin", 200, `{"events":[{"time":"2026-11-03T10:00:00Z","actor":"boss","action":"server.create","server":"deepwiki","outcome":"ok"}]}`)
	e := newEnv(t, func(o *Options) { o.API = api })
	return e, api
}

func TestNavByRole(t *testing.T) {
	e, _ := newUIEnv(t)
	links := regexp.MustCompile(`<nav>(.*?)</nav>`)
	for _, c := range []struct {
		role string
		want []string
		not  []string
	}{
		{store.RoleOwner, []string{"Approvals", "Servers", "Rules", "People", "Tokens", "Activity"}, nil},
		{store.RoleMember, []string{"Tokens"}, []string{"Servers", "Rules", "People", "Activity", "Approvals"}},
		{store.RoleApprover, []string{"Approvals"}, []string{"Servers", "Rules", "People", "Tokens", "Activity"}},
		{store.RoleAuditor, []string{"Activity"}, []string{"Servers", "Rules", "People", "Tokens", "Approvals"}},
	} {
		u := ghUser{ID: int64(len(c.role) * 100), Login: "u-" + c.role}
		e.member(u, "p-"+c.role, c.role)
		b := e.browser()
		e.signIn(b, u)
		p := b.get("/admin/tokens")
		if c.role == store.RoleApprover || c.role == store.RoleAuditor {
			p = b.get("/admin/approvals")
		}
		m := links.FindStringSubmatch(strings.ReplaceAll(p.Body, "\n", ""))
		if m == nil {
			t.Fatalf("%s: no nav in %s", c.role, p.Body)
		}
		for _, w := range c.want {
			if !strings.Contains(m[1], ">"+w+"<") {
				t.Errorf("%s: nav lacks %s: %s", c.role, w, m[1])
			}
		}
		for _, n := range c.not {
			if strings.Contains(m[1], ">"+n+"<") {
				t.Errorf("%s: nav shows %s", c.role, n)
			}
		}
	}
}

func TestPagesNeedSignInAndRole(t *testing.T) {
	e, api := newUIEnv(t)
	for _, path := range []string{"/admin/servers", "/admin/servers/deepwiki", "/admin/rules", "/admin/people", "/admin/tokens", "/admin/activity"} {
		if p := e.browser().get(path); p.StatusCode != http.StatusSeeOther || p.Header.Get("Location") != "/admin/signin" {
			t.Errorf("anonymous %s: %d %q", path, p.StatusCode, p.Header.Get("Location"))
		}
	}
	b, _ := e.signedIn(ada, "ada", store.RoleMember)
	for _, path := range []string{"/admin/servers", "/admin/servers/deepwiki", "/admin/rules", "/admin/people", "/admin/activity"} {
		if p := b.get(path); p.StatusCode != http.StatusForbidden {
			t.Errorf("member %s: %d", path, p.StatusCode)
		}
	}
	// The page asks the API as the signed-in person.
	b.get("/admin/tokens")
	api.mu.Lock()
	defer api.mu.Unlock()
	last := api.calls[len(api.calls)-1]
	if last.P.Kind != kindSession || last.P.Person != "ada" || last.P.UserID == "" || last.P.Role != store.RoleMember {
		t.Fatalf("API called as %+v", last.P)
	}
}

func TestServersPageListsAndAdds(t *testing.T) {
	e, api := newUIEnv(t)
	b, _ := e.signedIn(ghUser{ID: 1, Login: "boss"}, "boss", store.RoleOwner)
	p := b.get("/admin/servers")
	for _, want := range []string{"deepwiki", "https://mcp.deepwiki.com/mcp", "2 to review", `name="alias"`} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("servers page lacks %q", want)
		}
	}
	tok := csrfOf(t, p.Body)
	api.answer("POST", "/api/v1/servers", 201, `{"server":{"alias":"linear"}}`)
	res := b.post("/admin/servers", url.Values{"csrf": {tok}, "alias": {"linear"}, "url": {"https://mcp.linear.app/mcp"}, "transport": {"http"},
		"auth": {"header"}, "header_name": {"X-Api-Key"}, "header_value": {"sk-123"}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/servers/linear?result=added" {
		t.Fatalf("add: %d %q %s", res.StatusCode, res.Header.Get("Location"), res.Body)
	}
	body := api.last().Body
	if body["alias"] != "linear" || body["transport"] != "http" || body["headers"].(map[string]any)["X-Api-Key"] != "sk-123" || body["oauth"] != nil {
		t.Fatalf("API body %v", body)
	}
	// Each person's own account: an empty oauth object, or the admin's client.
	b.post("/admin/servers", url.Values{"csrf": {tok}, "alias": {"gh"}, "url": {"https://api.githubcopilot.com/mcp/"}, "transport": {"http"},
		"auth": {"oauth"}, "client_id": {"Iv1.x"}, "client_secret": {"s"}})
	if o, _ := api.last().Body["oauth"].(map[string]any); o["client_id"] != "Iv1.x" || o["client_secret"] != "s" || api.last().Body["headers"] != nil {
		t.Fatalf("oauth body %v", api.last().Body)
	}
	// The API's refusal is shown, the form keeps what was typed, but never a secret.
	api.answer("POST", "/api/v1/servers", 422, `{"error":"egress policy refused https://10.0.0.1/mcp: <b>private</b>"}`)
	res = b.post("/admin/servers", url.Values{"csrf": {tok}, "alias": {"inner"}, "url": {"https://10.0.0.1/mcp"}, "transport": {"http"},
		"auth": {"header"}, "header_name": {"Authorization"}, "header_value": {"Bearer TOPSECRET"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(res.Body, "egress policy refused") || strings.Contains(res.Body, "<b>private</b>") {
		t.Fatalf("refusal: %d %s", res.StatusCode, res.Body)
	}
	if !strings.Contains(res.Body, `value="inner"`) || strings.Contains(res.Body, "TOPSECRET") {
		t.Fatal("form not kept, or the secret echoed")
	}
}

func TestServerPageReviewsTools(t *testing.T) {
	e, api := newUIEnv(t)
	b, _ := e.signedIn(ghUser{ID: 1, Login: "boss"}, "boss", store.RoleOwner)
	p := b.get("/admin/servers/deepwiki")
	if !strings.Contains(p.Body, "ask_question") || !strings.Contains(p.Body, "Read the wiki") || !strings.Contains(p.Body, "Already fine") || strings.Contains(p.Body, "second line") {
		t.Fatalf("server page: %s", p.Body)
	}
	tok := csrfOf(t, p.Body)
	api.answer("POST", "/api/v1/servers/deepwiki/pins:accept", 200, `{"accepted":1}`)
	res := b.post("/admin/servers/deepwiki/accept", url.Values{"csrf": {tok}, "pin": {"tool|h2|read_wiki"}})
	if res.Header.Get("Location") != "/admin/servers/deepwiki?result=accepted" {
		t.Fatalf("accept: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	pins := api.last().Body["pins"].([]any)
	if len(pins) != 1 || pins[0].(map[string]any)["name"] != "read_wiki" || pins[0].(map[string]any)["hash"] != "h2" {
		t.Fatalf("accepted %v (only the ticked one)", pins)
	}
	// Delete needs the name typed in.
	n := len(api.writes())
	if res := b.post("/admin/servers/deepwiki/delete", url.Values{"csrf": {tok}, "confirm": {"yes"}}); res.StatusCode != http.StatusUnprocessableEntity || len(api.writes()) != n {
		t.Fatalf("delete without the name: %d", res.StatusCode)
	}
	api.answer("DELETE", "/api/v1/servers/deepwiki", 200, `{"deleted":"deepwiki"}`)
	if res := b.post("/admin/servers/deepwiki/delete", url.Values{"csrf": {tok}, "confirm": {"deepwiki"}}); res.Header.Get("Location") != "/admin/servers?result=deleted" {
		t.Fatalf("delete: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	api.answer("PATCH", "/api/v1/servers/deepwiki", 200, `{}`)
	b.post("/admin/servers/deepwiki/enabled", url.Values{"csrf": {tok}, "enabled": {"false"}})
	if c := api.last(); c.Method != "PATCH" || c.Body["enabled"] != false {
		t.Fatalf("turn off: %+v", c)
	}
	if p := b.get("/admin/servers/nosuch"); p.StatusCode != http.StatusNotFound {
		t.Errorf("unknown server: %d", p.StatusCode)
	}
}

func TestUpstreamTextIsEscaped(t *testing.T) {
	e, _ := newUIEnv(t)
	b, _ := e.signedIn(ghUser{ID: 1, Login: "boss"}, "boss", store.RoleOwner)
	p := b.get("/admin/servers/deepwiki")
	if strings.Contains(p.Body, "<script>alert(1)") || !strings.Contains(p.Body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("tool description not escaped")
	}
	for _, path := range []string{"/admin/servers", "/admin/servers/deepwiki", "/admin/rules", "/admin/people", "/admin/tokens", "/admin/activity"} {
		p := b.get(path)
		if p.StatusCode != http.StatusOK {
			t.Errorf("%s: %d %s", path, p.StatusCode, p.Body)
		}
		if regexp.MustCompile(`(?i)<script|\sstyle=|\son[a-z]+=`).MatchString(p.Body) {
			t.Errorf("%s: inline script or style", path)
		}
		if !strings.Contains(p.Header.Get("Content-Security-Policy"), "default-src 'none'") || p.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: headers %v", path, p.Header)
		}
	}
}

func TestRulesPage(t *testing.T) {
	e, api := newUIEnv(t)
	b, _ := e.signedIn(ghUser{ID: 1, Login: "boss"}, "boss", store.RoleOwner)
	p := b.get("/admin/rules")
	if !strings.Contains(p.Body, "read_wiki") || !strings.Contains(p.Body, `<option value="allow" selected>`) || !strings.Contains(p.Body, `<option value="deepwiki">`) {
		t.Fatalf("rules page: %s", p.Body)
	}
	tok := csrfOf(t, p.Body)
	api.answer("POST", "/api/v1/policies", 201, `{}`)
	b.post("/admin/rules", url.Values{"csrf": {tok}, "server": {"deepwiki"}, "tool": {""}, "subject": {"role:member"}, "action": {"ask"}})
	if c := api.last().Body; c["server"] != "deepwiki" || c["tool"] != "*" || c["subject"] != "role:member" || c["action"] != "ask" {
		t.Fatalf("rule body %v", c)
	}
	api.answer("DELETE", "/api/v1/policies/p1", 200, `{}`)
	if res := b.post("/admin/rules/p1/delete", url.Values{"csrf": {tok}}); res.Header.Get("Location") != "/admin/rules?result=rule-gone" {
		t.Fatalf("delete rule: %q", res.Header.Get("Location"))
	}
	api.answer("PATCH", "/api/v1/settings/policy", 200, `{}`)
	b.post("/admin/rules/default", url.Values{"csrf": {tok}, "default": {"ask"}})
	if c := api.last(); c.Path != "/api/v1/settings/policy" || c.Body["default"] != "ask" {
		t.Fatalf("default: %+v", c)
	}
}

func TestPeoplePage(t *testing.T) {
	e, api := newUIEnv(t)
	b, _ := e.signedIn(ghUser{ID: 1, Login: "boss"}, "boss", store.RoleOwner)
	p := b.get("/admin/people")
	if !strings.Contains(p.Body, "ada-gh") || !strings.Contains(p.Body, "(you)") || strings.Contains(p.Body, `action="/admin/people/boss/remove"`) {
		t.Fatalf("people page: %s", p.Body)
	}
	tok := csrfOf(t, p.Body)
	api.answer("POST", "/api/v1/invites", 201, `{"invite":{"person":"cleo","role":"member","url":"https://app.example.com/admin/invite/privo_invite_SECRETLINK","expires_at":"2026-11-06T10:00:00Z"}}`)
	res := b.post("/admin/people/invite", url.Values{"csrf": {tok}, "person": {" cleo "}, "role": {"member"}})
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, "privo_invite_SECRETLINK") {
		t.Fatalf("invite: %d %s", res.StatusCode, res.Body)
	}
	if c := api.last().Body; c["person"] != "cleo" || c["role"] != "member" {
		t.Fatalf("invite body %v", c)
	}
	if strings.Contains(b.get("/admin/people").Body, "SECRETLINK") {
		t.Fatal("the invite link shows again")
	}
	api.answer("PATCH", "/api/v1/members/ada", 200, `{}`)
	b.post("/admin/people/ada/role", url.Values{"csrf": {tok}, "role": {"approver"}})
	if c := api.last(); c.Path != "/api/v1/members/ada" || c.Body["role"] != "approver" {
		t.Fatalf("role: %+v", c)
	}
	n := len(api.writes())
	if res := b.post("/admin/people/ada/remove", url.Values{"csrf": {tok}}); res.StatusCode != http.StatusUnprocessableEntity || len(api.writes()) != n {
		t.Fatalf("remove without confirmation: %d", res.StatusCode)
	}
	api.answer("POST", "/api/v1/members/ada/remove", 200, `{}`)
	if res := b.post("/admin/people/ada/remove", url.Values{"csrf": {tok}, "confirm": {"yes"}}); res.Header.Get("Location") != "/admin/people?result=removed" {
		t.Fatalf("remove: %q", res.Header.Get("Location"))
	}
	// The API's refusal is shown in its words.
	api.answer("PATCH", "/api/v1/members/ada", 403, `{"error":"only owners can make owners and admins"}`)
	if res := b.post("/admin/people/ada/role", url.Values{"csrf": {tok}, "role": {"owner"}}); !strings.Contains(res.Body, "only owners can make owners") {
		t.Fatalf("refusal not shown: %s", res.Body)
	}
}

func TestTokensPageShowsTheTokenOnce(t *testing.T) {
	e, api := newUIEnv(t)
	b, _ := e.signedIn(ghUser{ID: 1, Login: "boss"}, "boss", store.RoleOwner)
	p := b.get("/admin/tokens")
	if !strings.Contains(p.Body, "laptop") || !strings.Contains(p.Body, "API") || !strings.Contains(p.Body, "ci") {
		t.Fatalf("tokens page: %s", p.Body)
	}
	tok := csrfOf(t, p.Body)
	api.answer("POST", "/api/v1/me/tokens", 201, `{"id":"t9","token":"privo_pat_SECRET9","name":"cursor","kind":"gateway","role":"owner","person":"boss","expires_at":"2027-01-01T00:00:00Z"}`)
	res := b.post("/admin/tokens/mine", url.Values{"csrf": {tok}, "name": {"cursor"}})
	want := `claude mcp add --transport http privo https://app.example.com/mcp --header &#34;Authorization: Bearer privo_pat_SECRET9&#34;`
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, want) {
		t.Fatalf("created page: %s", res.Body)
	}
	if strings.Contains(b.get("/admin/tokens").Body, "SECRET9") {
		t.Fatal("the token shows again")
	}
	api.answer("DELETE", "/api/v1/me/tokens/t1", 200, `{}`)
	if res := b.post("/admin/tokens/mine/t1/revoke", url.Values{"csrf": {tok}}); res.Header.Get("Location") != "/admin/tokens?result=revoked" {
		t.Fatalf("revoke: %q", res.Header.Get("Location"))
	}
	api.answer("POST", "/api/v1/tokens", 201, `{"id":"t3","token":"privo_pat_API3","name":"ci","kind":"control","role":"auditor","person":"ci"}`)
	res = b.post("/admin/tokens", url.Values{"csrf": {tok}, "name": {"ci"}, "person": {"ci"}, "role": {"auditor"}})
	if c := api.last().Body; c["kind"] != "control" || c["role"] != "auditor" || !strings.Contains(res.Body, "privo_pat_API3") || strings.Contains(res.Body, "claude mcp add") {
		t.Fatalf("API token: %v %s", c, res.Body)
	}
	// A member manages only their own.
	mb, _ := e.signedIn(ada, "ada", store.RoleMember)
	mp := mb.get("/admin/tokens")
	if strings.Contains(mp.Body, "All tokens") || mp.StatusCode != http.StatusOK {
		t.Fatalf("member tokens page: %d", mp.StatusCode)
	}
	if res := mb.post("/admin/tokens", url.Values{"csrf": {csrfOf(t, mp.Body)}, "name": {"x"}}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("member creating an API token: %d", res.StatusCode)
	}
}

func TestActivityPage(t *testing.T) {
	e, _ := newUIEnv(t)
	b, _ := e.signedIn(ghUser{ID: 1, Login: "boss"}, "boss", store.RoleOwner)
	if p := b.get("/admin/activity"); !strings.Contains(p.Body, "read_wiki") || !strings.Contains(p.Body, "approved <small class=\"meta\">by boss</small>") {
		t.Fatalf("calls: %s", p.Body)
	}
	if p := b.get("/admin/activity?view=admin"); !strings.Contains(p.Body, "server.create") {
		t.Fatalf("admin events: %s", p.Body)
	}
}

func TestEveryFormNeedsCSRF(t *testing.T) {
	e, api := newUIEnv(t)
	b, _ := e.signedIn(ghUser{ID: 1, Login: "boss"}, "boss", store.RoleOwner)
	n := len(api.writes())
	for _, path := range []string{"/admin/servers", "/admin/servers/deepwiki/accept", "/admin/servers/deepwiki/enabled", "/admin/servers/deepwiki/delete",
		"/admin/servers/deepwiki/connections/ada/remove", "/admin/rules", "/admin/rules/default", "/admin/rules/p1/delete", "/admin/people/invite",
		"/admin/people/ada/role", "/admin/people/ada/remove", "/admin/tokens", "/admin/tokens/mine", "/admin/tokens/mine/t1/revoke", "/admin/tokens/t1/revoke"} {
		if p := b.post(path, url.Values{"csrf": {"wrong"}, "confirm": {"yes"}}); p.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s with a wrong CSRF token: %d", path, p.StatusCode)
		}
		if p := b.do(http.MethodPost, path, url.Values{"csrf": {csrfOf(t, b.get("/admin/rules").Body)}}, map[string]string{"Origin": "https://evil.example"}); p.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s cross-origin: %d", path, p.StatusCode)
		}
	}
	if len(api.writes()) != n {
		t.Fatalf("a refused form reached the API: %+v", api.writes()[n:])
	}
}

func TestManagersLandOnServers(t *testing.T) {
	e, _ := newUIEnv(t)
	token := e.setupLink("boss")
	cb := e.setupAs(e.browser(), token, ghUser{ID: 1, Login: "boss"})
	if cb.Header.Get("Location") != "/admin/servers" {
		t.Fatalf("owner after setup lands on %q", cb.Header.Get("Location"))
	}
}

func TestMembersLandOnTokens(t *testing.T) {
	e, _ := newUIEnv(t)
	e.member(ada, "ada", store.RoleMember)
	if cb := e.signIn(e.browser(), ada); cb.Header.Get("Location") != "/admin/tokens" {
		t.Fatalf("member lands on %q", cb.Header.Get("Location"))
	}
	au := ghUser{ID: 77, Login: "aud"}
	e.member(au, "aud", store.RoleAuditor)
	if cb := e.signIn(e.browser(), au); cb.Header.Get("Location") != "/admin/activity" {
		t.Fatalf("auditor lands on %q", cb.Header.Get("Location"))
	}
}
