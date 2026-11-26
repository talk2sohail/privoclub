package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/grants"
	"privo.club/privo/internal/limit"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
	"privo.club/privo/internal/token"
	"privo.club/privo/internal/upstream"
	"privo.club/privo/internal/upstreamauth"
)

type limitTokenStore struct {
	mu     sync.Mutex
	tokens map[string]store.APIToken
}

func (s *limitTokenStore) TokenByID(_ context.Context, id string) (store.APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[id]
	if !ok {
		return store.APIToken{}, store.ErrNotFound
	}
	return t, nil
}

func (s *limitTokenStore) issue(org store.OrgID, userID, name string) string {
	tok, err := token.New()
	if err != nil {
		panic(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens == nil {
		s.tokens = make(map[string]store.APIToken)
	}
	s.tokens[tok.ID] = store.APIToken{
		ID:         tok.ID,
		OrgID:      org,
		Name:       name,
		Role:       store.RoleMember,
		Kind:       store.KindGateway,
		UserID:     userID,
		SecretHash: tok.SecretHash,
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	}
	return tok.Plain
}

type limitMemStore struct {
	mu       sync.Mutex
	servers  map[store.OrgID][]store.Server
	pins     map[string]map[store.PinKey]string
	policies map[store.OrgID][]store.Policy
	defAct   string
}

func (s *limitMemStore) PinStatuses(_ context.Context, _ store.OrgID, serverID string) (map[store.PinKey]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[store.PinKey]string)
	for k, v := range s.pins[serverID] {
		out[k] = v
	}
	return out, nil
}

func (s *limitMemStore) Setting(_ context.Context, _ store.OrgID, key, def string) (string, error) {
	if key == SettingPolicyDefault && s.defAct != "" {
		return s.defAct, nil
	}
	return def, nil
}

func (s *limitMemStore) ListPolicies(_ context.Context, org store.OrgID) ([]store.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.Policy(nil), s.policies[org]...), nil
}

func (s *limitMemStore) UserByID(_ context.Context, _ store.OrgID, id string) (store.User, error) {
	return store.User{ID: id}, nil
}

func (s *limitMemStore) AccessFor(_ context.Context, org store.OrgID, userID string) (store.Access, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	srvs := make(map[string]bool)
	for _, srv := range s.servers[org] {
		srvs[srv.ID] = true
	}
	acc := store.Access{Servers: srvs, Teams: []string{store.TeamEveryone}}
	if userID != "" {
		acc.Roles = []string{store.RoleMember}
	}
	return acc, nil
}

func (s *limitMemStore) ListServers(_ context.Context, org store.OrgID) ([]store.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.Server(nil), s.servers[org]...), nil
}

func (s *limitMemStore) ServerCredentials(context.Context, store.OrgID, string) ([]store.Credential, error) {
	return nil, nil
}

func (s *limitMemStore) RecordSeen(_ context.Context, _ store.OrgID, pins []store.Pin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pins == nil {
		s.pins = make(map[string]map[store.PinKey]string)
	}
	for _, p := range pins {
		if s.pins[p.ServerID] == nil {
			s.pins[p.ServerID] = make(map[store.PinKey]string)
		}
		s.pins[p.ServerID][store.PinKey{Kind: p.Kind, Name: p.Name, Hash: p.Hash}] = store.PinAccepted
	}
	return nil
}

func (s *limitMemStore) PrunePending(context.Context, store.OrgID, string, []store.PinKey) (int, error) {
	return 0, nil
}

type holdApprovalStore struct {
	mu           sync.Mutex
	status       string
	restored     []string
	holdEntered  chan struct{}
	holdOnce     sync.Once
	holdReleased chan struct{}
}

