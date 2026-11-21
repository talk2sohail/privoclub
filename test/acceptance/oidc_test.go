//go:build acceptance

package acceptance

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/oidcauth/oidcfake"
)

const oidcClientSecret = "acceptance-oidc-client-secret-0123456789"

// signInThroughOIDC follows a start response through the fake provider back
// to Privo's callback and returns the callback's answer.
func (p *person) signInThroughOIDC(start *http.Response) (*http.Response, string) {
	p.t.Helper()
	if start.StatusCode != http.StatusSeeOther {
		p.t.Fatalf("start: %d", start.StatusCode)
	}
	at, _ := p.follow("start", start) // the fake provider's authorize
	if at.StatusCode != http.StatusFound {
		p.t.Fatalf("the provider answered %d", at.StatusCode)
	}
	return p.follow("provider", at)
}

// ADR 0034 through the real binary: an invite claimed with an OIDC provider,
// a second sign-in linked while signed in, a token that fails a check, and
// GitHub switched off afterwards.
func TestSignInWithOpenIDConnect(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	op := oidcfake.New("privo-acceptance", oidcClientSecret)
	t.Cleanup(op.Close)
	secret := filepath.Join(t.TempDir(), "oidc-secret")
	if err := os.WriteFile(secret, []byte(oidcClientSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oidcEnv := []string{"PRIVO_OIDC_ISSUER=" + op.Issuer, "PRIVO_OIDC_CLIENT_ID=privo-acceptance", "PRIVO_OIDC_CLIENT_SECRET_FILE=" + secret, "PRIVO_OIDC_NAME=Acme SSO"}
	g := webGateway(t, gh, oidcEnv...)
	boss := ownerOf(t, g, gh)

	// 1. Bea's invite offers both providers; she joins with the OIDC one.
	code, inv := boss.app(http.MethodPost, "/v1/invites", map[string]any{"person": "bea", "role": "member"}, nil)
	invite, _ := inv["invite"].(map[string]any)
	if code != http.StatusCreated {
		t.Fatalf("invite: %d %v", code, inv)
	}
	bea := newPerson(t, g)
	_, page := bea.do(http.MethodGet, invite["url"].(string), nil, nil)
	if !strings.Contains(page, "Continue with GitHub") || !strings.Contains(page, "Continue with Acme SSO") {
		t.Fatalf("invite page:\n%s", page)
	}
	op.SignInAs(oidcfake.Person{Subject: "bea-sub", Email: "bea@example.com", EmailVerified: true, Name: "Bea"})
	start, _ := bea.do(http.MethodPost, g.base+"/auth/oidc/start", url.Values{"invite_token": {inviteTokenField.FindStringSubmatch(page)[1]}}, nil)
	if cb, body := bea.signInThroughOIDC(start); cb.Header.Get("Location") != "/admin/start" {
		t.Fatalf("bea lands on %q: %s", cb.Header.Get("Location"), body)
	}
	if code, _ := bea.app(http.MethodGet, "/me/identities", nil, nil); code != http.StatusOK {
		t.Fatalf("bea's account: %d", code)
	}

	// 2. The owner links a second sign-in while signed in, on Your account.
	op.SignInAs(oidcfake.Person{Subject: "boss-sub", Email: "boss@example.com", EmailVerified: true, Name: "Boss"})
	start, _ = boss.do(http.MethodPost, g.base+"/auth/oidc/start", url.Values{"link": {"1"}, "csrf": {boss.csrf()}}, nil)
	if cb, body := boss.signInThroughOIDC(start); cb.Header.Get("Location") != "/admin/account?result=linked" {
		t.Fatalf("link: %d %q %s", cb.StatusCode, cb.Header.Get("Location"), body)
	}
	_, ids := boss.app(http.MethodGet, "/me/identities", nil, nil)
	if list, _ := ids["identities"].([]any); len(list) != 2 {
		t.Fatalf("the owner's sign-ins: %v", ids)
	}

	// 3. An ID token for another client is refused, and says nothing more.
	op.Break(func(c map[string]any) { c["aud"] = "someone-else" })
	stranger := newPerson(t, g)
	start, _ = stranger.do(http.MethodPost, g.base+"/auth/oidc/start", url.Values{}, nil)
	if cb, body := stranger.signInThroughOIDC(start); cb.StatusCode != http.StatusBadRequest || strings.Contains(body, "audience") {
		t.Fatalf("a token for another client: %d %s", cb.StatusCode, body)
	}
	op.Break(nil)

	// 4. GitHub switched off: the owner signs in with the linked account; a
	// member with only GitHub is counted on Health.
	join(t, g, gh, boss, "cy", 3)
	g.stop()
	var kept []string
	for _, kv := range g.env {
		if !strings.HasPrefix(kv, "PRIVO_GITHUB_") {
			kept = append(kept, kv)
		}
	}
	g.env = kept
	g.start()
	again := newPerson(t, g)
	_, page = again.do(http.MethodGet, g.base+"/admin/signin", nil, nil)
	if strings.Contains(page, "GitHub") || !strings.Contains(page, "Acme SSO") {
		t.Fatalf("sign-in page without GitHub:\n%s", page)
	}
	op.SignInAs(oidcfake.Person{Subject: "boss-sub", Email: "boss@example.com", EmailVerified: true, Name: "Boss"})
	start, _ = again.do(http.MethodPost, g.base+"/auth/oidc/start", url.Values{}, nil)
	if cb, body := again.signInThroughOIDC(start); cb.Header.Get("Location") != "/admin/servers" {
		t.Fatalf("the owner with GitHub off: %q %s", cb.Header.Get("Location"), body)
	}
	g.eventually("the warning about cy", 6*time.Second, func() bool {
		return strings.Contains(g.logs.String(), "1 active members cannot sign in")
	})
	// The client secret is never written anywhere.
	if strings.Contains(g.logs.String(), oidcClientSecret) {
		t.Fatal("the OIDC client secret is in the log")
	}
	g.assertNotOnDisk(oidcClientSecret)
}
