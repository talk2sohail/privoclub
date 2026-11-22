package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"privo.club/privo/internal/config"
	"privo.club/privo/internal/oidcauth"
	"privo.club/privo/internal/oidcauth/oidcfake"
	"privo.club/privo/internal/store"
)

const oidcSecret = "an-oidc-client-secret-for-the-web-tests"

// withOIDC adds a fake OIDC provider to the site (ADR 0034).
func withOIDC(p *oidcfake.Provider, domains ...string) envOpt {
	return func(o *Options) {
		o.OIDC = &oidcauth.Client{Issuer: p.Issuer, ClientID: p.ClientID, ClientSecret: oidcSecret, Kind: config.OIDCGeneric,
			AllowedDomains: domains, HTTP: &http.Client{}}
		o.OIDCName = "Acme SSO"
	}
}

func newOIDC(t *testing.T) *oidcfake.Provider {
	t.Helper()
	p := oidcfake.New("privo-web", oidcSecret)
	t.Cleanup(p.Close)
	return p
}

// oidcFlow posts to /auth/oidc/start with form, lets the fake provider
// answer as the browser would, and returns the callback's response.
func (e *env) oidcFlow(b *browser, form url.Values) page {
	e.t.Helper()
	start := b.post("/auth/oidc/start", form)
	if start.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("start: %d %s", start.StatusCode, start.Body)
	}
	return e.oidcReturn(b, start.Header.Get("Location"))
}