func (a *holdApprovalStore) CreateApproval(context.Context, store.OrgID, store.Approval) error {
	return nil
}
func (a *holdApprovalStore) OpenApproval(context.Context, store.OrgID, store.ApprovalBinding, time.Time) (store.Approval, error) {
	a.mu.Lock()
	st := a.status
	a.mu.Unlock()
	return store.Approval{ID: "appr-1", Status: st, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (a *holdApprovalStore) GetApproval(context.Context, store.OrgID, string) (store.Approval, error) {
	if a.holdEntered != nil {
		a.holdOnce.Do(func() { close(a.holdEntered) })
	}
	if a.holdReleased != nil {
		<-a.holdReleased
	}
	a.mu.Lock()
	st := a.status
	a.mu.Unlock()
	return store.Approval{ID: "appr-1", Status: st, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (a *holdApprovalStore) ExpireStale(context.Context, store.OrgID, store.ApprovalBinding, time.Time) error {
	return nil
}
func (a *holdApprovalStore) CountPending(context.Context, store.OrgID, string, time.Time) (int, error) {
	return 0, nil
}
func (a *holdApprovalStore) RestoreApproval(_ context.Context, _ store.OrgID, id string) error {
	a.mu.Lock()
	a.restored = append(a.restored, id)
	a.mu.Unlock()
	return nil
}
func (a *holdApprovalStore) PurgeArguments(context.Context, store.OrgID, string) error { return nil }
func (a *holdApprovalStore) WithdrawApproval(context.Context, store.OrgID, string, string, string, time.Time) (bool, error) {
	return false, nil
}
func (a *holdApprovalStore) ConsumeApproval(context.Context, store.OrgID, string, time.Time) (bool, error) {
	return true, nil
}
func (a *holdApprovalStore) DecideApproval(context.Context, store.OrgID, string, string, string, time.Time) (bool, error) {
	return false, nil
}
func (a *holdApprovalStore) Setting(_ context.Context, _ store.OrgID, _, def string) (string, error) {
	return def, nil
}

// handlerRoundTripper dispatches HTTP requests to in-memory handlers by host
// without opening TCP sockets.
type handlerRoundTripper struct {
	mu     sync.RWMutex
	routes map[string]http.Handler
}

func (h *handlerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	h.mu.RLock()
	target := h.routes[r.URL.Host]
	h.mu.RUnlock()
	if target == nil {
		return nil, fmt.Errorf("no in-memory route for host %q", r.URL.Host)
	}
	rec := httptest.NewRecorder()
	target.ServeHTTP(rec, r)
	return rec.Result(), nil
}

func postMCP(h http.Handler, bearer string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "http://gateway.local/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGCRARateLimitPerTokenUserAndOrg(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := t0
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }

	ts := &limitTokenStore{}
	org1, org2 := store.OrgID("org-1"), store.OrgID("org-2")
	u1Tok1 := ts.issue(org1, "user-1", "u1-t1")
	u1Tok2 := ts.issue(org1, "user-1", "u1-t2")
	u2Tok1 := ts.issue(org1, "user-2", "u2-t1")
	anon1 := ts.issue(org1, "", "anon-1")
	anon2 := ts.issue(org1, "", "anon-2")
	o2Tok := ts.issue(org2, "user-1", "o2-t1")

	st := &limitMemStore{defAct: store.ActionAllow}
	sink := &memSink{}
	rec := telemetry.New(sink, telemetry.Options{})
	mgr := upstream.New(upstream.Options{})
	defer mgr.Close()

	gw := &Gateway{
		Store:        st,
		Upstream:     mgr,
		Audit:        audit.NewWriter(sink, audit.Options{}),
		Auth:         auth.New(ts),
		Logger:       slog.New(slog.DiscardHandler),
		Telemetry:    rec,
		TokenLimiter: limit.NewRateLimiter(2, 2, 100),
		UserLimiter:  limit.NewRateLimiter(3, 3, 100),
		OrgLimiter:   limit.NewRateLimiter(4, 4, 100),
		Now:          clock,
	}
	defer gw.Audit.Close()
	defer rec.Close()
	h := gw.Handler()

	pingBody := `{"jsonrpc":"2.0","id":1,"method":"ping"}`

	// u1Tok1: token burst=2 -> 2 succeed, 3rd denied by token limit.
	for i := range 2 {
		if r := postMCP(h, u1Tok1, pingBody); r.Code != http.StatusOK {
			t.Fatalf("u1Tok1 req %d: status %d, want 200: %s", i, r.Code, r.Body.String())
		}
	}
	if r := postMCP(h, u1Tok1, pingBody); r.Code != http.StatusTooManyRequests {
		t.Fatalf("u1Tok1 req 3: status %d, want 429", r.Code)
	}

	// u1Tok2 (same user-1): user-1 already used 2/3, so 1 succeeds and 2nd hits user limit (3).
	if r := postMCP(h, u1Tok2, pingBody); r.Code != http.StatusOK {
		t.Fatalf("u1Tok2 req 1: status %d, want 200", r.Code)
	}
	if r := postMCP(h, u1Tok2, pingBody); r.Code != http.StatusTooManyRequests {
		t.Fatalf("u1Tok2 req 2: status %d, want 429 (user limit)", r.Code)
	}

	// u2Tok1 (user-2 in org-1): org-1 used 3/4, so 1 succeeds and 2nd hits org limit (4).
	if r := postMCP(h, u2Tok1, pingBody); r.Code != http.StatusOK {
		t.Fatalf("u2Tok1 req 1: status %d, want 200", r.Code)
	}
	if r := postMCP(h, u2Tok1, pingBody); r.Code != http.StatusTooManyRequests {
		t.Fatalf("u2Tok1 req 2: status %d, want 429 (org limit)", r.Code)
	}

	// org-2 is completely isolated from org-1 even with identical user ID "user-1".
	for i := range 2 {
		if r := postMCP(h, o2Tok, pingBody); r.Code != http.StatusOK {
			t.Fatalf("o2Tok req %d: status %d, want 200", i, r.Code)
		}
	}

	// Memberless tokens (UserID == "") must not share a "" user bucket.
	// Advance clock by 1 minute on org-1 so org-1 resets, then test two anon tokens with user limit = 1.
	gw.OrgLimiter = limit.NewRateLimiter(100, 100, 100)
	gw.UserLimiter = limit.NewRateLimiter(1, 1, 100)
	if r := postMCP(h, anon1, pingBody); r.Code != http.StatusOK {
		t.Fatalf("anon1: status %d, want 200", r.Code)
	}
	if r := postMCP(h, anon2, pingBody); r.Code != http.StatusOK {
		t.Fatalf("anon2: status %d, want 200 (anon tokens must not share empty user bucket)", r.Code)
	}
}

func TestRateLimitReturns429WithRetryAfter(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	ts := &limitTokenStore{}
	orgSecret := store.OrgID("org-alpha-secret")
	tok := ts.issue(orgSecret, "user-secret-1", "client-secret")

	sink := &memSink{}
	rec := telemetry.New(sink, telemetry.Options{})
	mgr := upstream.New(upstream.Options{})
	defer mgr.Close()

	gw := &Gateway{
		Store:        &limitMemStore{defAct: store.ActionAllow},
		Upstream:     mgr,
		Audit:        audit.NewWriter(sink, audit.Options{}),
		Auth:         auth.New(ts),
		Logger:       slog.New(slog.DiscardHandler),
		Telemetry:    rec,
		TokenLimiter: limit.NewRateLimiter(10, 1, 100), // 6s emission, burst 1
		Now:          func() time.Time { return t0 },
	}
	h := gw.Handler()

	pingBody := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	if r := postMCP(h, tok, pingBody); r.Code != http.StatusOK {
		t.Fatalf("first request: status %d, want 200", r.Code)
	}
	r := postMCP(h, tok, pingBody)
	if r.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: status %d, want 429", r.Code)
	}
	ra := r.Header().Get("Retry-After")
	secs, err := strconv.Atoi(ra)
	if err != nil || secs < 1 {
		t.Fatalf("Retry-After = %q (err=%v), want integer >= 1", ra, err)
	}
	body := r.Body.String()
	for _, secret := range []string{string(orgSecret), "user-secret-1", "client-secret", tok} {
		if strings.Contains(body, secret) {
			t.Fatalf("429 response body leaked %q: %q", secret, body)
		}
	}

	rec.Close()
	gw.Audit.Close()

	var found bool
	for _, s := range sink.stats {
		if s.Outcome == store.OutcomeRefused && s.ErrorClass == "rate_limited" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected call stat with outcome=refused, error_class=rate_limited; got %+v", sink.stats)
	}
}

func newInMemoryUpstreamEnv(t *testing.T, slowBlock chan struct{}) (
	gw *Gateway,
	tokOrg1 string,
	tokOrg2 string,
	sink *memSink,
	rec *telemetry.Recorder,
) {
	t.Helper()
	org1, org2 := store.OrgID("org-1"), store.OrgID("org-2")
	ts := &limitTokenStore{}
	tokOrg1 = ts.issue(org1, "u1", "c1")
	tokOrg2 = ts.issue(org2, "u2", "c2")

	slowSrv := mcp.NewServer(&mcp.Implementation{Name: "slow", Version: "0.0.1"}, nil)
	mcp.AddTool(slowSrv, &mcp.Tool{Name: "work", Description: "blocks"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		select {
		case <-slowBlock:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "slow-ok"}}}, nil, nil
	})
	fastSrv := mcp.NewServer(&mcp.Implementation{Name: "fast", Version: "0.0.1"}, nil)
	mcp.AddTool(fastSrv, &mcp.Tool{Name: "ping", Description: "fast"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fast-ok"}}}, nil, nil
	})

	rt := &handlerRoundTripper{
		routes: map[string]http.Handler{
			"slow.local": mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return slowSrv }, &mcp.StreamableHTTPOptions{JSONResponse: true}),
			"fast.local": mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fastSrv }, &mcp.StreamableHTTPOptions{JSONResponse: true}),
		},
	}

	st := &limitMemStore{
		defAct: store.ActionAllow,
		servers: map[store.OrgID][]store.Server{
			org1: {
				{ID: "srv-slow", Alias: "slow", Transport: "http", Enabled: true, Config: store.ServerConfig{URL: "https://slow.local/mcp"}},
				{ID: "srv-fast", Alias: "fast", Transport: "http", Enabled: true, Config: store.ServerConfig{URL: "https://fast.local/mcp"}},
			},
			org2: {
				{ID: "srv-fast-2", Alias: "fast", Transport: "http", Enabled: true, Config: store.ServerConfig{URL: "https://fast.local/mcp"}},
			},
		},
	}

	sink = &memSink{}
	rec = telemetry.New(sink, telemetry.Options{})
	aw := audit.NewWriter(sink, audit.Options{})
	mgr := upstream.New(upstream.Options{
		Store:          st,
		HTTPClient:     &http.Client{Transport: rt},
		ConnectTimeout: time.Second,
		Logger:         slog.New(slog.DiscardHandler),
	})
	if err := mgr.Sync(context.Background(), org1); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Sync(context.Background(), org2); err != nil {
		t.Fatal(err)
	}
	mgr.WaitSettled(context.Background(), "srv-slow", 2*time.Second)
	mgr.WaitSettled(context.Background(), "srv-fast", 2*time.Second)
	mgr.WaitSettled(context.Background(), "srv-fast-2", 2*time.Second)
	for _, id := range []string{"srv-slow", "srv-fast", "srv-fast-2"} {
		if st, ok := mgr.State(id); !ok || st.Status != upstream.StatusUp {
			t.Fatalf("server %s not up: ok=%v state=%+v", id, ok, st)
		}
	}

	t.Cleanup(func() {
		rec.Close()
		aw.Close()
		mgr.Close()
	})

	gw = &Gateway{
		Store:     st,
		Upstream:  mgr,
		Audit:     aw,
		Auth:      auth.New(ts),
		Logger:    slog.New(slog.DiscardHandler),
		Telemetry: rec,
	}
	return gw, tokOrg1, tokOrg2, sink, rec
}

