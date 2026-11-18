package grants

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
	"privo.club/privo/internal/vault"
)

type fakeAS struct {
	srv       *httptest.Server
	mu        sync.Mutex
	refreshes atomic.Int32
	revoked   []string
	next      int
	answer    func(form url.Values) (int, string)
	delay     time.Duration
}

func newAS(t *testing.T) *fakeAS {
	f := &fakeAS{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/token":
			f.refreshes.Add(1)
			if f.delay > 0 {
				time.Sleep(f.delay)
			}
			f.mu.Lock()
			f.next++
			n := f.next
			ans := f.answer
			f.mu.Unlock()
			if ans != nil {
				code, body := ans(r.PostForm)
				w.WriteHeader(code)
				_, _ = w.Write([]byte(body))
				return
			}
			_, _ = fmt.Fprintf(w, `{"access_token":"at-%d","token_type":"Bearer","refresh_token":"rt-%d","expires_in":3600}`, n, n)
		case "/revoke":
			f.mu.Lock()
			f.revoked = append(f.revoked, r.PostForm.Get("token"))
			f.mu.Unlock()
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type env struct {
	svc  *Service
	st   *store.Store
	org  store.OrgID
	srv  store.Server
	as   *fakeAS
	now  time.Time
	logs *bytes.Buffer
	ada  store.User
	boss store.User
}

var t0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

var evSeq atomic.Int64

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, err := st.EnsureOrg(ctx, "default", "Default")
	if err != nil {
		t.Fatal(err)
	}
	kek, _ := vault.NewKey()
	kr := keyring.New(st, vault.StaticKeyProvider(kek, nil))
	if err := kr.EnsureOrgKey(ctx, org.ID); err != nil {
		t.Fatal(err)
	}
	as := newAS(t)
	srv := store.Server{OrgID: org.ID, ID: "srv-1", Alias: "linear", Transport: "http", Enabled: true, CreatedAt: t0,
		Config: store.ServerConfig{URL: as.srv.URL + "/mcp", Auth: store.AuthOAuth, OAuth: &store.ServerOAuth{
			Issuer: as.srv.URL, AuthorizationEndpoint: as.srv.URL + "/authorize", TokenEndpoint: as.srv.URL + "/token",
			RevocationEndpoint: as.srv.URL + "/revoke", Resource: as.srv.URL + "/mcp", ClientID: "client-1", Registration: store.RegistrationAdmin}}}
	if err := st.CreateServer(ctx, srv, nil); err != nil {
		t.Fatal(err)
	}
	e := &env{st: st, org: org.ID, srv: srv, as: as, now: t0, logs: &bytes.Buffer{}}
	pub, _ := url.Parse("https://privo.example")
	e.svc = &Service{Store: st, Keys: kr, PublicURL: pub, Now: func() time.Time { return e.now },
		Logger: slog.New(slog.NewTextHandler(e.logs, nil)),
		Auth:   &upstreamauth.Client{HTTP: egress.NewClient(egress.Options{Policy: egress.Policy{Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}}), Timeout: 2 * time.Second}}
	// An owner (through a setup link) and a member (through an invite).
	ev := func(a string) store.AuditEvent {
		return store.AuditEvent{ID: "ev-" + a, CallID: "c-" + a, Time: t0, Actor: "t", Action: a, Decision: "allow", Outcome: "ok"}
	}
	sess := func(id string) store.Session {
		return store.Session{ID: id, CSRF: "c", CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(time.Hour)}
	}
	_ = st.CreateSetupLink(ctx, org.ID, store.NewSetupLink{ID: "l", SecretHash: "lh", CreatedBy: "cli", Now: t0, ExpiresAt: t0.Add(time.Hour)}, ev("setup"))
	e.boss, err = st.ClaimSetup(ctx, org.ID, store.Claim{LinkID: "l", UserID: "u-boss", Person: "boss", Subject: "1", Login: "boss", DisplayName: "Boss", Session: sess("s1"), Now: t0}, ev("claim"))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.CreateInvite(ctx, org.ID, store.NewInvite{ID: "i", SecretHash: "ih", Person: "ada", Roles: []string{store.RoleMember}, CreatedBy: "boss", Now: t0, ExpiresAt: t0.Add(time.Hour)}, ev("invite"))
	e.ada, err = st.ClaimInvite(ctx, org.ID, store.Claim{LinkID: "i", UserID: "u-ada", Subject: "2", Login: "ada", DisplayName: "Ada", Session: sess("s2"), Now: t0}, ev("claim2"))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) connect(t *testing.T, u store.User, tok upstreamauth.Token) store.Grant {
	t.Helper()
	g, err := e.svc.Save(context.Background(), e.org, e.srv, u, tok, u.Has(store.RoleOwner),
		store.AuditEvent{ID: fmt.Sprintf("ev-c-%d", evSeq.Add(1)), CallID: "c", Time: t0, Actor: u.Person, Action: "upstream.connect:linear", Decision: "allow", Outcome: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestNotConnected(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.AccessToken(context.Background(), e.org, e.srv, e.ada.ID); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("no grant: %v", err)
	}
	if _, _, err := e.svc.CatalogAccessToken(context.Background(), e.org, e.srv); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("no catalog grant: %v", err)
	}
}

func TestSaveSealsBoundToOwnerAndServer(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.connect(t, e.ada, upstreamauth.Token{AccessToken: "ada-at", RefreshToken: "ada-rt", Expiry: t0.Add(time.Hour)})
	e.connect(t, e.boss, upstreamauth.Token{AccessToken: "boss-at", RefreshToken: "boss-rt", Expiry: t0.Add(time.Hour)})
	if at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); err != nil || at != "ada-at" {
		t.Fatalf("ada's token = %q, %v", at, err)
	}
	if uid, at, err := e.svc.CatalogAccessToken(ctx, e.org, e.srv); err != nil || at != "boss-at" || uid != e.boss.ID {
		t.Fatalf("catalog token = %q, %v (the owner's grant)", at, err)
	}
	// The ciphertext is bound to its owner: ada's sealed tokens presented as
	// boss's do not open.
	ga, _ := e.st.Grant(ctx, e.org, e.srv.ID, e.ada.ID)
	moved := ga
	moved.UserID = e.boss.ID
	moved.Credential.OwnerID = e.boss.ID
	if _, err := e.svc.token(ctx, e.org, moved); err == nil {
		t.Fatal("a grant moved to another person still opens")
	}
	moved = ga
	moved.Credential.ServerID = "other-server"
	if _, err := e.svc.token(ctx, e.org, moved); err == nil {
		t.Fatal("a grant moved to another server still opens")
	}
	if bytes.Contains(ga.Credential.Ciphertext, []byte("ada-at")) {
		t.Fatal("token stored in the clear")
	}
}

func TestRefreshBeforeExpiry(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.connect(t, e.ada, upstreamauth.Token{AccessToken: "old", RefreshToken: "rt-0", Expiry: t0.Add(time.Minute)})
	if at, _ := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); at != "old" || e.as.refreshes.Load() != 0 {
		t.Fatalf("fresh token refreshed: %q", at)
	}
	e.now = t0.Add(45 * time.Second) // within the 30 s skew
	at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID)
	if err != nil || at != "at-1" || e.as.refreshes.Load() != 1 {
		t.Fatalf("refreshed token = %q, %v, %d refreshes", at, err, e.as.refreshes.Load())
	}
	// The new pair is stored: no second refresh, and the next one uses rt-1.
	if at, _ := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); at != "at-1" || e.as.refreshes.Load() != 1 {
		t.Fatalf("second read %q", at)
	}
	g, _ := e.st.Grant(ctx, e.org, e.srv.ID, e.ada.ID)
	tok, _ := e.svc.token(ctx, e.org, g)
	if tok.RefreshToken != "rt-1" || g.Credential.RowVersion != 2 || !g.RefreshedAt.Equal(e.now) {
		t.Fatalf("stored after refresh: rv %d refreshed %v", g.Credential.RowVersion, g.RefreshedAt)
	}
}

