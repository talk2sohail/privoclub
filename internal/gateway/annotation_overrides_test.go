package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/token"
	"privo.club/privo/internal/upstream"
	"privo.club/privo/internal/vault"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type pipeResponseWriter struct {
	mu          sync.Mutex
	header      http.Header
	snap        http.Header
	wroteHeader bool
	status      int
	pw          *io.PipeWriter
	ready       chan struct{}
}

func (w *pipeResponseWriter) Header() http.Header { return w.header }

func (w *pipeResponseWriter) WriteHeader(code int) {
	w.mu.Lock()
	if !w.wroteHeader {
		w.wroteHeader = true
		w.status = code
		w.snap = w.header.Clone()
		close(w.ready)
	}
	w.mu.Unlock()
}

func (w *pipeResponseWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pw.Write(p)
}

func (w *pipeResponseWriter) Flush() {
	w.WriteHeader(http.StatusOK)
}

func (w *pipeResponseWriter) finish() {
	w.WriteHeader(http.StatusOK)
	_ = w.pw.Close()
}

func handlerRoundTripper(h http.Handler, wg *sync.WaitGroup) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		pr, pw := io.Pipe()
		rw := &pipeResponseWriter{
			header: make(http.Header),
			pw:     pw,
			ready:  make(chan struct{}),
		}
		if wg != nil {
			wg.Add(1)
		}
		go func() {
			if wg != nil {
				defer wg.Done()
			}
			defer rw.finish()
			h.ServeHTTP(rw, r)
		}()
		select {
		case <-rw.ready:
			return &http.Response{
				StatusCode: rw.status,
				Header:     rw.snap,
				Body:       pr,
				Request:    r,
			}, nil
		case <-r.Context().Done():
			_ = pr.Close()
			return nil, r.Context().Err()
		}
	})
}

func bptr(v bool) *bool { return &v }

type overrideTestEnv struct {
	st      *store.Store
	org     store.OrgID
	srvID   string
	alias   string
	mgr     *upstream.Manager
	gw      *Gateway
	aw      *audit.Writer
	tok     string
	upSrv   atomic.Pointer[mcp.Server]
	upWG    sync.WaitGroup
	gwWG    sync.WaitGroup
	session *mcp.ClientSession
}

func newOverrideTestEnv(t *testing.T, up *mcp.Server) *overrideTestEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	orgRec, err := st.EnsureOrg(ctx, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	org := orgRec.ID
	kek, _ := vault.NewKey()
	kr := keyring.New(st, vault.StaticKeyProvider(kek, nil))
	if err := kr.EnsureOrgKey(ctx, org); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	aw := audit.NewWriter(st, audit.Options{Logger: logger})

	env := &overrideTestEnv{st: st, org: org, srvID: "srv-1", alias: "notes", aw: aw}
	env.upSrv.Store(up)

	upHTTP := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return env.upSrv.Load()
	}, &mcp.StreamableHTTPOptions{Stateless: true, Logger: logger})

	upClient := &http.Client{
		Transport: handlerRoundTripper(upHTTP, &env.upWG),
	}

	mgr := upstream.New(upstream.Options{
		Store:      st,
		Keys:       kr,
		HTTPClient: upClient,
		RetryBase:  20 * time.Millisecond,
		RetryMax:   40 * time.Millisecond,
		Logger:     logger,
	})
	env.mgr = mgr

	srv := store.Server{
		OrgID:     org,
		ID:        env.srvID,
		Alias:     env.alias,
		Transport: "http",
		Config:    store.ServerConfig{URL: "http://notes.internal/mcp"},
		Enabled:   true,
		CreatedAt: time.Now(),
		Teams:     []string{store.TeamEveryone},
	}
	if err := st.CreateServer(ctx, srv, nil); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Sync(ctx, org); err != nil {
		t.Fatal(err)
	}
	mgr.WaitSettled(ctx, env.srvID, 5*time.Second)

	tok, err := token.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rec := store.APIToken{
		OrgID:      org,
		ID:         tok.ID,
		Name:       "client",
		Role:       store.RoleMember,
		Kind:       store.KindGateway,
		Person:     "ada",
		SecretHash: tok.SecretHash,
		CreatedAt:  now,
		ExpiresAt:  now.Add(time.Hour),
	}
	if err := st.CreateToken(ctx, rec); err != nil {
		t.Fatal(err)
	}
	env.tok = tok.Plain

	env.gw = &Gateway{
		Store:    st,
		Upstream: mgr,
		Keys:     kr,
		Audit:    aw,
		Auth:     auth.New(st),
		Approval: &approval.Service{Store: st, Keys: kr, Hold: 10 * time.Millisecond, Poll: 5 * time.Millisecond},
		Logger:   logger,
		BaseURL:  "https://privo.example",
	}

	t.Cleanup(func() {
		if env.session != nil {
			_ = env.session.Close()
		}
		env.gwWG.Wait()
		aw.Close()
		mgr.Close()
		env.upWG.Wait()
		_ = st.Close()
	})
	return env
}