func callToolJSON(tool string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, tool)
}

func TestBulkheadIsolatesSlowServerFromHealthyServer(t *testing.T) {
	block := make(chan struct{})
	var closeBlock sync.Once
	t.Cleanup(func() { closeBlock.Do(func() { close(block) }) })
	gw, tok1, _, sink, rec := newInMemoryUpstreamEnv(t, block)
	gw.InFlight = limit.NewInFlight(limit.InFlightConfig{Global: 64, Org: 64, Upstream: 2})
	h := gw.Handler()

	// Saturate slow server's 2 bulkhead slots.
	started := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started <- struct{}{}
			r := postMCP(h, tok1, callToolJSON("slow__work"))
			if r.Code != http.StatusOK || strings.Contains(r.Body.String(), `"isError":true`) || strings.Contains(r.Body.String(), `"error":`) {
				t.Errorf("slow in-flight call failed: %d %s", r.Code, r.Body.String())
			}
		}()
	}
	<-started
	<-started
	// Wait briefly until both goroutines have entered the upstream handler.
	deadline := time.Now().Add(2 * time.Second)
	for gw.InFlight.Upstream("org-1", "srv-slow") < 2 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for slow bulkhead saturation")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 3rd call to slow is shed immediately as a refusal (in_flight_limit).
	start := time.Now()
	shedResp := postMCP(h, tok1, callToolJSON("slow__work"))
	if time.Since(start) > time.Second {
		t.Fatal("bulkhead shed did not return immediately")
	}
	if shedResp.Code != http.StatusOK || !strings.Contains(shedResp.Body.String(), `"isError":true`) {
		t.Fatalf("expected MCP tool error refusal on bulkhead shed, got %d: %s", shedResp.Code, shedResp.Body.String())
	}

	// Healthy "fast" server remains completely unaffected and responds fast.
	fastResp := postMCP(h, tok1, callToolJSON("fast__ping"))
	if fastResp.Code != http.StatusOK || !strings.Contains(fastResp.Body.String(), "fast-ok") {
		t.Fatalf("fast server affected by slow bulkhead: %d %s", fastResp.Code, fastResp.Body.String())
	}

	// Unblock slow calls and wait for them to release slots.
	closeBlock.Do(func() { close(block) })
	wg.Wait()
	if got := gw.InFlight.Upstream("org-1", "srv-slow"); got != 0 {
		t.Fatalf("upstream slots leaked: %d", got)
	}

	// Verify refusal stat recorded with outcome="refused", error_class="in_flight_limit".
	rec.Close()
	var sawBulkheadRefusal bool
	for _, s := range sink.stats {
		if s.Server == "slow" && s.Outcome == store.OutcomeRefused && s.ErrorClass == "in_flight_limit" {
			sawBulkheadRefusal = true
		}
	}
	if !sawBulkheadRefusal {
		t.Fatalf("missing bulkhead refusal stat in %+v", sink.stats)
	}
}

