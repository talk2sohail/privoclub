package oidcauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/config"
	"privo.club/privo/internal/oidcauth/oidcfake"
)

const (
	clientID = "privo-test"
	// long enough to be an HMAC key, for the HS256 case
	clientSecret = "a-client-secret-that-is-long-enough-for-hmac-256"
	redirect     = "https://privo.example/auth/oidc/callback"
)

func fake(t *testing.T) (*oidcfake.Provider, *Client) {
	t.Helper()
	p := oidcfake.New(clientID, clientSecret)
	t.Cleanup(p.Close)
	return p, &Client{Issuer: p.Issuer, ClientID: clientID, ClientSecret: clientSecret, Kind: config.OIDCGeneric, HTTP: &http.Client{}}
}

// signIn runs the code flow as a browser would, with the nonce given to the
// authorization request and the one given to Exchange.
func signIn(t *testing.T, c *Client, sentNonce, checkNonce string) (Identity, error) {
	t.Helper()
	ctx := context.Background()
	verifier := "verifier-verifier-verifier-verifier-verifier-0123"
	to, err := c.AuthorizeURL(ctx, redirect, "state-1", sentNonce, verifier)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := browser.Get(to)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || back.Query().Get("state") != "state-1" {
		t.Fatalf("authorize answered %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	return c.Exchange(ctx, back.Query().Get("code"), redirect, verifier, checkNonce)
}

func TestSignInWorks(t *testing.T) {
	p, c := fake(t)
	id, err := signIn(t, c, "n1", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != p.As.Subject || id.Email != p.As.Email || id.Name != p.As.Name {
		t.Fatalf("got %+v", id)
	}
	if !strings.Contains(p.SeenScope, "openid") || !strings.Contains(p.SeenScope, "email") {
		t.Fatalf("scope %q", p.SeenScope)
	}
	if c.AuthOrigin() != p.URL {
		t.Fatalf("auth origin %q", c.AuthOrigin())
	}
}

// ADR 0034 point 4: every claim is checked, and a failure says nothing more.
func TestIDTokenChecks(t *testing.T) {
	for name, tweak := range map[string]func(map[string]any){
		"another issuer":            func(c map[string]any) { c["iss"] = "https://evil.example" },
		"expired":                   func(c map[string]any) { c["exp"] = time.Now().Add(-5 * time.Minute).Unix() },
		"issued long ago":           func(c map[string]any) { c["iat"] = time.Now().Add(-30 * time.Minute).Unix() },
		"issued in the future":      func(c map[string]any) { c["iat"] = time.Now().Add(10 * time.Minute).Unix() },
		"no iat":                    func(c map[string]any) { c["iat"] = "-" },
		"no subject":                func(c map[string]any) { c["sub"] = "" },
		"wrong at_hash":             func(c map[string]any) { c["at_hash"] = "AAAAAAAAAAAAAAAAAAAAAA" },
		"azp of another client":     func(c map[string]any) { c["azp"] = "someone-else" },
		"two audiences without azp": func(c map[string]any) { c["aud"] = []string{clientID, "someone-else"} },
		"no nonce":                  func(c map[string]any) { c["nonce"] = "-" },
	} {
		p, c := fake(t)
		p.Break(tweak)
		if _, err := signIn(t, c, "n1", "n1"); !errors.Is(err, ErrRejected) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Two audiences are fine when azp names this client.
	p, c := fake(t)
	p.Break(func(cl map[string]any) { cl["aud"] = []string{clientID, "other"}; cl["azp"] = clientID })
	if _, err := signIn(t, c, "n1", "n1"); err != nil {
		t.Errorf("two audiences with azp: %v", err)
	}
	// HMAC is never accepted, even signed with the client secret.
	p, c = fake(t)
	p.Alg = "HS256"
	if _, err := signIn(t, c, "n1", "n1"); !errors.Is(err, ErrRejected) {
		t.Errorf("HS256: %v", err)
	}
}

func TestNonceBound(t *testing.T) {
	_, c := fake(t)
	if _, err := signIn(t, c, "nonce-sent", "nonce-of-another-sign-in"); !errors.Is(err, ErrRejected) {
		t.Fatalf("got %v", err)
	}
	if _, err := signIn(t, c, "", ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("an empty nonce: %v", err)
	}
}

func TestWrongAudienceRefused(t *testing.T) {
	p, c := fake(t)
	p.Break(func(cl map[string]any) { cl["aud"] = "another-client" })
	if _, err := signIn(t, c, "n1", "n1"); !errors.Is(err, ErrRejected) {
		t.Fatalf("got %v", err)
	}
}

func TestDiscoveryIssuerMustMatch(t *testing.T) {
	p, _ := fake(t)
	c := &Client{Issuer: p.Issuer + "/", ClientID: clientID, ClientSecret: clientSecret, HTTP: &http.Client{}}
	if _, err := c.AuthorizeURL(context.Background(), redirect, "s", "n", "v"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	// A failure is not tried again at once.
	c.Issuer = p.Issuer
	if _, err := c.AuthorizeURL(context.Background(), redirect, "s", "n", "v"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("retried at once: %v", err)
	}
	c.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if _, err := c.AuthorizeURL(context.Background(), redirect, "s", "n", "v"); err != nil {
		t.Fatalf("after the pause: %v", err)
	}
}

func TestRejectedCodeSaysNothing(t *testing.T) {
	_, c := fake(t)
	if _, err := c.AuthorizeURL(context.Background(), redirect, "s", "n", "v"); err != nil {
		t.Fatal(err)
	}
	_, err := c.Exchange(context.Background(), "not-a-code", redirect, "v", "n")
	if !errors.Is(err, ErrRejected) || strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("got %v", err)
	}
}

// Point 6: who may sign in when domains are limited.
func TestGoogleNeedsHostedDomain(t *testing.T) {
	p, c := fake(t)
	c.Kind, c.AllowedDomains = config.OIDCGoogle, []string{"example.com"}
	// A personal Google account with a company address: no hd, refused.
	p.SignInAs(oidcfake.Person{Subject: "1", Email: "ada@example.com", EmailVerified: true})
	if _, err := signIn(t, c, "n", "n"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("no hd: %v", err)
	}
	p.SignInAs(oidcfake.Person{Subject: "2", Email: "ada@example.com", EmailVerified: true, HostedDomain: "other.com"})
	if _, err := signIn(t, c, "n", "n"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("another hd: %v", err)
	}
	p.SignInAs(oidcfake.Person{Subject: "3", Email: "ada@example.com", EmailVerified: true, HostedDomain: "example.com"})
	if _, err := signIn(t, c, "n", "n"); err != nil {
		t.Fatalf("the Workspace domain: %v", err)
	}
}

func TestGenericDomainNeedsVerifiedEmail(t *testing.T) {
	p, c := fake(t)
	c.AllowedDomains = []string{"example.com"}
	for _, who := range []oidcfake.Person{
		{Subject: "1", Email: "ada@example.com", EmailVerified: false},
		{Subject: "2", Email: "ada@evil.com", EmailVerified: true},
		{Subject: "3", Email: "ada@sub.example.com", EmailVerified: true},
		{Subject: "4", Email: "", EmailVerified: true},
	} {
		p.SignInAs(who)
		if _, err := signIn(t, c, "n", "n"); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%+v: %v", who, err)
		}
	}
	p.SignInAs(oidcfake.Person{Subject: "5", Email: "Ada@Example.com", EmailVerified: true})
	if _, err := signIn(t, c, "n", "n"); err != nil {
		t.Fatalf("a verified address of the domain: %v", err)
	}
}

func TestMicrosoftEmailNeverTrusted(t *testing.T) {
	p, c := fake(t)
	// Configuration refuses domains with Microsoft; were they set anyway,
	// a verified-looking email still does not let anyone in (nOAuth).
	c.Kind, c.AllowedDomains = config.OIDCMicrosoft, []string{"example.com"}
	p.SignInAs(oidcfake.Person{Subject: "1", Email: "ceo@example.com", EmailVerified: true})
	if _, err := signIn(t, c, "n", "n"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("got %v", err)
	}
	// Without domains, the tenant in the issuer is the limit: anyone the
	// provider signs in gets through, by subject.
	c.AllowedDomains = nil
	if id, err := signIn(t, c, "n", "n"); err != nil || id.Subject != "1" {
		t.Fatalf("got %+v %v", id, err)
	}
}

func TestLargeAnswersAreRefused(t *testing.T) {
	// A valid discovery document, padded past 1 MiB: refused for its size.
	var srv *httptest.Server
	big := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"` + srv.URL + `","authorization_endpoint":"` + srv.URL + `/a","token_endpoint":"` + srv.URL +
			`/t","jwks_uri":"` + srv.URL + `/k","padding":"` + strings.Repeat("x", 2<<20) + `"}`))
	})
	srv = newServer(big)
	defer srv.Close()
	c := &Client{Issuer: srv.URL, ClientID: clientID, ClientSecret: clientSecret, HTTP: &http.Client{}}
	if _, err := c.AuthorizeURL(context.Background(), redirect, "s", "n", "v"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func newServer(h http.Handler) *httptest.Server { return httptest.NewServer(h) }

// Point 4: only asymmetric signatures; never none, never HMAC (with HMAC the
// client secret, which the provider knows, would sign tokens).
func TestOnlyAsymmetricAlgorithms(t *testing.T) {
	for _, a := range algorithms {
		if a == "none" || strings.HasPrefix(a, "HS") {
			t.Fatalf("%s is accepted", a)
		}
	}
	if len(algorithms) != 3 {
		t.Fatalf("algorithms %v", algorithms)
	}
}

// Security review 2026-11-22, M-1: Unicode lowering must not turn another
// domain into an allowed one ("İnc.com" lowers to "inc.com").
func TestNonASCIIDomainsNeverMatch(t *testing.T) {
	p, c := fake(t)
	c.AllowedDomains = []string{"inc.com"}
	for _, email := range []string{"x@İnc.com", "x@inc.coMK", "x@ınc.com"} {
		p.SignInAs(oidcfake.Person{Subject: email, Email: email, EmailVerified: true})
		if _, err := signIn(t, c, "n", "n"); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%q: %v", email, err)
		}
	}
	c.Kind = config.OIDCGoogle
	p.SignInAs(oidcfake.Person{Subject: "g", Email: "x@inc.com", EmailVerified: true, HostedDomain: "İnc.com"})
	if _, err := signIn(t, c, "n", "n"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("hd İnc.com: %v", err)
	}
}