func (e *overrideTestEnv) acceptAll(t *testing.T) map[string]store.Pin {
	t.Helper()
	ctx := context.Background()
	pins, err := e.st.ListPins(ctx, e.org, e.srvID)
	if err != nil {
		t.Fatal(err)
	}
	var keys []store.PinKey
	for _, p := range pins {
		if p.Status == store.PinPending || p.Status == store.PinBlocked {
			keys = append(keys, store.PinKey{Kind: p.Kind, Name: p.Name, Hash: p.Hash})
		}
	}
	if len(keys) > 0 {
		if _, err := e.st.AcceptPins(ctx, e.org, e.srvID, keys); err != nil {
			t.Fatal(err)
		}
	}
	pins, err = e.st.ListPins(ctx, e.org, e.srvID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.Pin{}
	for _, p := range pins {
		if p.Status == store.PinAccepted {
			out[p.Name] = p
		}
	}
	return out
}

func (e *overrideTestEnv) newClient(t *testing.T) *mcp.ClientSession {
	t.Helper()
	handler := e.gw.Handler()
	rt := handlerRoundTripper(handler, &e.gwWG)
	gwClient := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer "+e.tok)
			return rt.RoundTrip(r)
		}),
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             "http://gateway.internal/mcp",
		HTTPClient:           gwClient,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("gateway connect: %v", err)
	}
	return s
}

func (e *overrideTestEnv) client(t *testing.T) *mcp.ClientSession {
	t.Helper()
	if e.session != nil {
		return e.session
	}
	e.session = e.newClient(t)
	return e.session
}

func (e *overrideTestEnv) listTools(t *testing.T) map[string]*mcp.Tool {
	t.Helper()
	// Open a fresh MCP client session per list call so the SDK's 5s client-side
	// list cache (TTLMs=5000) never hides newly updated overrides.
	s := e.newClient(t)
	defer func() { _ = s.Close() }()
	res, err := s.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	out := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		out[tool.Name] = tool
	}
	return out
}

func (e *overrideTestEnv) callTool(t *testing.T, name string) (string, bool) {
	t.Helper()
	res, err := e.client(t).CallTool(context.Background(), &mcp.CallToolParams{Name: name})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if len(res.Content) == 0 {
		return "", res.IsError
	}
	tc, _ := res.Content[0].(*mcp.TextContent)
	if tc == nil {
		return "", res.IsError
	}
	return tc.Text, res.IsError
}

func notesServer(readDesc string, readAnn, writeAnn *mcp.ToolAnnotations) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "notes", Version: "1.0.0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "read_note", Description: readDesc, Annotations: readAnn},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "note-ok"}}}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "delete_note", Description: "Delete note", Annotations: writeAnn},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "deleted"}}}, nil, nil
		})
	return s
}

