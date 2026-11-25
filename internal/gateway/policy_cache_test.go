package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstream"
)

// spyPolicyStore wraps *store.Store to count policy/setting/version reads
// and inject failures on demand.
type spyPolicyStore struct {
	*store.Store
	versionCalls atomic.Int64
	listCalls    atomic.Int64
	settingCalls atomic.Int64

	mu           sync.Mutex
	failVersion  error
	failList     error
	failDefault  error
	onBeforeList func() // optional hook to simulate concurrent policy write
}

func (s *spyPolicyStore) PolicyVersion(ctx context.Context, org store.OrgID) (int64, error) {
	s.versionCalls.Add(1)
	s.mu.Lock()
	err := s.failVersion
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return s.Store.PolicyVersion(ctx, org)
}

func (s *spyPolicyStore) ListPolicies(ctx context.Context, org store.OrgID) ([]store.Policy, error) {
	s.listCalls.Add(1)
	s.mu.Lock()
	err := s.failList
	hook := s.onBeforeList
	s.onBeforeList = nil
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err != nil {
		return nil, err
	}
	return s.Store.ListPolicies(ctx, org)
}

func (s *spyPolicyStore) Setting(ctx context.Context, org store.OrgID, key, def string) (string, error) {
	if key == SettingPolicyDefault {
		s.settingCalls.Add(1)
		s.mu.Lock()
		err := s.failDefault
		s.mu.Unlock()
		if err != nil {
			return "", err
		}
	}
	return s.Store.Setting(ctx, org, key, def)
}

// uncachedPolicyStore implements gateway.Store without policyVersionStore so
// BenchmarkGatewayDecide can compare uncached vs version-cached decide().
type uncachedPolicyStore struct {
	st *store.Store
}

func (u uncachedPolicyStore) PinStatuses(ctx context.Context, org store.OrgID, serverID string) (map[store.PinKey]string, error) {
	return u.st.PinStatuses(ctx, org, serverID)
}
func (u uncachedPolicyStore) Setting(ctx context.Context, org store.OrgID, key, def string) (string, error) {
	return u.st.Setting(ctx, org, key, def)
}
func (u uncachedPolicyStore) ListPolicies(ctx context.Context, org store.OrgID) ([]store.Policy, error) {
	return u.st.ListPolicies(ctx, org)
}
func (u uncachedPolicyStore) UserByID(ctx context.Context, org store.OrgID, id string) (store.User, error) {
	return u.st.UserByID(ctx, org, id)
}
func (u uncachedPolicyStore) AccessFor(ctx context.Context, org store.OrgID, userID string) (store.Access, error) {
	return u.st.AccessFor(ctx, org, userID)
}

type noopAuditStore struct{}

func (noopAuditStore) InsertAudit(context.Context, store.OrgID, ...store.AuditEvent) error {
	return nil
}

func newTestGateway(t testing.TB, st Store) *Gateway {
	t.Helper()
	aw := audit.NewWriter(noopAuditStore{}, audit.Options{})
	t.Cleanup(aw.Close)
	return &Gateway{
		Store:  st,
		Audit:  aw,
		Logger: slog.New(slog.DiscardHandler),
	}
}

func setupPolicyTestStore(t testing.TB) (*store.Store, store.OrgID, store.OrgID) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a, err := st.EnsureOrg(ctx, "org-a", "Org A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.EnsureOrg(ctx, "org-b", "Org B")
	if err != nil {
		t.Fatal(err)
	}
	return st, a.ID, b.ID
}

func testCall(org store.OrgID) call {
	return call{
		p:      auth.Principal{OrgID: org, TokenID: "tok-1", Roles: []string{store.RoleMember}},
		item:   upstream.Item{Alias: "github", ServerID: "srv-1", Name: "search"},
		action: "tools/call",
		args:   []byte(`{}`),
	}
}