func TestRefreshOnceAfterInvalidToken(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.connect(t, e.ada, upstreamauth.Token{AccessToken: "rejected", RefreshToken: "rt-0", Expiry: t0.Add(time.Hour)})
	at, err := e.svc.RefreshAfterReject(ctx, e.org, e.srv, e.ada.ID, "rejected")
	if err != nil || at != "at-1" {
		t.Fatalf("after reject: %q %v", at, err)
	}
	// A second caller that saw the same rejected token gets the new one
	// without another refresh.
	at, err = e.svc.RefreshAfterReject(ctx, e.org, e.srv, e.ada.ID, "rejected")
	if err != nil || at != "at-1" || e.as.refreshes.Load() != 1 {
		t.Fatalf("second reject: %q %v, %d refreshes", at, err, e.as.refreshes.Load())
	}
}

func TestConcurrentRefreshIsSingleFlight(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.as.delay = 50 * time.Millisecond
	e.connect(t, e.ada, upstreamauth.Token{AccessToken: "old", RefreshToken: "rt-0", Expiry: t0})
	var wg sync.WaitGroup
	got := make([]string, 20)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], _ = e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID)
		}()
	}
	wg.Wait()
	if n := e.as.refreshes.Load(); n != 1 {
		t.Fatalf("%d refreshes, want 1", n)
	}
	for i, at := range got {
		if at != "at-1" {
			t.Fatalf("caller %d got %q", i, at)
		}
	}
}