func TestOverrideShownToClients(t *testing.T) {
	up := notesServer("Read note", &mcp.ToolAnnotations{Title: "Reader", ReadOnlyHint: true}, &mcp.ToolAnnotations{DestructiveHint: bptr(true)})
	env := newOverrideTestEnv(t, up)
	accepted := env.acceptAll(t)

	tools := env.listTools(t)
	rn := tools["notes__read_note"]
	if rn == nil || rn.Annotations == nil || !rn.Annotations.ReadOnlyHint || rn.Annotations.DestructiveHint != nil {
		t.Fatalf("before override read_note = %+v", rn)
	}

	// Admin overrides read_note to non-read-only, destructive, closed-world.
	key := store.PinKey{Kind: "tool", Name: "read_note", Hash: accepted["read_note"].Hash}
	if err := env.st.SetPinAnnotations(context.Background(), env.org, env.srvID, key, store.AnnotationOverrides{
		ReadOnlyHint:    bptr(false),
		DestructiveHint: bptr(true),
		OpenWorldHint:   bptr(false),
	}); err != nil {
		t.Fatal(err)
	}

	tools = env.listTools(t)
	rn = tools["notes__read_note"]
	if rn.Annotations == nil || rn.Annotations.Title != "Reader" || rn.Annotations.ReadOnlyHint ||
		rn.Annotations.DestructiveHint == nil || !*rn.Annotations.DestructiveHint ||
		rn.Annotations.OpenWorldHint == nil || *rn.Annotations.OpenWorldHint {
		t.Fatalf("after override read_note annotations = %+v", rn.Annotations)
	}
	// Other tools keep their upstream annotations.
	dn := tools["notes__delete_note"]
	if dn.Annotations == nil || dn.Annotations.DestructiveHint == nil || !*dn.Annotations.DestructiveHint {
		t.Fatalf("delete_note annotations changed unexpectedly = %+v", dn.Annotations)
	}

	// Clearing readOnlyHint returns it to upstream's true while preserving destructiveHint=true.
	if err := env.st.SetPinAnnotations(context.Background(), env.org, env.srvID, key, store.AnnotationOverrides{
		DestructiveHint: bptr(true),
	}); err != nil {
		t.Fatal(err)
	}
	tools = env.listTools(t)
	rn = tools["notes__read_note"]
	if !rn.Annotations.ReadOnlyHint || rn.Annotations.DestructiveHint == nil || !*rn.Annotations.DestructiveHint || rn.Annotations.OpenWorldHint != nil {
		t.Fatalf("after partial clear read_note annotations = %+v", rn.Annotations)
	}
}