func TestGlobalAndOrgInFlightShedWith503(t *testing.T) {
	block := make(chan struct{})
	var closeBlock sync.Once
	t.Cleanup(func() { closeBlock.Do(func() { close(block) }) })
	gw, tokOrg1, tokOrg2, sink, rec := newInMemoryUpstreamEnv(t, block)
	gw.InFlight = limit.NewInFlight(limit.InFlightConfig{Global: 2, Org: 1, Upstream: 10})
	h := gw.Handler()

	// Hold 1 slot in org-1 via slow__work.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = postMCP(h, tokOrg1, callToolJSON("slow__work"))
	}()
	deadline := time.Now().Add(2 * time.Second)
	for gw.InFlight.Org("org-1") < 1 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for org-1 in-flight slot")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 2nd concurrent request in org-1 must be shed with 503 + Retry-After: 1 (org cap 1).
	rOrg := postMCP(h, tokOrg1, callToolJSON("fast__ping"))
	if rOrg.Code != http.StatusServiceUnavailable || rOrg.Header().Get("Retry-After") != "1" {
		t.Fatalf("org-1 overflow: status=%d Retry-After=%q, want 503, 1", rOrg.Code, rOrg.Header().Get("Retry-After"))
	}

	// org-2 can still acquire global slot #2, and while held, another org-2 call gets 503.
	st := gw.Store.(*limitMemStore)
	st.mu.Lock()
	st.servers["org-2"] = append(st.servers["org-2"], store.Server{
		ID: "srv-slow-2", Alias: "slow", Transport: "http", Enabled: true, Config: store.ServerConfig{URL: "https://slow.local/mcp"},
	})
	st.mu.Unlock()
	if err := gw.Upstream.Sync(context.Background(), "org-2"); err != nil {
		t.Fatal(err)
	}
	gw.Upstream.WaitSettled(context.Background(), "srv-slow-2", 2*time.Second)

	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = postMCP(h, tokOrg2, callToolJSON("slow__work"))
	}()
	deadline = time.Now().Add(2 * time.Second)
	for gw.InFlight.Global() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for global in-flight=2")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Now global is 2/2: any org gets 503 + Retry-After: 1.
	rGlob := postMCP(h, tokOrg2, callToolJSON("fast__ping"))
	if rGlob.Code != http.StatusServiceUnavailable || rGlob.Header().Get("Retry-After") != "1" {
		t.Fatalf("global overflow: status=%d Retry-After=%q, want 503, 1", rGlob.Code, rGlob.Header().Get("Retry-After"))
	}

	closeBlock.Do(func() { close(block) })
	wg.Wait()
	if gw.InFlight.Global() != 0 {
		t.Fatalf("global in-flight leaked: %d", gw.InFlight.Global())
	}

	rec.Close()
	var saw503Stat bool
	for _, s := range sink.stats {
		if s.Outcome == store.OutcomeRefused && s.ErrorClass == "in_flight_limit" && s.Method == "mcp" {
			saw503Stat = true
		}
	}
	if !saw503Stat {
		t.Fatalf("expected call stat for 503 shedding, got %+v", sink.stats)
	}
}