func TestRefreshSurvivesCallerCancel(t *testing.T) {
	e := newEnv(t)
	e.as.delay = 100 * time.Millisecond
	e.connect(t, e.ada, upstreamauth.Token{AccessToken: "old", RefreshToken: "rt-0", Expiry: t0})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _ = e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID)
	// The refresh finished and was stored although the caller left.
	time.Sleep(200 * time.Millisecond)
	g, _ := e.st.Grant(context.Background(), e.org, e.srv.ID, e.ada.ID)
	tok, _ := e.svc.token(context.Background(), e.org, g)
	if tok.RefreshToken != "rt-1" {
		t.Fatal("a rotated refresh token was lost when the caller went away")
	}
}

func TestInvalidGrantDropsTheGrant(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.as.answer = func(url.Values) (int, string) { return 400, `{"error":"invalid_grant"}` }
	e.connect(t, e.ada, upstreamauth.Token{AccessToken: "old", RefreshToken: "rt-0", Expiry: t0})
	if _, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("invalid grant: %v", err)
	}
	if _, err := e.st.Grant(ctx, e.org, e.srv.ID, e.ada.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused grant kept: %v", err)
	}
	evs, _ := e.st.RecentAdminEvents(ctx, e.org, 50)
	found := false
	for _, ev := range evs {
		if ev.Action == "upstream.grant_lost:linear" && ev.Actor == "system" {
			found = true
		}
	}
	if !found {
		t.Error("grant loss not audited")
	}
	// A transient failure keeps the grant.
	e.as.answer = func(url.Values) (int, string) { return 503, `busy` }
	e.connect(t, e.ada, upstreamauth.Token{AccessToken: "old2", RefreshToken: "rt-0", Expiry: t0})
	if _, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); err == nil || errors.Is(err, ErrNotConnected) {
		t.Fatalf("transient failure: %v", err)
	}
	if _, err := e.st.Grant(ctx, e.org, e.srv.ID, e.ada.ID); err != nil {
		t.Fatalf("grant dropped on a transient failure: %v", err)
	}
}

func TestRaceLoserReadsTheWinnersToken(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.connect(t, e.ada, upstreamauth.Token{AccessToken: "old", RefreshToken: "rt-0", Expiry: t0})
	// Another replica stores a refreshed pair while this one is at the AS.
	e.as.answer = func(url.Values) (int, string) {
		g, _ := e.st.Grant(ctx, e.org, e.srv.ID, e.ada.ID)
		b, _ := json.Marshal(upstreamauth.Token{AccessToken: "theirs", RefreshToken: "rt-x", Expiry: t0.Add(time.Hour)})
		c := g.Credential
		c.RowVersion++
		c, _ = e.svc.seal(ctx, e.org, c, b)
		if err := e.st.UpdateGrantToken(ctx, e.org, e.srv.ID, e.ada.ID, g.Credential.RowVersion, c, t0); err != nil {
			t.Error(err)
		}
		return 200, `{"access_token":"mine","token_type":"bearer","refresh_token":"rt-m","expires_in":3600}`
	}
	at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID)
	if err != nil || at != "theirs" {
		t.Fatalf("race loser got %q, %v; want the stored winner", at, err)
	}
}