func TestRuleChangeAppliesToTheNextCall(t *testing.T) {
	ctx := context.Background()
	st, orgA, _ := setupPolicyTestStore(t)
	spy := &spyPolicyStore{Store: st}
	g := newTestGateway(t, spy)

	// Cold call loads once and caches.
	c := testCall(orgA)
	dec, err := g.decide(ctx, c, "d1", "c1")
	if err != nil || dec.label != store.ActionAllow {
		t.Fatalf("cold decide = %+v, %v; want allow", dec, err)
	}
	if spy.listCalls.Load() != 1 || spy.settingCalls.Load() != 1 {
		t.Fatalf("after 2 calls: list=%d setting=%d, want 1, 1 (cache hit on second call)", spy.listCalls.Load(), spy.settingCalls.Load())
	}

	// Many repeated calls still hit cache with zero ListPolicies/Setting reads.
	for range 10 {
		dec, err := g.decide(ctx, c, "d1", "c1")
		if err != nil || dec.label != store.ActionAllow {
			t.Fatalf("cached decide = %+v, %v", dec, err)
		}
	}
	if spy.listCalls.Load() != 1 || spy.settingCalls.Load() != 1 {
		t.Fatalf("after 12 calls: list=%d setting=%d, want 1, 1", spy.listCalls.Load(), spy.settingCalls.Load())
	}

	// Adding a deny rule applies to the very next call.
	now := time.Now()
	rule := store.Policy{ID: "r-deny", Server: "github", Tool: "search", Subject: "*", Action: store.ActionDeny, CreatedBy: "admin", CreatedAt: now}
	if err := st.CreatePolicies(ctx, orgA, rule); err != nil {
		t.Fatal(err)
	}
	if _, err := g.decide(ctx, c, "d2", "c2"); err == nil {
		t.Fatal("expected deny refusal immediately after CreatePolicies")
	} else {
		var r *refusal
		if !errors.As(err, &r) {
			t.Fatalf("expected refusal, got %v", err)
		}
	}
	if spy.listCalls.Load() != 2 || spy.settingCalls.Load() != 2 {
		t.Fatalf("after rule create: list=%d setting=%d, want 2, 2", spy.listCalls.Load(), spy.settingCalls.Load())
	}

	// Deleting the rule applies to the very next call.
	if err := st.DeletePolicy(ctx, orgA, "r-deny"); err != nil {
		t.Fatal(err)
	}
	if dec, err := g.decide(ctx, c, "d3", "c3"); err != nil || dec.label != store.ActionAllow {
		t.Fatalf("after DeletePolicy: %+v, %v; want allow", dec, err)
	}
	if spy.listCalls.Load() != 3 {
		t.Fatalf("after DeletePolicy: list=%d, want 3", spy.listCalls.Load())
	}

	// Changing policy.default applies to the very next call.
	if err := st.SetSetting(ctx, orgA, SettingPolicyDefault, store.ActionDeny); err != nil {
		t.Fatal(err)
	}
	if _, err := g.decide(ctx, c, "d4", "c4"); err == nil {
		t.Fatal("expected deny immediately after SetSetting(policy.default)")
	}
	if err := st.SetSetting(ctx, orgA, SettingPolicyDefault, store.ActionAllow); err != nil {
		t.Fatal(err)
	}

	// An expiring rule stops matching at its exact expiry without waiting for any version bump.
	exp := time.Now().Add(40 * time.Millisecond)
	expRule := store.Policy{ID: "r-exp", Server: "github", Tool: "search", Subject: "*", Action: store.ActionDeny, CreatedBy: "admin", CreatedAt: now, ExpiresAt: &exp}
	if err := st.CreatePolicies(ctx, orgA, expRule); err != nil {
		t.Fatal(err)
	}
	if _, err := g.decide(ctx, c, "d5", "c5"); err == nil {
		t.Fatal("expiring rule should deny before expiry")
	}
	time.Sleep(60 * time.Millisecond)
	if dec, err := g.decide(ctx, c, "d6", "c6"); err != nil || dec.label != store.ActionAllow {
		t.Fatalf("expired cached rule still matched: %+v, %v", dec, err)
	}

	// Concurrent write during a cache load never poisons the newer version with old rules.
	spy.mu.Lock()
	spy.onBeforeList = func() {
		// While ListPolicies reads or right before it returns, bump policy to deny.
		_ = st.CreatePolicies(ctx, orgA, store.Policy{ID: "r-race", Server: "github", Tool: "search", Subject: "*", Action: store.ActionDeny, CreatedBy: "admin", CreatedAt: time.Now()})
	}
	spy.mu.Unlock()
	// Bump once so next call misses cache and triggers onBeforeList.
	if err := st.SetSetting(ctx, orgA, SettingPolicyDefault, store.ActionAllow); err != nil {
		t.Fatal(err)
	}
	if _, err := g.decide(ctx, c, "d7", "c7"); err == nil {
		t.Fatal("concurrent write during load was poisoned into cache; expected deny")
	}

	// Stampede test: concurrent callers on a version bump only load once.
	if err := st.DeletePolicy(ctx, orgA, "r-race"); err != nil {
		t.Fatal(err)
	}
	beforeLoads := spy.listCalls.Load()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if dec, err := g.decide(ctx, c, "d8", "c8"); err != nil || dec.label != store.ActionAllow {
				t.Errorf("concurrent decide = %+v, %v", dec, err)
			}
		}()
	}
	wg.Wait()
	if got := spy.listCalls.Load() - beforeLoads; got != 1 {
		t.Fatalf("stampede loaded policies %d times, want 1", got)
	}
}

