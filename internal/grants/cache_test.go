package grants

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
	"privo.club/privo/internal/vault"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// memAS handles OAuth token and revoke requests completely in memory so tests
// run without opening loopback TCP sockets.
type memAS struct {
	mu        sync.Mutex
	refreshes atomic.Int32
	next      int
	answer    func(url.Values) (int, string)
	revoked   []string
}

func newMemAS() (*memAS, *http.Client) {
	m := &memAS{}
	cli := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			raw, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(raw))
			switch r.URL.Path {
			case "/token":
				m.refreshes.Add(1)
				m.mu.Lock()
				m.next++
				n := m.next
				ans := m.answer
				m.mu.Unlock()
				code, body := http.StatusOK, fmt.Sprintf(`{"access_token":"at-%d","token_type":"Bearer","refresh_token":"rt-%d","expires_in":3600}`, n, n)
				if ans != nil {
					code, body = ans(form)
				}
				return &http.Response{
					StatusCode: code,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			case "/revoke":
				m.mu.Lock()
				m.revoked = append(m.revoked, form.Get("token"))
				m.mu.Unlock()
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
		}),
	}
	return m, cli
}

type spyGrantStore struct {
	Store
	grantReads   atomic.Int64
	catalogReads atomic.Int64
	touchCalls   atomic.Int64

	mu        sync.Mutex
	failGrant error
}

func (s *spyGrantStore) Grant(ctx context.Context, org store.OrgID, serverID, userID string) (store.Grant, error) {
	s.grantReads.Add(1)
	s.mu.Lock()
	err := s.failGrant
	s.mu.Unlock()
	if err != nil {
		return store.Grant{}, err
	}
	return s.Store.Grant(ctx, org, serverID, userID)
}

func (s *spyGrantStore) CatalogGrant(ctx context.Context, org store.OrgID, serverID string) (store.Grant, error) {
	s.catalogReads.Add(1)
	return s.Store.CatalogGrant(ctx, org, serverID)
}

func (s *spyGrantStore) TouchGrant(ctx context.Context, org store.OrgID, serverID, userID string, now time.Time) error {
	s.touchCalls.Add(1)
	return s.Store.TouchGrant(ctx, org, serverID, userID, now)
}

type memEnv struct {
	st     *store.Store
	spy    *spyGrantStore
	svc    *Service
	kr     *keyring.Keyring
	org    store.OrgID
	org2   store.OrgID
	srv    store.Server
	srvKey store.Server
	srv2   store.Server
	as     *memAS
	now    atomic.Int64
	ada    store.User
	boss   store.User
	carol  store.User
	logs   *bytes.Buffer
}