func TestDisconnectRevokesAndDeletes(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.connect(t, e.ada, upstreamauth.Token{AccessToken: "at", RefreshToken: "rt", Expiry: t0.Add(time.Hour)})
	ev := store.AuditEvent{ID: "d", CallID: "d", Time: t0, Actor: "ada", Action: "upstream.disconnect:linear", Decision: "allow", Outcome: "ok"}
	if err := e.svc.Disconnect(ctx, e.org, e.srv, e.ada.ID, ev); err != nil {
		t.Fatal(err)
	}
	if len(e.as.revoked) != 1 || e.as.revoked[0] != "rt" {
		t.Fatalf("revoked %v, want the refresh token", e.as.revoked)
	}
	if _, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("after disconnect: %v", err)
	}
	// Revocation failing does not keep the grant.
	e.connect(t, e.ada, upstreamauth.Token{AccessToken: "at", RefreshToken: "rt", Expiry: t0.Add(time.Hour)})
	e.as.srv.Close()
	ev.ID = "d2"
	if err := e.svc.Disconnect(ctx, e.org, e.srv, e.ada.ID, ev); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Grant(ctx, e.org, e.srv.ID, e.ada.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("grant kept after a failed revocation")
	}
}

func TestClientSecretAndCIMDEndpoint(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	o := *e.srv.Config.OAuth
	o.HasClientSecret, o.TokenAuthMethod = true, "client_secret_post"
	sec, err := e.svc.SealClientSecret(ctx, e.org, e.srv.ID, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateServerOAuth(ctx, e.org, e.srv.ID, o, &sec); err != nil {
		t.Fatal(err)
	}
	srv, _ := e.st.ServerByAlias(ctx, e.org, "linear")
	ep, err := e.svc.Endpoint(ctx, e.org, srv)
	if err != nil || ep.ClientSecret != "s3cret" || ep.AuthMethod != "client_secret_post" || ep.ClientID != "client-1" {
		t.Fatalf("endpoint %+v %v", ep.AuthMethod, err)
	}
	o.Registration, o.HasClientSecret, o.TokenAuthMethod = store.RegistrationCIMD, false, ""
	_ = e.st.UpdateServerOAuth(ctx, e.org, e.srv.ID, o, nil)
	srv, _ = e.st.ServerByAlias(ctx, e.org, "linear")
	ep, err = e.svc.Endpoint(ctx, e.org, srv)
	if err != nil || ep.ClientID != "https://privo.example/oauth/client-metadata.json" || ep.ClientSecret != "" || ep.AuthMethod != "none" {
		t.Fatalf("CIMD endpoint %q %q %v", ep.ClientID, ep.AuthMethod, err)
	}
}

func TestTokensNeverInLogsAuditOrErrors(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.as.answer = func(f url.Values) (int, string) {
		return 500, `{"error":"server_error","error_description":"` + f.Get("refresh_token") + `"}`
	}
	e.connect(t, e.ada, upstreamauth.Token{AccessToken: "secret-access", RefreshToken: "secret-refresh", Expiry: t0})
	_, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error: %v", err)
	}
	e.as.answer = func(url.Values) (int, string) { return 400, `{"error":"invalid_grant"}` }
	_, _ = e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID)
	e.connect(t, e.ada, upstreamauth.Token{AccessToken: "secret-access2", RefreshToken: "secret-refresh2", Expiry: t0.Add(time.Hour)})
	e.as.srv.Close()
	_ = e.svc.Disconnect(ctx, e.org, e.srv, e.ada.ID, store.AuditEvent{ID: "x", CallID: "x", Time: t0, Actor: "a", Action: "upstream.disconnect:linear", Decision: "allow", Outcome: "ok"})
	if strings.Contains(e.logs.String(), "secret") {
		t.Errorf("logs hold a token: %s", e.logs.String())
	}
	evs, _ := e.st.RecentAdminEvents(ctx, e.org, 100)
	b, _ := json.Marshal(evs)
	if strings.Contains(string(b), "secret") {
		t.Errorf("audit holds a token")
	}
}
