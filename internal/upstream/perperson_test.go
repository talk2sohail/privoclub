package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/store"
)

var errNoGrant = errors.New("no connected account")

// bearerServer is an MCP server that needs a bearer token and whose tool
// "whoami" answers with the subject the token belongs to.
type bearerServer struct {
	ts       *httptest.Server
	mu       sync.Mutex
	subjects map[string]string // token -> subject
	seen     []string          // Authorization headers, in order
}

func newBearerServer(t *testing.T) *bearerServer {
	t.Helper()
	b := &bearerServer{subjects: map[string]string{}}
	servers := map[string]*mcp.Server{}
	serverFor := func(subject string) *mcp.Server {
		b.mu.Lock()
		defer b.mu.Unlock()
		if s, ok := servers[subject]; ok {
			return s
		}
		s := mcp.NewServer(&mcp.Implementation{Name: "bearer", Version: "0"}, nil)
		mcp.AddTool(s, &mcp.Tool{Name: "whoami", Description: "Who is calling."},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: subject}}}, nil, nil
			})
		servers[subject] = s
		return s
	}
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return serverFor(b.subject(r))
	}, nil)
	b.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		b.mu.Lock()
		b.seen = append(b.seen, auth)
		b.mu.Unlock()
		if b.subject(r) == "" {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(b.ts.Close)
	return b
}

func (b *bearerServer) subject(r *http.Request) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.subjects[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
}

func (b *bearerServer) allow(token, subject string) {
	b.mu.Lock()
	b.subjects[token] = subject
	b.mu.Unlock()
}

func (b *bearerServer) seenSince(n int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.seen[n:]...)
}

func (b *bearerServer) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.seen)
}

// fakeGrants hands out tokens per user; "" catalog means no catalog grant.
type fakeGrants struct {
	mu        sync.Mutex
	tokens    map[string]string // user -> token
	catalog   string            // user id of the catalog grant
	refreshed map[string]string // user -> token after a refresh
	refreshes atomic.Int32
}

func (f *fakeGrants) AccessToken(_ context.Context, _ store.OrgID, _ store.Server, userID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[userID]
	if !ok {
		return "", errNoGrant
	}
	return t, nil
}

func (f *fakeGrants) CatalogAccessToken(ctx context.Context, org store.OrgID, srv store.Server) (string, string, error) {
	f.mu.Lock()
	uid := f.catalog
	f.mu.Unlock()
	if uid == "" {
		return "", "", errNoGrant
	}
	t, err := f.AccessToken(ctx, org, srv, uid)
	return uid, t, err
}

func (f *fakeGrants) RefreshAfterReject(_ context.Context, _ store.OrgID, _ store.Server, userID, rejected string) (string, error) {
	f.refreshes.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	next, ok := f.refreshed[userID]
	if !ok || f.tokens[userID] != rejected {
		return f.tokens[userID], nil
	}
	f.tokens[userID] = next
	return next, nil
}

func (f *fakeGrants) set(user, token string) {
	f.mu.Lock()
	f.tokens[user] = token
	f.mu.Unlock()
}

func (f *fakeGrants) drop(user string) {
	f.mu.Lock()
	delete(f.tokens, user)
	f.mu.Unlock()
}

func oauthManager(t *testing.T, b *bearerServer, g *fakeGrants, opts ...func(*Options)) *Manager {
	t.Helper()
	fs := &fakeStore{servers: []store.Server{{OrgID: "o", ID: "s1", Alias: "linear", Transport: "http", Enabled: true,
		Config: store.ServerConfig{URL: b.ts.URL, Auth: store.AuthOAuth, OAuth: &store.ServerOAuth{Resource: b.ts.URL}}}}}
	o := Options{Store: fs, HTTPClient: loopClient(), Grants: g, RetryBase: time.Hour, RetryMax: time.Hour, ConnectTimeout: 5 * time.Second}
	for _, f := range opts {
		f(&o)
	}
	m := New(o)
	t.Cleanup(m.Close)
	if err := m.Sync(context.Background(), "o"); err != nil {
		t.Fatal(err)
	}
	m.WaitSettled(context.Background(), "s1", 5*time.Second)
	return m
}