func newMemEnv(t testing.TB) *memEnv {
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
	org2, err := st.EnsureOrg(ctx, "second", "Second")
	if err != nil {
		t.Fatal(err)
	}
	kek, _ := vault.NewKey()
	kr := keyring.New(st, vault.StaticKeyProvider(kek, nil))
	if err := kr.EnsureOrgKey(ctx, org.ID); err != nil {
		t.Fatal(err)
	}
	if err := kr.EnsureOrgKey(ctx, org2.ID); err != nil {
		t.Fatal(err)
	}

	as, httpCli := newMemAS()
	const asBase = "https://as.example"
	srv := store.Server{
		OrgID: org.ID, ID: "srv-1", Alias: "linear", Transport: "http", Enabled: true, CreatedAt: t0,
		Config: store.ServerConfig{
			URL: "https://mcp.example/linear", Auth: store.AuthOAuth,
			OAuth: &store.ServerOAuth{
				Issuer: asBase, AuthorizationEndpoint: asBase + "/authorize",
				TokenEndpoint: asBase + "/token", RevocationEndpoint: asBase + "/revoke",
				Resource: "https://mcp.example/linear", ClientID: "client-1", Registration: store.RegistrationAdmin,
			},
		},
	}
	srvKey := store.Server{
		OrgID: org.ID, ID: "srv-key", Alias: "acme", Transport: "http", Enabled: true, CreatedAt: t0,
		Config: store.ServerConfig{URL: "https://mcp.example/acme", Auth: store.AuthKey, Key: &store.ServerKey{Header: "X-Api-Key"}},
	}
	srv2 := store.Server{
		OrgID: org2.ID, ID: "srv-1", Alias: "linear", Transport: "http", Enabled: true, CreatedAt: t0,
		Config: srv.Config,
	}
	for _, s := range []store.Server{srv, srvKey, srv2} {
		if err := st.CreateServer(ctx, s, nil); err != nil {
			t.Fatal(err)
		}
	}

	ev := func(a string) store.AuditEvent {
		return store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: t0, Actor: "t", Action: a, Decision: "allow", Outcome: "ok"}
	}
	sess := func(id string) store.Session {
		return store.Session{ID: id, CSRF: "c", CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(time.Hour)}
	}
	_ = st.CreateSetupLink(ctx, org.ID, store.NewSetupLink{ID: "l", SecretHash: "lh", CreatedBy: "cli", Now: t0, ExpiresAt: t0.Add(time.Hour)}, ev("setup"))
	boss, err := st.ClaimSetup(ctx, org.ID, store.Claim{LinkID: "l", UserID: "u-boss", Person: "boss", Subject: "1", Login: "boss", DisplayName: "Boss", Session: sess("s1"), Now: t0}, ev("claim-boss"))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.CreateInvite(ctx, org.ID, store.NewInvite{ID: "i-ada", SecretHash: "iha", Person: "ada", Roles: []string{store.RoleMember}, CreatedBy: "boss", Now: t0, ExpiresAt: t0.Add(time.Hour)}, ev("inv-ada"))
	ada, err := st.ClaimInvite(ctx, org.ID, store.Claim{LinkID: "i-ada", UserID: "u-ada", Subject: "2", Login: "ada", DisplayName: "Ada", Session: sess("s2"), Now: t0}, ev("claim-ada"))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.CreateInvite(ctx, org.ID, store.NewInvite{ID: "i-carol", SecretHash: "ihc", Person: "carol", Roles: []string{store.RoleAdmin}, CreatedBy: "boss", Now: t0, ExpiresAt: t0.Add(time.Hour)}, ev("inv-carol"))
	carol, err := st.ClaimInvite(ctx, org.ID, store.Claim{LinkID: "i-carol", UserID: "u-carol", Subject: "3", Login: "carol", DisplayName: "Carol", Session: sess("s3"), Now: t0}, ev("claim-carol"))
	if err != nil {
		t.Fatal(err)
	}

	spy := &spyGrantStore{Store: st}
	logs := &bytes.Buffer{}
	pub, _ := url.Parse("https://privo.example")
	e := &memEnv{
		st: st, spy: spy, kr: kr, org: org.ID, org2: org2.ID,
		srv: srv, srvKey: srvKey, srv2: srv2, as: as,
		ada: ada, boss: boss, carol: carol, logs: logs,
	}
	e.now.Store(t0.UnixNano())
	e.svc = &Service{
		Store:     spy,
		Keys:      kr,
		PublicURL: pub,
		Now:       func() time.Time { return time.Unix(0, e.now.Load()).UTC() },
		Logger:    slog.New(slog.NewTextHandler(logs, nil)),
		Auth:      &upstreamauth.Client{HTTP: httpCli, Timeout: 2 * time.Second},
	}
	return e
}

func (e *memEnv) setNow(tm time.Time) { e.now.Store(tm.UnixNano()) }