func TestCacheReadFailureRefuses(t *testing.T) {
	ctx := context.Background()
	st, orgA, _ := setupPolicyTestStore(t)
	spy := &spyPolicyStore{Store: st}
	g := newTestGateway(t, spy)
	c := testCall(orgA)

	// Warm the cache at version 1 (ActionAllow).
	if dec, err := g.decide(ctx, c, "d1", "c1"); err != nil || dec.label != store.ActionAllow {
		t.Fatalf("warm cache: %+v, %v", dec, err)
	}

	// 1. PolicyVersion failure must refuse even when cache already holds v1.
	spy.mu.Lock()
	spy.failVersion = errors.New("db version read failed")
	spy.mu.Unlock()
	if _, err := g.decide(ctx, c, "d2", "c2"); err == nil {
		t.Fatal("PolicyVersion error fell back to cached rules")
	}
	spy.mu.Lock()
	spy.failVersion = nil
	spy.mu.Unlock()

	// 2. ListPolicies failure after version bump must refuse and never fall back to v1 or empty rules.
	if err := st.CreatePolicies(ctx, orgA, store.Policy{ID: "r1", Server: "github", Tool: "search", Subject: "*", Action: store.ActionDeny, CreatedBy: "admin", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	spy.mu.Lock()
	spy.failList = errors.New("db list failed")
	spy.mu.Unlock()
	if _, err := g.decide(ctx, c, "d3", "c3"); err == nil {
		t.Fatal("ListPolicies error fell back to stale/empty rules")
	}
	spy.mu.Lock()
	spy.failList = nil
	spy.mu.Unlock()

	// 3. Setting failure must refuse and never assume ActionAllow.
	spy.mu.Lock()
	spy.failDefault = errors.New("db setting failed")
	spy.mu.Unlock()
	if _, err := g.decide(ctx, c, "d4", "c4"); err == nil {
		t.Fatal("Setting error fell back to default allow")
	}
	spy.mu.Lock()
	spy.failDefault = nil
	spy.mu.Unlock()

	// 4. Recovery once DB works: loads v2 and enforces deny rule (cache was not poisoned by failed load).
	err := func() error {
		_, err := g.decide(ctx, c, "d5", "c5")
		return err
	}()
	var r *refusal
	if !errors.As(err, &r) {
		t.Fatalf("after recovery: got %v, want policy refusal", err)
	}
}

func TestPolicyCacheIsIsolatedPerOrg(t *testing.T) {
	ctx := context.Background()
	st, orgA, orgB := setupPolicyTestStore(t)
	spy := &spyPolicyStore{Store: st}
	g := newTestGateway(t, spy)

	// Org A denies "search"; Org B allows "search".
	if err := st.CreatePolicies(ctx, orgA, store.Policy{ID: "pa", Server: "github", Tool: "search", Subject: "*", Action: store.ActionDeny, CreatedBy: "admin", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePolicies(ctx, orgB, store.Policy{ID: "pb", Server: "github", Tool: "search", Subject: "*", Action: store.ActionAllow, CreatedBy: "admin", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	// Warm Org A -> denied.
	if _, err := g.decide(ctx, testCall(orgA), "da", "ca"); err == nil {
		t.Fatal("org A should deny")
	}
	// Org B must still allow and load its own rules.
	if dec, err := g.decide(ctx, testCall(orgB), "db", "cb"); err != nil || dec.label != store.ActionAllow {
		t.Fatalf("org B leaked org A's policy: %+v, %v", dec, err)
	}

	// Bumping Org A's policy does not invalidate Org B's cache.
	listsBefore := spy.listCalls.Load()
	if err := st.SetSetting(ctx, orgA, SettingPolicyDefault, store.ActionDeny); err != nil {
		t.Fatal(err)
	}
	if dec, err := g.decide(ctx, testCall(orgB), "db2", "cb2"); err != nil || dec.label != store.ActionAllow {
		t.Fatalf("org B after org A update: %+v, %v", dec, err)
	}
	if spy.listCalls.Load() != listsBefore {
		t.Fatalf("org B unnecessarily reloaded policies after org A update: %d -> %d", listsBefore, spy.listCalls.Load())
	}
}

func TestPolicyCacheBoundsEntryCount(t *testing.T) {
	var c policyCache
	for i := range maxPolicyCacheOrgs + 1 {
		org := store.OrgID("org-" + string(rune('a'+i/676)) + string(rune('a'+(i/26)%26)) + string(rune('a'+i%26)))
		c.put(org, 1, nil, store.ActionAllow)
	}
	c.mu.Lock()
	n := len(c.elems)
	c.mu.Unlock()
	if n != maxPolicyCacheOrgs {
		t.Fatalf("policy cache size = %d, want %d", n, maxPolicyCacheOrgs)
	}
	if _, _, hit := c.get("org-aaa", 1); hit {
		t.Fatal("oldest org was not evicted from bounded policy cache")
	}
}

func BenchmarkGatewayDecide(b *testing.B) {
	ctx := context.Background()
	st, orgA, _ := setupPolicyTestStore(b)
	now := time.Now()
	rules := make([]store.Policy, 50)
	for i := range rules {
		rules[i] = store.Policy{
			ID:        "r-" + string(rune('a'+i/26)) + string(rune('a'+i%26)),
			Server:    "other-server",
			Tool:      "*",
			Subject:   "*",
			Action:    store.ActionAsk,
			Priority:  i,
			CreatedBy: "bench",
			CreatedAt: now,
		}
	}
	if err := st.CreatePolicies(ctx, orgA, rules...); err != nil {
		b.Fatal(err)
	}
	c := testCall(orgA)

	b.Run("Uncached", func(b *testing.B) {
		g := newTestGateway(b, uncachedPolicyStore{st: st})
		b.ResetTimer()
		for b.Loop() {
			if dec, err := g.decide(ctx, c, "d", "c"); err != nil || dec.label != store.ActionAllow {
				b.Fatalf("decide: %+v, %v", dec, err)
			}
		}
	})

	b.Run("CachedHit", func(b *testing.B) {
		g := newTestGateway(b, st)
		// Warm cache before timing.
		if dec, err := g.decide(ctx, c, "d", "c"); err != nil || dec.label != store.ActionAllow {
			b.Fatalf("warm: %+v, %v", dec, err)
		}
		b.ResetTimer()
		for b.Loop() {
			if dec, err := g.decide(ctx, c, "d", "c"); err != nil || dec.label != store.ActionAllow {
				b.Fatalf("decide: %+v, %v", dec, err)
			}
		}
	})
}
