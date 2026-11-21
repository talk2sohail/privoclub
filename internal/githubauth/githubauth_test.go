package githubauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	secret   = "client-secret-DO-NOT-LEAK"
	authCode = "code-DO-NOT-LEAK"
	ghToken  = "gho_token_DO_NOT_LEAK"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func newClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{ClientID: "Iv1.test", ClientSecret: secret, BaseURL: mustURL(t, srv.URL), APIURL: mustURL(t, srv.URL+"/api"), HTTP: srv.Client()}
}

// RFC 7636 appendix B.
func TestChallengeMatchesTheRFCExample(t *testing.T) {
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("Challenge = %q", got)
	}
}

func TestNewVerifier(t *testing.T) {
	a, err := NewVerifier()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewVerifier()
	if a == b || len(a) < 43 || len(a) > 128 {
		t.Errorf("verifiers %q, %q: must differ and be 43 to 128 characters", a, b)
	}
	if strings.Trim(a, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~") != "" {
		t.Errorf("verifier %q uses characters outside the RFC 7636 set", a)
	}
}

func TestAuthorizeURL(t *testing.T) {
	c := &Client{ClientID: "Iv1.test", ClientSecret: secret, BaseURL: mustURL(t, "https://ghe.example.com/prefix"), APIURL: mustURL(t, "https://ghe.example.com/api/v3")}
	raw := c.AuthorizeURL("https://app.example.com/auth/github/callback", "state-123", "challenge-456")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "ghe.example.com" || u.Path != "/prefix/login/oauth/authorize" {
		t.Errorf("url %s", raw)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id": "Iv1.test", "redirect_uri": "https://app.example.com/auth/github/callback", "state": "state-123",
		"code_challenge": "challenge-456", "code_challenge_method": "S256", "allow_signup": "false",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if _, has := q["scope"]; has {
		t.Error("a scope was requested: the sign-in must read the public profile only")
	}
	if strings.Contains(raw, secret) {
		t.Error("the client secret is in the authorization URL")
	}
}

func TestExchangeSendsSecretVerifierRedirect(t *testing.T) {
	var got url.Values
	var method, path, accept string
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, accept = r.Method, r.URL.Path, r.Header.Get("Accept")
		_ = r.ParseForm()
		got = r.PostForm
		if r.URL.RawQuery != "" {
			t.Errorf("the secret or code travelled in the query: %s", r.URL.RawQuery)
		}
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"bearer","scope":""}`, ghToken)
	}))
	tok, err := c.Exchange(context.Background(), authCode, "https://app.example.com/auth/github/callback", "verifier-xyz")
	if err != nil || tok != ghToken {
		t.Fatalf("Exchange = %q, %v", tok, err)
	}
	if method != http.MethodPost || path != "/login/oauth/access_token" || accept != "application/json" {
		t.Errorf("request %s %s Accept %q", method, path, accept)
	}
	for k, want := range map[string]string{
		"client_id": "Iv1.test", "client_secret": secret, "code": authCode,
		"redirect_uri": "https://app.example.com/auth/github/callback", "code_verifier": "verifier-xyz",
	} {
		if got.Get(k) != want {
			t.Errorf("form %s = %q, want %q", k, got.Get(k), want)
		}
	}
}

func TestExchangeFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		status   int
		body     string
		rejected bool
	}{
		"error in a 200 body":     {200, `{"error":"bad_verification_code","error_description":"The code passed is incorrect or expired."}`, true},
		"incorrect client secret": {200, `{"error":"incorrect_client_credentials"}`, true},
		"server error":            {500, `oops`, false},
		"forbidden":               {403, `{"message":"rate limited"}`, false},
		"not json":                {200, `<html>hello</html>`, false},
		"empty token":             {200, `{"access_token":"","token_type":"bearer"}`, false},
		"missing token":           {200, `{"token_type":"bearer"}`, false},
		"wrong token type":        {200, `{"access_token":"x","token_type":"mac"}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			_, err := c.Exchange(context.Background(), authCode, "https://app.example.com/cb", "v")
			if err == nil {
				t.Fatal("no error")
			}
			if errors.Is(err, ErrRejected) != tc.rejected {
				t.Errorf("errors.Is(ErrRejected) = %v, want %v (%v)", !tc.rejected, tc.rejected, err)
			}
			// What GitHub said, the code and the secret never come back in an error.
			for _, leak := range []string{authCode, secret, "hello", "oops", "rate limited", "incorrect or expired"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("the error repeats %q: %v", leak, err)
				}
			}
		})
	}
}