func (e *memEnv) connect(t testing.TB, org store.OrgID, srv store.Server, u store.User, tok upstreamauth.Token) store.Grant {
	t.Helper()
	g, err := e.svc.Save(context.Background(), org, srv, u, tok, u.Has(store.RoleOwner) || u.Has(store.RoleAdmin),
		store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: t0, Actor: u.Person, Action: "upstream.connect:" + srv.Alias, Decision: "allow", Outcome: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGrantCacheAvoidsRepeatedDecryptAndDBReads(t *testing.T) {
	ctx := context.Background()
	e := newMemEnv(t)
	e.connect(t, e.org, e.srv, e.ada, upstreamauth.Token{AccessToken: "ada-at", RefreshToken: "ada-rt", Expiry: t0.Add(time.Hour)})

	// First AccessToken call reads DB once and caches.
	at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID)
	if err != nil || at != "ada-at" {
		t.Fatalf("first AccessToken = %q, %v", at, err)
	}
	if e.spy.grantReads.Load() != 1 {
		t.Fatalf("grantReads = %d, want 1", e.spy.grantReads.Load())
	}

	// Make DB Grant fail; subsequent hits still succeed with 0 DB reads and 0 decrypts.
	e.spy.mu.Lock()
	e.spy.failGrant = errors.New("db should not be called on cache hit")
	e.spy.mu.Unlock()

	for i := range 20 {
		got, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID)
		if err != nil || got != "ada-at" {
			t.Fatalf("hit %d: %q, %v", i, got, err)
		}
	}
	if e.spy.grantReads.Load() != 1 || e.spy.touchCalls.Load() != 0 {
		t.Fatalf("after 20 cache hits: grantReads=%d touchCalls=%d, want 1, 0", e.spy.grantReads.Load(), e.spy.touchCalls.Load())
	}
	e.spy.mu.Lock()
	e.spy.failGrant = nil
	e.spy.mu.Unlock()

	// Auth mode mismatch still fails closed even when cached!
	if got, err := e.svc.AccessToken(ctx, e.org, e.srvKey, e.ada.ID); !errors.Is(err, ErrNotConnected) || got != "" {
		// Wait: srvKey has ID "srv-key", test with same server ID switched to AuthKey:
		_ = got
	}
	switched := e.srv
	switched.Config.Auth = store.AuthKey
	switched.Config.Key = &store.ServerKey{Header: "X-Api-Key"}
	if got, err := e.svc.AccessToken(ctx, e.org, switched, e.ada.ID); !errors.Is(err, ErrNotConnected) || got != "" {
		t.Fatalf("cached OAuth grant used for AuthKey server: %q, %v", got, err)
	}

	// Org isolation: org2 with identical serverID "srv-1" and userID "u-ada" must NOT see org1's cached token.
	if _, err := e.svc.AccessToken(ctx, e.org2, e.srv2, e.ada.ID); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("org2 read org1's cached token: %v", err)
	}

	// CatalogAccessToken respects CatalogGrant and PassCatalog, never serving wrong user's token.
	ev := func() store.AuditEvent {
		return store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: t0, Actor: "t", Action: "k", Decision: "allow", Outcome: "ok"}
	}
	if _, err := e.svc.SaveKey(ctx, e.org, e.srvKey, e.boss, "boss-key", true, ev()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SaveKey(ctx, e.org, e.srvKey, e.carol, "carol-key", true, ev()); err != nil {
		t.Fatal(err)
	}
	uid, tok, err := e.svc.CatalogAccessToken(ctx, e.org, e.srvKey)
	if err != nil || uid != e.boss.ID || tok != "boss-key" {
		t.Fatalf("CatalogAccessToken initial = (%q, %q, %v)", uid, tok, err)
	}
	if moved, err := e.svc.PassCatalog(ctx, e.org, e.srvKey, e.boss.ID); err != nil || !moved {
		t.Fatalf("PassCatalog = %v, %v", moved, err)
	}
	uid, tok, err = e.svc.CatalogAccessToken(ctx, e.org, e.srvKey)
	if err != nil || uid != e.carol.ID || tok != "carol-key" {
		t.Fatalf("CatalogAccessToken after PassCatalog = (%q, %q, %v), want carol-key", uid, tok, err)
	}

	// Demoting carol promotes boss back to catalog owner; disconnecting boss leaves none.
	if _, err := e.st.SetUserRoles(ctx, e.org, e.carol.ID, []string{store.RoleMember}, ev()); err != nil {
		t.Fatal(err)
	}
	uid, tok, err = e.svc.CatalogAccessToken(ctx, e.org, e.srvKey)
	if err != nil || uid != e.boss.ID || tok != "boss-key" {
		t.Fatalf("CatalogAccessToken after demotion = (%q, %q, %v), want boss-key", uid, tok, err)
	}
	if err := e.svc.Disconnect(ctx, e.org, e.srvKey, e.boss.ID, ev()); err != nil {
		t.Fatal(err)
	}
	if uid, tok, err := e.svc.CatalogAccessToken(ctx, e.org, e.srvKey); !errors.Is(err, ErrNotConnected) || tok != "" {
		t.Fatalf("CatalogAccessToken after all managers disconnected = (%q, %q, %v), want ErrNotConnected", uid, tok, err)
	}

	// Bounded LRU cap (maxCacheEntries = 4096): inserting 4097 entries evicts the oldest.
	for i := range maxCacheEntries + 1 {
		uid := fmt.Sprintf("u-%04d", i)
		e.svc.putCachedIfFresh(cacheKey{org: e.org, serverID: e.srv.ID, userID: uid},
			store.Credential{ID: "c", Purpose: store.PurposeOAuthGrant, RowVersion: 1},
			upstreamauth.Token{AccessToken: "t-" + uid, Expiry: t0.Add(time.Hour)}, t0, e.svc.currentEpoch())
	}
	e.svc.mu.Lock()
	cacheLen := e.svc.cache.len()
	_, hasFirst := e.svc.cache.get(cacheKey{org: e.org, serverID: e.srv.ID, userID: "u-0000"})
	_, hasLast := e.svc.cache.get(cacheKey{org: e.org, serverID: e.srv.ID, userID: fmt.Sprintf("u-%04d", maxCacheEntries)})
	e.svc.mu.Unlock()
	if cacheLen != maxCacheEntries || hasFirst || !hasLast {
		t.Fatalf("LRU bound: len=%d hasFirst=%v hasLast=%v", cacheLen, hasFirst, hasLast)
	}
}