// L-1: a slow provider holds no lock that pages need, and a visitor who
// gives up does not fail discovery for everyone.
func TestSlowDiscoveryBlocksNobody(t *testing.T) {
	p, _ := fake(t)
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		p.Config.Handler.ServeHTTP(w, r)
	}))
	defer slow.Close()
	defer close(release)
	c := &Client{Issuer: p.Issuer, ClientID: clientID, ClientSecret: clientSecret, HTTP: &http.Client{Transport: redirectTo(slow.URL)}}
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() { errs <- c.Ready(ctx) }()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	if c.AuthOrigin() != "" || time.Since(start) > 100*time.Millisecond {
		t.Fatal("AuthOrigin waited for discovery")
	}
	cancel()
	if err := <-errs; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("the visitor who gave up: %v", err)
	}
	release <- struct{}{} // the discovery under way goes on and succeeds
	if err := c.Ready(context.Background()); err != nil {
		t.Fatalf("after the slow answer: %v", err)
	}
	if c.AuthOrigin() != p.URL {
		t.Fatalf("auth origin %q", c.AuthOrigin())
	}
}

// L-2: the key set must be https too (or loopback for development).
func TestPlainHTTPKeySetIsRefused(t *testing.T) {
	var srv *httptest.Server
	srv = newServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"` + srv.URL + `","authorization_endpoint":"` + srv.URL + `/a","token_endpoint":"` + srv.URL +
			`/t","jwks_uri":"http://keys.example.com/k"}`))
	}))
	defer srv.Close()
	c := &Client{Issuer: srv.URL, ClientID: clientID, ClientSecret: clientSecret, HTTP: &http.Client{}}
	if err := c.Ready(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

// redirectTo sends every request to base instead of its own host, keeping
// the path.
type redirectTo string

func (b redirectTo) RoundTrip(r *http.Request) (*http.Response, error) {
	u, _ := url.Parse(string(b))
	r2 := r.Clone(r.Context())
	r2.URL.Scheme, r2.URL.Host, r2.Host = u.Scheme, u.Host, u.Host
	return http.DefaultTransport.RoundTrip(r2)
}
