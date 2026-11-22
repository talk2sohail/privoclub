// Package oidcfake is an OpenID Connect provider for tests (ADR 0034): it
// serves discovery, keys, authorize and token, signs its ID tokens, and can be
// told to get any claim wrong. It is never used outside tests.
package oidcfake

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc/oidctest"
)

// Person is who signs in at the fake.
type Person struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	HostedDomain  string // Google's hd
}

// Provider is a running fake provider.
type Provider struct {
	*httptest.Server
	Issuer       string
	ClientID     string
	ClientSecret string

	key *rsa.PrivateKey

	mu sync.Mutex
	// As is who the next authorization signs in.
	As Person
	// Tweak, when set, changes the claims of the next ID token: tests use
	// it to break one claim at a time. "-" deletes a claim.
	Tweak func(claims map[string]any)
	// Alg signs with another algorithm ("HS256" with the client secret);
	// empty is RS256.
	Alg string
	// SeenScope is the scope of the last authorization request.
	SeenScope string
	codes     map[string]pending
}

type pending struct {
	nonce, challenge, redirect string
	as                         Person
}

// New starts a fake provider for one client.
func New(clientID, clientSecret string) *Provider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	p := &Provider{ClientID: clientID, ClientSecret: clientSecret, key: key, codes: map[string]pending{},
		As: Person{Subject: "248289761001", Email: "ada@example.com", EmailVerified: true, Name: "Ada Lovelace"}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /keys", p.keys)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	p.Server = httptest.NewServer(mux)
	p.Issuer = p.URL
	return p
}

// SignInAs sets who the next authorization signs in.
func (p *Provider) SignInAs(who Person) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.As = who
}

// Break sets Tweak for the following ID tokens; nil restores them.
func (p *Provider) Break(tweak func(map[string]any)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Tweak = tweak
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer": p.Issuer, "authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token",
		"jwks_uri": p.URL + "/keys", "response_types_supported": []string{"code"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *Provider) keys(w http.ResponseWriter, _ *http.Request) {
	n := base64.RawURLEncoding.EncodeToString(p.key.N.Bytes())
	writeJSON(w, map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig", "n": n, "e": "AQAB"}}})
}

func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != p.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	code := randomString()
	p.mu.Lock()
	p.SeenScope = q.Get("scope")
	p.codes[code] = pending{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), as: p.As}
	p.mu.Unlock()
	to, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect", http.StatusBadRequest)
		return
	}
	v := to.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	to.RawQuery = v.Encode()
	http.Redirect(w, r, to.String(), http.StatusFound) //nolint:gosec // a test fake: it sends the browser back to the client's own redirect URI
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.ClientID || secret != p.ClientSecret {
		tokenError(w, "invalid_client")
		return
	}
	p.mu.Lock()
	pend, found := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	tweak, alg := p.Tweak, p.Alg
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || pend.redirect != r.PostForm.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != pend.challenge {
		tokenError(w, "invalid_grant")
		return
	}
	access := randomString()
	ah := sha256.Sum256([]byte(access))
	now := time.Now()
	claims := map[string]any{
		"iss": p.Issuer, "sub": pend.as.Subject, "aud": p.ClientID, "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
		"nonce": pend.nonce, "email": pend.as.Email, "email_verified": pend.as.EmailVerified, "name": pend.as.Name,
		"at_hash": base64.RawURLEncoding.EncodeToString(ah[:16]),
	}
	if pend.as.HostedDomain != "" {
		claims["hd"] = pend.as.HostedDomain
	}
	if tweak != nil {
		tweak(claims)
		for k, v := range claims {
			if v == "-" {
				delete(claims, k)
			}
		}
	}
	raw, _ := json.Marshal(claims)
	var idToken string
	if alg == "HS256" {
		idToken = oidctest.SignIDToken([]byte(p.ClientSecret), "k1", "HS256", string(raw))
	} else {
		idToken = oidctest.SignIDToken(p.key, "k1", "RS256", string(raw))
	}
	writeJSON(w, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 300, "id_token": idToken})
}

func tokenError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func randomString() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprint("oidcfake: ", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