func TestGrantCacheEvictsOnDisconnectDisableEditAndRefresh(t *testing.T) {
	ctx := context.Background()
	e := newMemEnv(t)
	ev := func() store.AuditEvent {
		return store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: t0, Actor: "t", Action: "x", Decision: "allow", Outcome: "ok"}
	}

	// 1. Save evicts old cached token immediately.
	e.connect(t, e.org, e.srv, e.ada, upstreamauth.Token{AccessToken: "at-1", RefreshToken: "rt-1", Expiry: t0.Add(time.Hour)})
	if at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); err != nil || at != "at-1" {
		t.Fatal(at, err)
	}
	e.connect(t, e.org, e.srv, e.ada, upstreamauth.Token{AccessToken: "at-2", RefreshToken: "rt-2", Expiry: t0.Add(time.Hour)})
	if at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); err != nil || at != "at-2" {
		t.Fatalf("after Save: got %q, %v; want at-2", at, err)
	}

	// 2. SaveKey evicts old cached API key immediately.
	if _, err := e.svc.SaveKey(ctx, e.org, e.srvKey, e.ada, "key-1", false, ev()); err != nil {
		t.Fatal(err)
	}
	if k, err := e.svc.AccessToken(ctx, e.org, e.srvKey, e.ada.ID); err != nil || k != "key-1" {
		t.Fatal(k, err)
	}
	if _, err := e.svc.SaveKey(ctx, e.org, e.srvKey, e.ada, "key-2", false, ev()); err != nil {
		t.Fatal(err)
	}
	if k, err := e.svc.AccessToken(ctx, e.org, e.srvKey, e.ada.ID); err != nil || k != "key-2" {
		t.Fatalf("after SaveKey: got %q, %v; want key-2", k, err)
	}

	// 3. Disconnect evicts immediately.
	if err := e.svc.Disconnect(ctx, e.org, e.srvKey, e.ada.ID, ev()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AccessToken(ctx, e.org, e.srvKey, e.ada.ID); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("after Disconnect: %v, want ErrNotConnected", err)
	}

	// 4. EvictServer evicts all users for that server in that org, keeping other servers intact.
	e.connect(t, e.org, e.srv, e.boss, upstreamauth.Token{AccessToken: "boss-at", RefreshToken: "boss-rt", Expiry: t0.Add(time.Hour)})
	if _, err := e.svc.SaveKey(ctx, e.org, e.srvKey, e.boss, "boss-key", true, ev()); err != nil {
		t.Fatal(err)
	}
	// Warm caches for ada+boss on srv and boss on srvKey.
	_, _ = e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID)
	_, _ = e.svc.AccessToken(ctx, e.org, e.srv, e.boss.ID)
	_, _ = e.svc.AccessToken(ctx, e.org, e.srvKey, e.boss.ID)

	// Simulate EditServer(EndConnections) / DeleteServer on srv: DB deletes grants + EvictServer.
	if _, err := e.st.EditServer(ctx, e.org, e.srv.ID, store.ServerEdit{Version: e.srv.CreatedAt, Config: e.srv.Config, EndConnections: true}, []store.AuditEvent{ev()}); err != nil {
		t.Fatal(err)
	}
	e.svc.EvictServer(e.org, e.srv.ID)
	if _, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("ada after EvictServer: %v", err)
	}
	if _, err := e.svc.AccessToken(ctx, e.org, e.srv, e.boss.ID); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("boss after EvictServer: %v", err)
	}
	// boss on srvKey must stay cached (make DB fail to verify it came from cache).
	e.spy.mu.Lock()
	e.spy.failGrant = errors.New("db fail")
	e.spy.mu.Unlock()
	if k, err := e.svc.AccessToken(ctx, e.org, e.srvKey, e.boss.ID); err != nil || k != "boss-key" {
		t.Fatalf("unrelated server evicted: %q, %v", k, err)
	}
	e.spy.mu.Lock()
	e.spy.failGrant = nil
	e.spy.mu.Unlock()

	// 5. EvictUser (DisableUser) evicts every server for that user, keeping others intact.
	e.connect(t, e.org, e.srv, e.ada, upstreamauth.Token{AccessToken: "ada-at", RefreshToken: "ada-rt", Expiry: t0.Add(time.Hour)})
	if _, err := e.svc.SaveKey(ctx, e.org, e.srvKey, e.ada, "ada-key", false, ev()); err != nil {
		t.Fatal(err)
	}
	_, _ = e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID)
	_, _ = e.svc.AccessToken(ctx, e.org, e.srvKey, e.ada.ID)

	if err := e.st.DisableUser(ctx, e.org, e.ada.ID, t0, ev()); err != nil {
		t.Fatal(err)
	}
	e.svc.EvictUser(e.org, e.ada.ID)
	if _, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("ada srv after DisableUser: %v", err)
	}
	if _, err := e.svc.AccessToken(ctx, e.org, e.srvKey, e.ada.ID); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("ada srvKey after DisableUser: %v", err)
	}
	if k, err := e.svc.AccessToken(ctx, e.org, e.srvKey, e.boss.ID); err != nil || k != "boss-key" {
		t.Fatalf("boss after ada DisableUser: %q, %v", k, err)
	}

	// 6. Refresh paths: success updates/evicts, invalid_grant evicts, ErrServerOff evicts.
	e.connect(t, e.org, e.srv, e.boss, upstreamauth.Token{AccessToken: "old-boss", RefreshToken: "rt-boss", Expiry: t0.Add(time.Hour)})
	if at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.boss.ID); err != nil || at != "old-boss" {
		t.Fatal(at, err)
	}
	// RefreshAfterReject succeeds -> next AccessToken returns the refreshed token.
	at, err := e.svc.RefreshAfterReject(ctx, e.org, e.srv, e.boss.ID, "old-boss")
	if err != nil || at != "at-1" {
		t.Fatalf("RefreshAfterReject = %q, %v", at, err)
	}
	if got, err := e.svc.AccessToken(ctx, e.org, e.srv, e.boss.ID); err != nil || got != "at-1" {
		t.Fatalf("after RefreshAfterReject: got %q, %v", got, err)
	}
	// RefreshAfterReject with ErrServerOff evicts cache!
	if err := e.st.SetServerEnabled(ctx, e.org, e.srv.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RefreshAfterReject(ctx, e.org, e.srv, e.boss.ID, "at-1"); !errors.Is(err, ErrServerOff) {
		t.Fatalf("ErrServerOff: %v", err)
	}
	e.svc.mu.Lock()
	_, stillCached := e.svc.cache.get(cacheKey{org: e.org, serverID: e.srv.ID, userID: e.boss.ID})
	e.svc.mu.Unlock()
	if stillCached {
		t.Fatal("disabled server left token cached after RefreshAfterReject")
	}
	if err := e.st.SetServerEnabled(ctx, e.org, e.srv.ID, true); err != nil {
		t.Fatal(err)
	}
	// Warm again, then invalid_grant drops grant & evicts cache.
	if _, err := e.svc.AccessToken(ctx, e.org, e.srv, e.boss.ID); err != nil {
		t.Fatal(err)
	}
	e.as.mu.Lock()
	e.as.answer = func(url.Values) (int, string) { return 400, `{"error":"invalid_grant"}` }
	e.as.mu.Unlock()
	if _, err := e.svc.RefreshAfterReject(ctx, e.org, e.srv, e.boss.ID, "at-1"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("invalid_grant: %v", err)
	}
	if _, err := e.svc.AccessToken(ctx, e.org, e.srv, e.boss.ID); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("after invalid_grant: %v", err)
	}
}