func (e *env) oidcReturn(b *browser, authorize string) page {
	e.t.Helper()
	nofollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := nofollow.Get(authorize)
	if err != nil {
		e.t.Fatal(err)
	}
	_ = resp.Body.Close()
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || back.Path != "/auth/oidc/callback" {
		e.t.Fatalf("the provider answered %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	return b.get(back.Path + "?" + back.RawQuery)
}

func TestOIDCSetupAndSignIn(t *testing.T) {
	p := newOIDC(t)
	e := newEnv(t, withOIDC(p))
	b := e.browser()
	signin := b.get("/admin/signin")
	if !strings.Contains(signin.Body, "Continue with GitHub") || !strings.Contains(signin.Body, "Continue with Acme SSO") {
		t.Fatalf("sign-in page:\n%s", signin.Body)
	}
	// The page's CSP lets the form go on to the provider (Chrome applies
	// form-action to the redirect).
	if csp := signin.Header.Get("Content-Security-Policy"); !strings.Contains(csp, p.URL) {
		t.Fatalf("CSP %q", csp)
	}

	token := e.setupLink("")
	if res := e.oidcFlow(b, url.Values{"setup_token": {token}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("setup: %d %s", res.StatusCode, res.Body)
	}
	u, err := e.st.UserByIdentity(context.Background(), e.org, p.Issuer, p.As.Subject)
	if err != nil || !u.Has(store.RoleOwner) || u.Person != "ada" {
		t.Fatalf("the owner: %+v %v", u, err)
	}
	// Sign out, sign in again with the same provider account.
	b2 := e.browser()
	if res := e.oidcFlow(b2, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign in: %d %s", res.StatusCode, res.Body)
	}
	if res := b2.get("/admin/api/session"); res.StatusCode != http.StatusOK {
		t.Fatalf("no session after signing in: %d", res.StatusCode)
	}
	if !e.auditHas("signin", p.Issuer+"#"+p.As.Subject) {
		t.Fatal("the sign-in is not audited with its provider and subject")
	}
}

// Point 2: nothing is found or linked by email or name.
func TestNoEmailMatching(t *testing.T) {
	p := newOIDC(t)
	e := newEnv(t, withOIDC(p))
	ada := e.member(ghUser{ID: 7, Login: "ada", Name: "Ada"}, "ada", store.RoleMember)
	// The provider says the same name and an email of the same person.
	p.SignInAs(oidcfake.Person{Subject: "other-subject", Email: "ada@example.com", EmailVerified: true, Name: "Ada"})
	res := e.oidcFlow(e.browser(), nil)
	if res.StatusCode != http.StatusForbidden || !strings.Contains(res.Body, "cannot sign in") {
		t.Fatalf("got %d %s", res.StatusCode, res.Body)
	}
	if ids, _ := e.st.Identities(context.Background(), e.org, ada.ID); len(ids) != 1 {
		t.Fatalf("an identity was linked by email: %+v", ids)
	}
}

// Point 7: a second sign-in is linked only by the signed-in member, and the
// callback must come back to the same session.
func TestLinkOnlyWhileSignedIn(t *testing.T) {
	p := newOIDC(t)
	e := newEnv(t, withOIDC(p))
	octo := ghUser{ID: 42, Login: "octo"}
	b, _ := e.appSignedIn(octo, "octo", store.RoleMember)
	csrf := csrfOfApp(t, b)

	// Without a session: sent to sign in, nothing starts.
	anon := e.browser()
	if res := anon.post("/auth/oidc/start", url.Values{"link": {"1"}}); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/signin" {
		t.Fatalf("anonymous link: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if anon.cookie(e.srv.signInCookie) != nil {
		t.Fatal("an anonymous link started a sign-in")
	}
	// With the session but without its CSRF token: refused.
	if res := b.post("/auth/oidc/start", url.Values{"link": {"1"}, "csrf": {"wrong"}}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong CSRF: %d", res.StatusCode)
	}
	// Started, then the session ends before the provider answers: refused.
	start := b.post("/auth/oidc/start", url.Values{"link": {"1"}, "csrf": {csrf}})
	if start.StatusCode != http.StatusSeeOther {
		t.Fatalf("start: %d %s", start.StatusCode, start.Body)
	}
	if res := b.post("/admin/signout", url.Values{"csrf": {csrf}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign out: %d", res.StatusCode)
	}
	if res := e.oidcReturn(b, start.Header.Get("Location")); res.StatusCode != http.StatusForbidden || !strings.Contains(res.Body, "same signed-in browser") {
		t.Fatalf("after signing out: %d %s", res.StatusCode, res.Body)
	}
	// Signed in again, the link completes and the new sign-in works.
	if res := e.signIn(b, octo); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign in again: %d", res.StatusCode)
	}
	csrf = csrfOfApp(t, b)
	if res := e.oidcFlow(b, url.Values{"link": {"1"}, "csrf": {csrf}}); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/account?result=linked" {
		t.Fatalf("link: %d %s", res.StatusCode, res.Body)
	}
	u, err := e.st.UserByIdentity(context.Background(), e.org, p.Issuer, p.As.Subject)
	if err != nil || u.Person != "octo" {
		t.Fatalf("the linked identity: %+v %v", u, err)
	}
	if !e.auditHas("identity.link:oidc", "") {
		t.Fatal("the link is not audited")
	}
	// The same provider account cannot sign in someone else too.
	b2, _ := e.appSignedIn(ghUser{ID: 43, Login: "bea"}, "bea", store.RoleMember)
	if res := e.oidcFlow(b2, url.Values{"link": {"1"}, "csrf": {csrfOfApp(t, b2)}}); res.StatusCode != http.StatusConflict {
		t.Fatalf("someone else's account: %d %s", res.StatusCode, res.Body)
	}
}

func TestUnlinkNeverTheLast(t *testing.T) {
	p := newOIDC(t)
	e := newEnv(t, withOIDC(p))
	b, csrf := e.appSignedIn(ghUser{ID: 42, Login: "octo"}, "octo", store.RoleMember)
	list := func() (out struct {
		Identities []identityView `json:"identities"`
		Providers  []linkable     `json:"providers"`
	}) {
		res := b.call(http.MethodGet, "/admin/api/me/identities", csrf, "", nil)
		if res.StatusCode != http.StatusOK || json.Unmarshal([]byte(res.Body), &out) != nil {
			t.Fatalf("list: %d %s", res.StatusCode, res.Body)
		}
		return out
	}
	got := list()
	if len(got.Identities) != 1 || got.Identities[0].Name != "GitHub" || len(got.Providers) != 2 || !got.Providers[0].Linked || got.Providers[1].Linked {
		t.Fatalf("before linking: %+v", got)
	}
	unlink := func(provider, subject string) page {
		return b.call(http.MethodPost, "/admin/api/me/identities/unlink", csrf, `{"provider":"`+provider+`","subject":"`+subject+`"}`, nil)
	}
	if res := unlink(store.ProviderGitHub, "42"); res.StatusCode != http.StatusConflict {
		t.Fatalf("the only sign-in: %d %s", res.StatusCode, res.Body)
	}
	if res := e.oidcFlow(b, url.Values{"link": {"1"}, "csrf": {csrf}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("link: %d %s", res.StatusCode, res.Body)
	}
	if got := list(); len(got.Identities) != 2 || got.Identities[1].Name != "Acme SSO" {
		t.Fatalf("after linking: %+v", got)
	}
	if res := unlink(store.ProviderGitHub, "42"); res.StatusCode != http.StatusNoContent {
		t.Fatalf("unlink GitHub: %d %s", res.StatusCode, res.Body)
	}
	if res := unlink(store.ProviderGitHub, "99"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("someone else's: %d", res.StatusCode)
	}
	if !e.auditHas("identity.unlink:github", "") {
		t.Fatal("the unlink is not audited")
	}
	// Without the CSRF header, nothing.
	if res := b.call(http.MethodPost, "/admin/api/me/identities/unlink", "", `{"provider":"x","subject":"y"}`, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("no CSRF: %d", res.StatusCode)
	}
}

// Point 9: with only OIDC configured, GitHub sign-in is off.
func TestOIDCOnlyInstance(t *testing.T) {
	p := newOIDC(t)
	e := newEnv(t, withOIDC(p), func(o *Options) { o.GitHub = nil })
	b := e.browser()
	page := b.get("/admin/signin")
	if page.StatusCode != http.StatusOK || strings.Contains(page.Body, "GitHub") || !strings.Contains(page.Body, "Acme SSO") {
		t.Fatalf("sign-in page: %d\n%s", page.StatusCode, page.Body)
	}
	if res := b.post("/auth/github/start", nil); res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GitHub start: %d", res.StatusCode)
	}
	token := e.setupLink("boss")
	if res := e.oidcFlow(b, url.Values{"setup_token": {token}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("setup: %d %s", res.StatusCode, res.Body)
	}
}

// A callback opens only its own provider's sign-in: GitHub's cookie does not
// finish at the OIDC callback, nor the other way round.
func TestCallbackOpensOnlyItsProvider(t *testing.T) {
	p := newOIDC(t)
	e := newEnv(t, withOIDC(p))
	b := e.browser()
	start := b.post("/auth/github/start", nil)
	loc, _ := url.Parse(start.Header.Get("Location"))
	state := loc.Query().Get("state")
	if res := b.get("/auth/oidc/callback?code=x&state=" + url.QueryEscape(state)); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("GitHub's sign-in at the OIDC callback: %d", res.StatusCode)
	}
	start = b.post("/auth/oidc/start", nil)
	loc, _ = url.Parse(start.Header.Get("Location"))
	state = loc.Query().Get("state")
	if res := b.get("/auth/github/callback?code=x&state=" + url.QueryEscape(state)); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("the OIDC sign-in at GitHub's callback: %d", res.StatusCode)
	}
}

// Point 6: a refusal by the domain limit is audited, under the stranger bound.
func TestOIDCDomainRefusalIsAudited(t *testing.T) {
	p := newOIDC(t)
	e := newEnv(t, withOIDC(p, "example.com"))
	p.SignInAs(oidcfake.Person{Subject: "s1", Email: "ada@example.com", EmailVerified: false})
	res := e.oidcFlow(e.browser(), nil)
	if res.StatusCode != http.StatusForbidden || !strings.Contains(res.Body, "cannot sign in here") {
		t.Fatalf("got %d %s", res.StatusCode, res.Body)
	}
	if !e.auditHas("signin.refused:not_allowed", "") {
		t.Fatal("the refusal is not audited")
	}
}

// csrfOfApp reads the session's CSRF token the way the app does.
func csrfOfApp(t *testing.T, b *browser) string {
	t.Helper()
	return b.csrf()
}

// auditHas reports whether an admin event with action (and item, when given)
// was written.
func (e *env) auditHas(action, item string) bool {
	e.t.Helper()
	evs, err := e.st.RecentAdminEvents(context.Background(), e.org, 500)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Action == action && (item == "" || ev.Item == item) {
			return true
		}
	}
	return false
}