func TestToolErrorsAndPolicyDenialsDoNotTripBreaker(t *testing.T) {
	now := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	sink := &memSink{}
	rec := telemetry.New(sink, telemetry.Options{})
	aw := audit.NewWriter(sink, audit.Options{})
	st := &statStore{def: store.ActionAllow}
	mgr := upstream.New(upstream.Options{})
	breaker := limit.NewBreakerWithClock(2, 30*time.Second, clock)

	gw := &Gateway{
		Store:     st,
		Upstream:  mgr,
		Audit:     aw,
		Logger:    slog.New(slog.DiscardHandler),
		Telemetry: rec,
		Breaker:   breaker,
	}
	defer func() {
		rec.Close()
		aw.Close()
		mgr.Close()
	}()

	// 1. Verify classifyBreakerOutcome directly for all non-failure and failure error classes.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	nonFailures := map[string]struct {
		ctx context.Context
		err error
	}{
		"nil":                      {context.Background(), nil},
		"jsonrpc app error":        {context.Background(), &jsonrpc.Error{Code: -32602, Message: "invalid params"}},
		"http 400 jsonrpc":         {context.Background(), fmt.Errorf("calling: %w: %w: Bad Request", &jsonrpc.Error{Code: -32602, Message: "bad"}, errors.New("rejected"))},
		"policy refusal":           {context.Background(), &refusal{msg: "Denied", class: "denied"}},
		"grants not connected":     {context.Background(), grants.ErrNotConnected},
		"grants key refused":       {context.Background(), grants.ErrKeyRefused},
		"insufficient scope":       {context.Background(), &upstreamauth.InsufficientScopeError{Scopes: []string{"repo"}}},
		"malformed reply":          {context.Background(), fmt.Errorf("%w: content[0]", errMalformedReply)},
		"needs input":              {context.Background(), errNeedsInput},
		"client context canceled":  {canceledCtx, context.Canceled},
		"http 403 forbidden":       {context.Background(), errors.New(`calling "tools/call": Forbidden`)},
		"http 401 unauthorized":    {context.Background(), errors.New(`calling "tools/call": Unauthorized`)},
		"http 429 too many":        {context.Background(), fmt.Errorf(`%w: calling "tools/call": Too Many Requests`, &jsonrpc.Error{Code: -32005, Message: "rejected by transport"})},
		"sdk session missing 404":  {context.Background(), fmt.Errorf("calling: %w", mcp.ErrSessionMissing)},
		"accountError not connect": {context.Background(), gw.connectFirst("github")},
	}
	for name, tc := range nonFailures {
		if got := classifyBreakerOutcome(tc.ctx, tc.err); got == limit.OutcomeFailure {
			t.Errorf("%s classified as OutcomeFailure, must NEVER trip breaker", name)
		}
	}

	failures := map[string]error{
		"upstream unavailable": upstream.ErrUnavailable,
		"call timeout":         fmt.Errorf("%w (limit 10s)", errCallTimeout),
		"http 503":             fmt.Errorf(`%w: calling "tools/call": Service Unavailable`, &jsonrpc.Error{Code: -32005, Message: "rejected by transport"}),
		"http 500":             fmt.Errorf(`%w: calling "tools/call": Internal Server Error`, &jsonrpc.Error{Code: -32005, Message: "rejected by transport"}),
		"transport dial error": errors.New(`Post "https://x": dial tcp 10.0.0.1:443: connection refused`),
	}
	for name, err := range failures {
		if got := classifyBreakerOutcome(context.Background(), err); got != limit.OutcomeFailure {
			t.Errorf("%s classified as %v, want OutcomeFailure", name, got)
		}
	}

	// 2. Drive real calls through Gateway.run using an in-memory upstream server.
	var toolMode atomic.Int32 // 0=ok, 1=isError, 2=jsonrpcErr, 3=http503
	srv := mcp.NewServer(&mcp.Implementation{Name: "srv", Version: "0.0.1"}, nil)
	srv.AddTool(&mcp.Tool{Name: "op", Description: "op", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			switch toolMode.Load() {
			case 1:
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "tool-err"}}}, nil
			case 2:
				return nil, &jsonrpc.Error{Code: -32042, Message: "app error"}
			default:
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
			}
		})
	baseHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	rt := &handlerRoundTripper{
		routes: map[string]http.Handler{
			"app.local": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if toolMode.Load() == 3 {
					http.Error(w, "down", http.StatusServiceUnavailable)
					return
				}
				baseHandler.ServeHTTP(w, r)
			}),
		},
	}

	memSt := &limitMemStore{
		defAct: store.ActionAllow,
		servers: map[store.OrgID][]store.Server{
			"o": {{ID: "srv-app", Alias: "app", Transport: "http", Enabled: true, Config: store.ServerConfig{URL: "https://app.local/mcp"}}},
		},
	}
	mgr2 := upstream.New(upstream.Options{Store: memSt, HTTPClient: &http.Client{Transport: rt}, Logger: slog.New(slog.DiscardHandler)})
	if err := mgr2.Sync(context.Background(), "o"); err != nil {
		t.Fatal(err)
	}
	mgr2.WaitSettled(context.Background(), "srv-app", 5*time.Second)
	defer mgr2.Close()

	gw.Store = memSt
	gw.Upstream = mgr2
	c := call{
		p:      auth.Principal{OrgID: "o", TokenID: "tok", UserID: "u", Roles: []string{store.RoleMember}},
		item:   upstream.Item{ServerID: "srv-app", Alias: "app", Name: "op"},
		action: "tools/call",
	}
	runOp := func() error {
		return gw.run(context.Background(), c, func(ctx context.Context, s *mcp.ClientSession) (string, any, error) {
			res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "op"})
			if err != nil {
				return "error", nil, err
			}
			if res.IsError {
				return "tool_error", res, nil
			}
			return "ok", res, nil
		})
	}

	// 5 tool errors + 5 JSON-RPC errors + 5 policy denials: breaker stays Closed (0).
	toolMode.Store(1)
	for range 5 {
		if err := runOp(); err != nil {
			t.Fatalf("tool error returned err: %v", err)
		}
	}
	toolMode.Store(2)
	for range 5 {
		if err := runOp(); err == nil {
			t.Fatal("expected jsonrpc error")
		}
	}
	memSt.defAct = store.ActionDeny
	for range 5 {
		var r *refusal
		if err := runOp(); !errors.As(err, &r) || r.class != "denied" {
			t.Fatalf("expected policy denial, got %v", err)
		}
	}
	memSt.defAct = store.ActionAllow
	for _, st := range breaker.States() {
		if st.State != limit.Closed {
			t.Fatalf("breaker tripped on non-failures: %+v", breaker.States())
		}
	}

	// Approval holds must not occupy bulkhead slots either, and an approval
	// consumed before circuit_open must be restored.
	holdSt := &holdApprovalStore{
		status:       store.ApprovalPending,
		holdEntered:  make(chan struct{}),
		holdReleased: make(chan struct{}),
	}
	gw.InFlight = limit.NewInFlight(limit.InFlightConfig{Upstream: 1})
	gw.Approval = &approval.Service{Store: holdSt, Hold: 80 * time.Millisecond, Poll: 10 * time.Millisecond}
	memSt.defAct = store.ActionAsk

	var holdWG sync.WaitGroup
	holdWG.Add(1)
	go func() {
		defer holdWG.Done()
		_ = runOp()
	}()
	<-holdSt.holdEntered
	if got := gw.InFlight.Upstream("o", "srv-app"); got != 0 {
		t.Fatalf("approval hold occupied %d upstream bulkhead slots, want 0", got)
	}
	close(holdSt.holdReleased)
	holdWG.Wait()
	memSt.defAct = store.ActionAllow

	// Now 2 HTTP 503 responses trip breaker Closed -> Open.
	toolMode.Store(3)
	for i := range 2 {
		if err := runOp(); err == nil {
			t.Fatalf("503 call %d unexpectedly succeeded", i)
		}
	}
	if states := breaker.States(); len(states) != 1 || states[0].State != limit.Open {
		t.Fatalf("breaker state after 503s = %+v, want Open", states)
	}

	// Subsequent call is refused immediately with circuit_open without calling upstream.
	var ref *refusal
	if err := runOp(); !errors.As(err, &ref) || ref.class != "circuit_open" {
		t.Fatalf("expected circuit_open refusal, got %v", err)
	}

	// An approved call blocked by circuit_open restores its consumed approval.
	holdSt.mu.Lock()
	holdSt.status = store.ApprovalApproved
	holdSt.mu.Unlock()
	memSt.defAct = store.ActionAsk
	if err := runOp(); !errors.As(err, &ref) || ref.class != "circuit_open" {
		t.Fatalf("expected circuit_open refusal on approved call, got %v", err)
	}
	holdSt.mu.Lock()
	restoredCount := len(holdSt.restored)
	holdSt.mu.Unlock()
	if restoredCount != 1 {
		t.Fatalf("expected consumed approval to be restored on circuit_open refusal, got %d restores", restoredCount)
	}
	memSt.defAct = store.ActionAllow

	// Advance cooldown, upstream recovers -> probe succeeds -> Closed.
	advance(31 * time.Second)
	toolMode.Store(0)
	if err := runOp(); err != nil {
		t.Fatalf("half-open recovery call failed: %v", err)
	}
	if states := breaker.States(); len(states) != 1 || states[0].State != limit.Closed {
		t.Fatalf("breaker state after recovery = %+v, want Closed", states)
	}

	rec.Close()
	// Expect deduplicated CategoryConnection diagnostics for Open, HalfOpen, Closed.
	var openDiag, halfDiag, closedDiag bool
	for _, d := range sink.diags {
		if d.Category != store.CategoryConnection || d.Server != "app" {
			continue
		}
		switch {
		case strings.Contains(d.Message, "opened"):
			openDiag = true
		case strings.Contains(d.Message, "half-open"):
			halfDiag = true
		case strings.Contains(d.Message, "closed"):
			closedDiag = true
		}
	}
	if !openDiag || !halfDiag || !closedDiag {
		t.Fatalf("missing breaker diagnostics (open=%v half=%v closed=%v): %+v", openDiag, halfDiag, closedDiag, sink.diags)
	}
}