func TestGrantCacheNeverServesExpiredToken(t *testing.T) {
	ctx := context.Background()
	e := newMemEnv(t)

	// Case 1: OAuth token expiring in 60s -> cached at most until Expiry - Skew (t0 + 30s).
	e.connect(t, e.org, e.srv, e.ada, upstreamauth.Token{AccessToken: "short-tok", RefreshToken: "rt-0", Expiry: t0.Add(time.Minute)})
	if at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); err != nil || at != "short-tok" {
		t.Fatalf("initial = %q, %v", at, err)
	}
	e.setNow(t0.Add(29 * time.Second))
	if at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); err != nil || at != "short-tok" || e.as.refreshes.Load() != 0 {
		t.Fatalf("before skew: %q, %v, refreshes=%d", at, err, e.as.refreshes.Load())
	}
	// At exact Skew boundary (t0 + 30s), cache must NOT serve short-tok; must refresh.
	e.setNow(t0.Add(30 * time.Second))
	if at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); err != nil || at != "at-1" || e.as.refreshes.Load() != 1 {
		t.Fatalf("at skew boundary: %q, %v, refreshes=%d", at, err, e.as.refreshes.Load())
	}

	// Case 2: OAuth token with long expiry (1h) is capped at maxCacheTTL (5m).
	e.setNow(t0)
	e.connect(t, e.org, e.srv, e.boss, upstreamauth.Token{AccessToken: "long-tok", RefreshToken: "rt-long", Expiry: t0.Add(time.Hour)})
	if at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.boss.ID); err != nil || at != "long-tok" {
		t.Fatal(at, err)
	}
	readsBefore := e.spy.grantReads.Load()
	e.setNow(t0.Add(maxCacheTTL - time.Second))
	if at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.boss.ID); err != nil || at != "long-tok" || e.spy.grantReads.Load() != readsBefore {
		t.Fatalf("before maxCacheTTL: %q, %v, reads=%d", at, err, e.spy.grantReads.Load())
	}
	e.setNow(t0.Add(maxCacheTTL))
	if at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.boss.ID); err != nil || at != "long-tok" || e.spy.grantReads.Load() != readsBefore+1 {
		t.Fatalf("at maxCacheTTL: %q, %v, reads=%d (want %d)", at, err, e.spy.grantReads.Load(), readsBefore+1)
	}

	// Case 3: API key with zero ExpiresAt is cached up to maxCacheTTL (5m).
	e.setNow(t0)
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: t0, Actor: "t", Action: "k", Decision: "allow", Outcome: "ok"}
	if _, err := e.svc.SaveKey(ctx, e.org, e.srvKey, e.ada, "api-key-1", false, ev); err != nil {
		t.Fatal(err)
	}
	if k, err := e.svc.AccessToken(ctx, e.org, e.srvKey, e.ada.ID); err != nil || k != "api-key-1" {
		t.Fatal(k, err)
	}
	readsBefore = e.spy.grantReads.Load()
	e.setNow(t0.Add(maxCacheTTL - time.Second))
	if k, err := e.svc.AccessToken(ctx, e.org, e.srvKey, e.ada.ID); err != nil || k != "api-key-1" || e.spy.grantReads.Load() != readsBefore {
		t.Fatalf("key before maxCacheTTL: %q, %v", k, err)
	}
	e.setNow(t0.Add(maxCacheTTL))
	if k, err := e.svc.AccessToken(ctx, e.org, e.srvKey, e.ada.ID); err != nil || k != "api-key-1" || e.spy.grantReads.Load() != readsBefore+1 {
		t.Fatalf("key at maxCacheTTL: %q, %v, reads=%d", k, err, e.spy.grantReads.Load())
	}
}

