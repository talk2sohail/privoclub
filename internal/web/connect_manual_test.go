package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
)

// handAS is an authorization server whose settings an admin entered by hand
// (ADR 0023): no metadata, no issuer. It hands out one code and records what
// reaches its token endpoint.
type handAS struct {
	srv          *httptest.Server
	mu           sync.Mutex
	code, verify string
}

func newHandAS(t *testing.T) *handAS {
	h := &handAS{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		h.mu.Lock()
		h.code, h.verify = r.PostForm.Get("code"), r.PostForm.Get("code_verifier")
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"AT-hand","refresh_token":"RT-hand","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (e *env) handServer(t *testing.T, h *handAS) {
	t.Helper()
	srv := store.Server{OrgID: e.org, ID: ids.New(), Alias: "inhouse", Transport: "http", Enabled: true, CreatedAt: e.clock.now(),
		Config: store.ServerConfig{URL: "https://inhouse.example/mcp", Auth: store.AuthOAuth, OAuth: &store.ServerOAuth{
			AuthorizationEndpoint: h.srv.URL + "/authorize", TokenEndpoint: h.srv.URL + "/token",
			ClientID: "hand-client", Registration: store.RegistrationManual, TokenAuthMethod: "none"}}}
	if err := e.st.CreateServer(context.Background(), srv, nil); err != nil {
		t.Fatal(err)
	}
}

func (e *env) startHand(t *testing.T, b *browser) url.Values {
	t.Helper()
	p := b.get("/connect/inhouse")
	start := b.post("/connect/inhouse/start", url.Values{"csrf": {csrfOf(t, p.Body)}})
	loc, err := url.Parse(start.Header.Get("Location"))
	if err != nil || start.StatusCode != http.StatusSeeOther {
		t.Fatalf("start: %d %q", start.StatusCode, start.Header.Get("Location"))
	}
	return loc.Query()
}

// The connect flow with settings entered by hand works, without an issuer.
func TestConnectWithSettingsEnteredByHand(t *testing.T) {
	e := newConnEnv(t)
	h := newHandAS(t)
	e.handServer(t, h)
	b, u := e.signedIn(ada, "ada", store.RoleMember)
	q := e.startHand(t, b)
	// No resource is known for these settings, so none is sent (ADR 0023).
	if q.Get("client_id") != "hand-client" || q.Get("code_challenge_method") != "S256" || q.Has("resource") {
		t.Fatalf("authorization request: %v", q)
	}
	cb := b.get("/connect/callback?code=c-hand&state=" + url.QueryEscape(q.Get("state")))
	if cb.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback: %d %s", cb.StatusCode, cb.Body)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.code != "c-hand" || h.verify == "" {
		t.Fatalf("token request: code %q verifier set %v", h.code, h.verify != "")
	}
	if _, err := e.st.Grant(context.Background(), e.org, mustServer(t, e, "inhouse").ID, u.ID); err != nil {
		t.Fatalf("no grant saved: %v", err)
	}
}

// Review of 2026-11-15, U-01: an answer naming an issuer is refused for a
// server with no issuer, so a code another authorization server issued (the
// mix-up) never reaches the hand-entered token endpoint.
func TestHandEnteredSettingsRefuseAnAnswerNamingAnIssuer(t *testing.T) {
	e := newConnEnv(t)
	h := newHandAS(t)
	e.handServer(t, h)
	b, _ := e.signedIn(ada, "ada", store.RoleMember)
	q := e.startHand(t, b)
	cb := b.get("/connect/callback?code=c-honest&state=" + url.QueryEscape(q.Get("state")) + "&iss=" + url.QueryEscape(e.as.srv.URL))
	if cb.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback with an issuer: %d", cb.StatusCode)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.code != "" {
		t.Fatal("a code from another authorization server reached the hand-entered token endpoint")
	}
}

func mustServer(t *testing.T, e *env, alias string) store.Server {
	t.Helper()
	srv, err := e.st.ServerByAlias(context.Background(), e.org, alias)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}