func whoami(t *testing.T, s *mcp.ClientSession) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func TestCatalogReadWithTheAdminsGrant(t *testing.T) {
	b := newBearerServer(t)
	b.allow("boss-tok", "boss")
	g := &fakeGrants{tokens: map[string]string{"boss": "boss-tok", "ada": "ada-tok"}, catalog: "boss"}
	m := oauthManager(t, b, g)
	st, _ := m.State("s1")
	if st.Status != StatusUp || len(st.Items) != 1 || st.Items[0].Name != "whoami" {
		t.Fatalf("state %+v", st)
	}
	if !m.PerPerson("s1") {
		t.Fatal("oauth server not per person")
	}
	for _, h := range b.seenSince(0) {
		if h != "Bearer boss-tok" {
			t.Fatalf("catalog request carried %q", h)
		}
	}
}

func TestCatalogWaitsForAnAdminAndKickBringsItUp(t *testing.T) {
	b := newBearerServer(t)
	b.allow("boss-tok", "boss")
	g := &fakeGrants{tokens: map[string]string{}}
	m := oauthManager(t, b, g)
	st, _ := m.State("s1")
	if st.Status != StatusDown || !strings.Contains(st.Error, "/connect/linear") {
		t.Fatalf("without a catalog grant: %+v", st)
	}
	if b.count() != 0 {
		t.Fatal("the server was asked without a token")
	}
	g.set("boss", "boss-tok")
	g.mu.Lock()
	g.catalog = "boss"
	g.mu.Unlock()
	m.Kick("s1") // RetryBase is an hour: only the kick can bring it up now
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, _ := m.State("s1"); st.Status == StatusUp {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("kick did not reconnect")
}

func TestCallUsesTheCallersGrant(t *testing.T) {
	b := newBearerServer(t)
	b.allow("boss-tok", "boss")
	b.allow("ada-tok", "ada")
	g := &fakeGrants{tokens: map[string]string{"boss": "boss-tok", "ada": "ada-tok"}, catalog: "boss"}
	m := oauthManager(t, b, g)
	ctx := context.Background()

	n := b.count()
	sa, err := m.PersonSession(ctx, "o", "s1", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if who := whoami(t, sa); who != "ada" {
		t.Fatalf("ada's call ran as %q", who)
	}
	for _, h := range b.seenSince(n) {
		if h != "Bearer ada-tok" {
			t.Fatalf("ada's session sent %q", h)
		}
	}
	sb, _ := m.PersonSession(ctx, "o", "s1", "boss")
	if who := whoami(t, sb); who != "boss" {
		t.Fatalf("boss's call ran as %q", who)
	}
	// The pool hands back the same session.
	if again, _ := m.PersonSession(ctx, "o", "s1", "ada"); again != sa {
		t.Error("ada's session was not reused")
	}
	if m.PoolSize() != 2 {
		t.Errorf("pool size %d", m.PoolSize())
	}
	// Wrong org or a server that is not oauth.
	if _, err := m.PersonSession(ctx, "other", "s1", "ada"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("other org: %v", err)
	}
}

func TestCallNeverUsesAnotherPersonsGrant(t *testing.T) {
	b := newBearerServer(t)
	b.allow("boss-tok", "boss")
	g := &fakeGrants{tokens: map[string]string{"boss": "boss-tok"}, catalog: "boss"}
	m := oauthManager(t, b, g)
	n := b.count()
	if _, err := m.PersonSession(context.Background(), "o", "s1", "ada"); !errors.Is(err, errNoGrant) {
		t.Fatalf("ada without a grant: %v", err)
	}
	if b.count() != n {
		t.Fatal("a request was sent for a person without a grant")
	}
}

func TestDisconnectStopsAPooledSessionAtOnce(t *testing.T) {
	b := newBearerServer(t)
	b.allow("boss-tok", "boss")
	b.allow("ada-tok", "ada")
	g := &fakeGrants{tokens: map[string]string{"boss": "boss-tok", "ada": "ada-tok"}, catalog: "boss"}
	m := oauthManager(t, b, g)
	sa, err := m.PersonSession(context.Background(), "o", "s1", "ada")
	if err != nil {
		t.Fatal(err)
	}
	g.drop("ada")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	n := b.count()
	if _, err := sa.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"}); err == nil {
		t.Fatal("the pooled session still works after the grant was removed")
	}
	if b.count() != n {
		t.Fatal("a request went out without a grant")
	}
	if _, err := m.PersonSession(context.Background(), "o", "s1", "ada"); !errors.Is(err, errNoGrant) {
		t.Fatalf("new session after disconnect: %v", err)
	}
}

func TestRefreshOnceAfterInvalidTokenOnTheWire(t *testing.T) {
	b := newBearerServer(t)
	b.allow("boss-tok", "boss")
	b.allow("ada-fresh", "ada") // "ada-stale" is refused with invalid_token
	g := &fakeGrants{tokens: map[string]string{"boss": "boss-tok", "ada": "ada-stale"}, catalog: "boss",
		refreshed: map[string]string{"ada": "ada-fresh"}}
	m := oauthManager(t, b, g)
	sa, err := m.PersonSession(context.Background(), "o", "s1", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if who := whoami(t, sa); who != "ada" {
		t.Fatalf("after refresh the call ran as %q", who)
	}
	if n := g.refreshes.Load(); n != 1 {
		t.Fatalf("%d refreshes, want 1", n)
	}
	// A token that stays refused: one refresh attempt, then the 401 stands.
	b2 := newBearerServer(t)
	b2.allow("boss-tok", "boss")
	g2 := &fakeGrants{tokens: map[string]string{"boss": "boss-tok", "ada": "bad"}, catalog: "boss"}
	m2 := oauthManager(t, b2, g2)
	if _, err := m2.PersonSession(context.Background(), "o", "s1", "ada"); err == nil {
		t.Fatal("connected with a refused token")
	}
	if n := g2.refreshes.Load(); n < 1 || n > 2 {
		t.Fatalf("%d refreshes for a refused token", n)
	}
}

func TestBearerOnlySentToTheServerOrigin(t *testing.T) {
	got := make(chan string, 4)
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.WriteHeader(500)
	}))
	defer evil.Close()
	evilURL := strings.Replace(evil.URL, "127.0.0.1", "localhost", 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evilURL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer up.Close()
	g := &fakeGrants{tokens: map[string]string{"boss": "SECRET-TOKEN"}, catalog: "boss"}
	fs := &fakeStore{servers: []store.Server{{OrgID: "o", ID: "s1", Alias: "x", Transport: "http", Enabled: true,
		Config: store.ServerConfig{URL: up.URL, Auth: store.AuthOAuth, OAuth: &store.ServerOAuth{}}}}}
	m := New(Options{Store: fs, HTTPClient: loopClient(), Grants: g, RetryBase: time.Hour, ConnectTimeout: 2 * time.Second})
	defer m.Close()
	_ = m.Sync(context.Background(), "o")
	m.WaitSettled(context.Background(), "s1", 3*time.Second)
	select {
	case h := <-got:
		t.Fatalf("redirect target received a request (Authorization=%q)", h)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestSessionPoolIdleCloseAndCap(t *testing.T) {
	b := newBearerServer(t)
	toks := map[string]string{"boss": "boss-tok"}
	for _, u := range []string{"u1", "u2", "u3"} {
		b.allow(u+"-tok", u)
		toks[u] = u + "-tok"
	}
	b.allow("boss-tok", "boss")
	g := &fakeGrants{tokens: toks, catalog: "boss"}
	m := oauthManager(t, b, g, func(o *Options) { o.PoolCap = 2; o.PoolIdle = time.Hour })
	ctx := context.Background()
	s1, _ := m.PersonSession(ctx, "o", "s1", "u1")
	time.Sleep(5 * time.Millisecond)
	_, _ = m.PersonSession(ctx, "o", "s1", "u2")
	time.Sleep(5 * time.Millisecond)
	_, _ = m.PersonSession(ctx, "o", "s1", "u3")
	if n := m.PoolSize(); n != 2 {
		t.Fatalf("pool size %d, want the cap of 2", n)
	}
	// u1 was the least recently used: its session was closed.
	done := make(chan struct{})
	go func() { _ = s1.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the evicted session is still open")
	}
	m.reapIdle(time.Now().Add(2 * time.Hour))
	if n := m.PoolSize(); n != 0 {
		t.Fatalf("pool size after idle reap %d", n)
	}
	// ClosePerson and a server change empty the pool.
	_, _ = m.PersonSession(ctx, "o", "s1", "u1")
	m.ClosePerson("s1", "u1")
	if m.PoolSize() != 0 {
		t.Fatal("ClosePerson left the session")
	}
}

func TestConcurrentFirstCallsShareOneConnection(t *testing.T) {
	b := newBearerServer(t)
	b.allow("boss-tok", "boss")
	b.allow("ada-tok", "ada")
	g := &fakeGrants{tokens: map[string]string{"boss": "boss-tok", "ada": "ada-tok"}, catalog: "boss"}
	m := oauthManager(t, b, g)
	var wg sync.WaitGroup
	sessions := make([]*mcp.ClientSession, 10)
	for i := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sessions[i], _ = m.PersonSession(context.Background(), "o", "s1", "ada")
		}()
	}
	wg.Wait()
	for _, s := range sessions[1:] {
		if s == nil || s != sessions[0] {
			t.Fatal("concurrent first calls opened several sessions")
		}
	}
}