func TestResponsesAreBounded(t *testing.T) {
	big := strings.Repeat("x", 2<<20)
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"access_token":"%s","token_type":"bearer","id":1,"login":"a"}`, big)
	}))
	if _, err := c.Exchange(context.Background(), authCode, "https://app.example.com/cb", "v"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("a 2 MiB token reply: %v, want a too-large error", err)
	}
	if _, err := c.User(context.Background(), ghToken); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("a 2 MiB profile: %v, want a too-large error", err)
	}
}

func TestRequestsTimeOut(t *testing.T) {
	block := make(chan struct{})
	c := newClient(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(block) })
	c.Timeout = 100 * time.Millisecond
	start := time.Now()
	if _, err := c.Exchange(context.Background(), authCode, "https://app.example.com/cb", "v"); err == nil {
		t.Fatal("no error from a server that never answers")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

func TestUser(t *testing.T) {
	var auth, accept, path string
	body := `{"login":"octo-cat","id":9007199254,"name":null,"avatar_url":"x","email":"private@example.com"}`
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, accept, path = r.Header.Get("Authorization"), r.Header.Get("Accept"), r.URL.Path
		_, _ = w.Write([]byte(body))
	}))
	p, err := c.User(context.Background(), ghToken)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer "+ghToken || accept != "application/vnd.github+json" || path != "/api/user" {
		t.Errorf("request: %q %q %q", auth, accept, path)
	}
	if p.ID != 9007199254 || p.Login != "octo-cat" || p.Name != "" {
		t.Errorf("profile %+v", p)
	}
	if p.Subject() != "9007199254" {
		t.Errorf("Subject = %q", p.Subject())
	}
	// The profile type has no email, avatar or token field to leak.
	if fmt.Sprintf("%+v", p) != "{ID:9007199254 Login:octo-cat Name:}" {
		t.Errorf("profile fields: %+v", p)
	}
}

func TestUserRefusesWhatIsNotAProfile(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"no id":             {200, `{"login":"a"}`},
		"zero id":           {200, `{"login":"a","id":0}`},
		"negative id":       {200, `{"login":"a","id":-4}`},
		"string id":         {200, `{"login":"a","id":"12"}`},
		"fractional id":     {200, `{"login":"a","id":1.5}`},
		"no login":          {200, `{"id":5}`},
		"login with markup": {200, `{"login":"<script>","id":5}`},
		"login with space":  {200, `{"login":"a b","id":5}`},
		"login too long":    {200, `{"login":"` + strings.Repeat("a", 65) + `","id":5}`},
		"401":               {401, `{"message":"Bad credentials"}`},
		"not json":          {200, `nope`},
		"array":             {200, `[]`},
	} {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			p, err := c.User(context.Background(), ghToken)
			if err == nil {
				t.Fatalf("accepted %+v", p)
			}
			for _, leak := range []string{ghToken, "Bad credentials", "<script>"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("the error repeats %q: %v", leak, err)
				}
			}
		})
	}
}

func TestDisplayNameIsCleaned(t *testing.T) {
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{\"login\":\"a\",\"id\":5,\"name\":\"Ada\\u0000 Love\\nlace\\u202e\"}"))
	}))
	p, err := c.User(context.Background(), ghToken)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Ada Love lace" {
		t.Errorf("Name = %q: control and bidi characters must go", p.Name)
	}
	long := strings.Repeat("é", 300)
	c2 := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"login":"a","id":5,"name":%q}`, long)
	}))
	p2, err := c2.User(context.Background(), ghToken)
	if err != nil || len([]rune(p2.Name)) != 100 {
		t.Errorf("long name: %d runes, %v; want 100", len([]rune(p2.Name)), err)
	}
}

// A followed 307 would send the client secret to another host.
func TestRedirectsAreNotFollowed(t *testing.T) {
	var hits int
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	t.Cleanup(other.Close)
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	if _, err := c.Exchange(context.Background(), authCode, "https://app.example.com/cb", "v"); err == nil {
		t.Error("a redirect was treated as an answer")
	}
	if _, err := c.User(context.Background(), ghToken); err == nil {
		t.Error("a redirect was treated as a profile")
	}
	if hits != 0 {
		t.Errorf("the redirect target was contacted %d times", hits)
	}
}

// A *url.Error prints the request URL; errors here must not.
func TestTransportErrorsDoNotPrintURLs(t *testing.T) {
	c := &Client{ClientID: "id", ClientSecret: secret, BaseURL: mustURL(t, "http://127.0.0.1:1"), APIURL: mustURL(t, "http://127.0.0.1:1/api"), HTTP: http.DefaultClient}
	_, err := c.Exchange(context.Background(), authCode, "https://app.example.com/cb", "v")
	if err == nil {
		t.Fatal("no error from a closed port")
	}
	if strings.Contains(err.Error(), "127.0.0.1:1/login") {
		t.Errorf("the error prints the URL: %v", err)
	}
}

// F05 A: read:org is asked for only when organisations are checked.
func TestAuthorizeAsksReadOrgOnlyWhenNeeded(t *testing.T) {
	c := newClient(t, http.NotFoundHandler())
	if u := c.AuthorizeURL("https://p.example/cb", "s", "c"); strings.Contains(u, "scope=") {
		t.Fatalf("a scope without organisations: %s", u)
	}
	c.OrgIDs = []int64{42}
	if u := c.AuthorizeURL("https://p.example/cb", "s", "c"); !strings.Contains(u, "scope=read%3Aorg") {
		t.Fatalf("no read:org with organisations: %s", u)
	}
}

// The organisations are read by numeric id, page by page; logins are ignored
// (an organisation's old name can be registered by someone else).
func TestOrgsUsesTheNumericID(t *testing.T) {
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/orgs" || r.Header.Get("Authorization") != "Bearer tok" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("page") {
		case "1":
			var b strings.Builder
			b.WriteString("[")
			for i := range 100 {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"login":"acme","id":%d}`, 1000+i)
			}
			b.WriteString("]")
			_, _ = w.Write([]byte(b.String()))
		case "2":
			_, _ = w.Write([]byte(`[{"login":"acme","id":42}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	ids, err := c.Orgs(t.Context(), "tok")
	if err != nil || len(ids) != 101 || !slices.Contains(ids, 42) {
		t.Fatalf("orgs: %d %v", len(ids), err)
	}
}