func TestLastUsedAtFlushedPeriodicallyAndOnClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := newMemEnv(t)
	e.svc.FlushInterval = 20 * time.Millisecond
	e.connect(t, e.org, e.srv, e.ada, upstreamauth.Token{AccessToken: "ada-at", RefreshToken: "ada-rt", Expiry: t0.Add(time.Hour)})

	e.svc.Start(ctx)
	defer e.svc.Close()

	// Repeated AccessToken calls do NOT synchronously call TouchGrant.
	t1 := t0.Add(2 * time.Minute)
	e.setNow(t1)
	for range 25 {
		if _, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Wait for periodic flush to coalesce into one TouchGrant write.
	eventually(t, "periodic touch flush", func() bool {
		return e.spy.touchCalls.Load() >= 1
	})
	if n := e.spy.touchCalls.Load(); n != 1 {
		t.Fatalf("touchCalls = %d, want 1 coalesced flush", n)
	}
	g, err := e.st.Grant(ctx, e.org, e.srv.ID, e.ada.ID)
	if err != nil || !g.LastUsedAt.Equal(t1) {
		t.Fatalf("LastUsedAt after periodic flush = %v (err %v), want %v", g.LastUsedAt, err, t1)
	}

	// Next use is flushed synchronously on Close().
	t2 := t0.Add(5 * time.Minute)
	e.setNow(t2)
	for range 10 {
		if _, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); err != nil {
			t.Fatal(err)
		}
	}
	e.svc.Close()
	if n := e.spy.touchCalls.Load(); n < 2 {
		t.Fatalf("touchCalls after Close = %d, want >= 2", n)
	}
	g, err = e.st.Grant(context.Background(), e.org, e.srv.ID, e.ada.ID)
	if err != nil || !g.LastUsedAt.Equal(t2) {
		t.Fatalf("LastUsedAt after Close = %v (err %v), want %v", g.LastUsedAt, err, t2)
	}
}

func BenchmarkGrantAccessToken(b *testing.B) {
	ctx := context.Background()
	e := newMemEnv(b)
	e.connect(b, e.org, e.srv, e.ada, upstreamauth.Token{AccessToken: "ada-at", RefreshToken: "ada-rt", Expiry: t0.Add(time.Hour)})

	b.Run("Uncached", func(b *testing.B) {
		for b.Loop() {
			e.svc.Evict(e.org, e.srv.ID, e.ada.ID)
			at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID)
			if err != nil || at != "ada-at" {
				b.Fatalf("AccessToken: %q, %v", at, err)
			}
		}
	})

	b.Run("CachedHit", func(b *testing.B) {
		if _, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for b.Loop() {
			at, err := e.svc.AccessToken(ctx, e.org, e.srv, e.ada.ID)
			if err != nil || at != "ada-at" {
				b.Fatalf("AccessToken: %q, %v", at, err)
			}
		}
	})
}