func TestRulesReadOverrides(t *testing.T) {
	ctx := context.Background()
	up := notesServer("Read note", &mcp.ToolAnnotations{ReadOnlyHint: true}, &mcp.ToolAnnotations{DestructiveHint: bptr(true)})
	env := newOverrideTestEnv(t, up)
	accepted := env.acceptAll(t)

	// Before override: read_note is allowed, delete_note asks for approval under default allow.
	if text, isErr := env.callTool(t, "notes__read_note"); isErr || text != "note-ok" {
		t.Fatalf("before override read_note = %q (err=%v)", text, isErr)
	}
	if text, isErr := env.callTool(t, "notes__delete_note"); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("before override delete_note = %q (err=%v), want Approval required", text, isErr)
	}

	// Override read_note as destructive -> default allow tightens to ask.
	if err := env.st.SetPinAnnotations(ctx, env.org, env.srvID, store.PinKey{Kind: "tool", Name: "read_note", Hash: accepted["read_note"].Hash}, store.AnnotationOverrides{
		ReadOnlyHint:    bptr(false),
		DestructiveHint: bptr(true),
	}); err != nil {
		t.Fatal(err)
	}
	if text, isErr := env.callTool(t, "notes__read_note"); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("overridden destructive read_note = %q (err=%v), want Approval required", text, isErr)
	}

	// Override delete_note as readOnly + non-destructive -> allowed under default allow.
	if err := env.st.SetPinAnnotations(ctx, env.org, env.srvID, store.PinKey{Kind: "tool", Name: "delete_note", Hash: accepted["delete_note"].Hash}, store.AnnotationOverrides{
		ReadOnlyHint:    bptr(true),
		DestructiveHint: bptr(false),
	}); err != nil {
		t.Fatal(err)
	}
	if text, isErr := env.callTool(t, "notes__delete_note"); isErr || text != "deleted" {
		t.Fatalf("overridden non-destructive delete_note = %q (err=%v), want deleted", text, isErr)
	}

	// Explicit deny rule still denies even when readOnlyHint is overridden to true.
	if err := env.st.CreatePolicies(ctx, env.org, store.Policy{
		ID: "deny-del", Server: env.alias, Tool: "delete_note", Subject: "*", Action: store.ActionDeny, Priority: 10, CreatedBy: "test", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if text, isErr := env.callTool(t, "notes__delete_note"); !isErr || !strings.Contains(text, "Denied") {
		t.Fatalf("explicit deny with readOnly override = %q (err=%v), want Denied", text, isErr)
	}
}

func TestOverrideFollowsTheAcceptedDefinition(t *testing.T) {
	ctx := context.Background()
	v1 := notesServer("Read note v1", &mcp.ToolAnnotations{ReadOnlyHint: true}, nil)
	env := newOverrideTestEnv(t, v1)
	acceptedV1 := env.acceptAll(t)
	h1 := acceptedV1["read_note"].Hash

	// Override v1 (h1) as destructive.
	if err := env.st.SetPinAnnotations(ctx, env.org, env.srvID, store.PinKey{Kind: "tool", Name: "read_note", Hash: h1}, store.AnnotationOverrides{
		ReadOnlyHint:    bptr(false),
		DestructiveHint: bptr(true),
	}); err != nil {
		t.Fatal(err)
	}
	if text, isErr := env.callTool(t, "notes__read_note"); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("v1 call = %q (err=%v), want Approval required", text, isErr)
	}

	// Upstream changes read_note (rug pull / new definition -> hash h2) and server reconnects.
	v2 := notesServer("Read note v2 — rug pull", &mcp.ToolAnnotations{ReadOnlyHint: true}, nil)
	env.upSrv.Store(v2)
	time.Sleep(2 * time.Millisecond)
	if err := env.st.SetServerEnabled(ctx, env.org, env.srvID, true); err != nil {
		t.Fatal(err)
	}
	if err := env.mgr.Sync(ctx, env.org); err != nil {
		t.Fatal(err)
	}
	env.mgr.WaitSettled(ctx, env.srvID, 5*time.Second)

	// Wait until upstream manager publishes v2 items (new hash).
	deadline := time.Now().Add(5 * time.Second)
	var h2 string
	for time.Now().Before(deadline) {
		pins, _ := env.st.ListPins(ctx, env.org, env.srvID)
		for _, p := range pins {
			if p.Name == "read_note" && p.Hash != h1 {
				h2 = p.Hash
			}
		}
		if h2 != "" {
			if st, ok := env.mgr.State(env.srvID); ok && st.Status == upstream.StatusUp {
				for _, it := range st.Items {
					if it.Name == "read_note" && it.Hash == h2 {
						goto sawV2
					}
				}
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("timed out waiting for upstream v2 load")
sawV2:

	// Before h2 is accepted, read_note is hidden from clients.
	tools := env.listTools(t)
	if tools["notes__read_note"] != nil {
		t.Fatalf("unaccepted v2 tool shown to client: %+v", tools["notes__read_note"])
	}

	// Once h2 is accepted, h2 MUST NOT inherit h1's overrides.
	if _, err := env.st.AcceptPins(ctx, env.org, env.srvID, []store.PinKey{{Kind: "tool", Name: "read_note", Hash: h2}}); err != nil {
		t.Fatal(err)
	}
	tools = env.listTools(t)
	rn := tools["notes__read_note"]
	if rn == nil || rn.Annotations == nil || !rn.Annotations.ReadOnlyHint || rn.Annotations.DestructiveHint != nil {
		t.Fatalf("accepted v2 inherited v1 overrides: %+v", rn)
	}
	if text, isErr := env.callTool(t, "notes__read_note"); isErr || text != "note-ok" {
		t.Fatalf("accepted v2 call = %q (err=%v), want note-ok", text, isErr)
	}

	// Switching back to v1 restores h1 with its own override intact.
	env.upSrv.Store(v1)
	time.Sleep(2 * time.Millisecond)
	if err := env.st.SetServerEnabled(ctx, env.org, env.srvID, true); err != nil {
		t.Fatal(err)
	}
	if err := env.mgr.Sync(ctx, env.org); err != nil {
		t.Fatal(err)
	}
	env.mgr.WaitSettled(ctx, env.srvID, 5*time.Second)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := env.mgr.State(env.srvID); ok && st.Status == upstream.StatusUp {
			for _, it := range st.Items {
				if it.Name == "read_note" && it.Hash == h1 {
					goto backToV1
				}
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("timed out waiting for upstream v1 reload")
backToV1:
	tools = env.listTools(t)
	rn = tools["notes__read_note"]
	if rn == nil || rn.Annotations == nil || rn.Annotations.ReadOnlyHint || rn.Annotations.DestructiveHint == nil || !*rn.Annotations.DestructiveHint {
		t.Fatalf("restored v1 lost its override: %+v", rn)
	}
}

func TestOverrideDoesNotMutateUpstreamCache(t *testing.T) {
	ctx := context.Background()
	up := notesServer(
		"Read note",
		&mcp.ToolAnnotations{Title: "KeepTitle", ReadOnlyHint: true, DestructiveHint: bptr(false), OpenWorldHint: bptr(false)},
		nil,
	)
	env := newOverrideTestEnv(t, up)
	accepted := env.acceptAll(t)

	if err := env.st.SetPinAnnotations(ctx, env.org, env.srvID, store.PinKey{Kind: "tool", Name: "read_note", Hash: accepted["read_note"].Hash}, store.AnnotationOverrides{
		ReadOnlyHint:    bptr(false),
		DestructiveHint: bptr(true),
		OpenWorldHint:   bptr(true),
	}); err != nil {
		t.Fatal(err)
	}

	// Trigger both listTools and decide.
	tools := env.listTools(t)
	rn := tools["notes__read_note"]
	if rn.Annotations == nil || rn.Annotations.ReadOnlyHint || !*rn.Annotations.DestructiveHint || !*rn.Annotations.OpenWorldHint {
		t.Fatalf("client annotations = %+v", rn.Annotations)
	}
	// Mutate the client-returned tool annotations too.
	rn.Annotations.Title = "Mutated"
	*rn.Annotations.DestructiveHint = false
	*rn.Annotations.OpenWorldHint = false

	_, _ = env.callTool(t, "notes__read_note")

	// Upstream cache must stay completely untouched.
	var cached *mcp.Tool
	for _, it := range env.mgr.Items(env.org) {
		if it.Name == "read_note" {
			cached = it.Tool
			break
		}
	}
	if cached == nil || cached.Annotations == nil {
		t.Fatalf("cached tool missing: %+v", cached)
	}
	if cached.Annotations.Title != "KeepTitle" || !cached.Annotations.ReadOnlyHint ||
		cached.Annotations.DestructiveHint == nil || *cached.Annotations.DestructiveHint ||
		cached.Annotations.OpenWorldHint == nil || *cached.Annotations.OpenWorldHint {
		t.Fatalf("upstream cache was mutated: %+v", cached.Annotations)
	}
}

type failingOverrideStore struct {
	Store
	err error
}

func (s *failingOverrideStore) PinAnnotationOverrides(context.Context, store.OrgID, string) (map[store.PinKey]store.AnnotationOverrides, error) {
	return nil, s.err
}

func TestAnnotationOverridesFailClosedOnStoreError(t *testing.T) {
	up := notesServer("Read note", &mcp.ToolAnnotations{ReadOnlyHint: true}, nil)
	env := newOverrideTestEnv(t, up)
	accepted := env.acceptAll(t)

	failGW := &Gateway{
		Store:    &failingOverrideStore{Store: env.st, err: errors.New("db unavailable")},
		Upstream: env.mgr,
		Logger:   slog.New(slog.DiscardHandler),
	}
	p := auth.Principal{OrgID: env.org, TokenID: "t1", Roles: []string{store.RoleMember}, Kind: store.KindGateway}
	if _, err := failGW.listTools(context.Background(), p); err == nil {
		t.Fatal("listTools succeeded when PinAnnotationOverrides failed")
	}
	c := call{
		p:      p,
		action: "tools/call",
		item: upstream.Item{
			ServerID: env.srvID,
			Alias:    env.alias,
			Kind:     upstream.KindTool,
			Name:     "read_note",
			Hash:     accepted["read_note"].Hash,
			Tool:     &mcp.Tool{Name: "read_note", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		},
	}
	if _, err := failGW.decide(context.Background(), c, "d", "c1"); err == nil {
		t.Fatal("decide succeeded when PinAnnotationOverrides failed")
	}
}
