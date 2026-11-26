package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
	"privo.club/privo/internal/token"
	"privo.club/privo/internal/upstream"
)

type pipeResponseWriter struct {
	mu      sync.Mutex
	hdr     http.Header
	snapHdr http.Header
	code    int
	ready   chan struct{}
	once    sync.Once
	pw      *io.PipeWriter
}

func (p *pipeResponseWriter) Header() http.Header { return p.hdr }

func (p *pipeResponseWriter) signal(code int) {
	p.once.Do(func() {
		p.mu.Lock()
		p.code = code
		p.snapHdr = p.hdr.Clone()
		p.mu.Unlock()
		close(p.ready)
	})
}

func (p *pipeResponseWriter) WriteHeader(code int) { p.signal(code) }

func (p *pipeResponseWriter) Write(b []byte) (int, error) {
	p.signal(http.StatusOK)
	return p.pw.Write(b)
}

func (p *pipeResponseWriter) Flush() { p.signal(http.StatusOK) }

type handlerRT struct {
	h  http.Handler
	wg *sync.WaitGroup
}

func (rt handlerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	pr, pw := io.Pipe()
	rw := &pipeResponseWriter{
		hdr:   make(http.Header),
		ready: make(chan struct{}),
		pw:    pw,
	}
	if rt.wg != nil {
		rt.wg.Add(1)
	}
	go func() {
		if rt.wg != nil {
			defer rt.wg.Done()
		}
		rt.h.ServeHTTP(rw, r)
		rw.signal(http.StatusOK)
		_ = pw.Close()
	}()
	select {
	case <-rw.ready:
	case <-r.Context().Done():
		_ = pr.Close()
		return nil, r.Context().Err()
	}
	rw.mu.Lock()
	code, hdr := rw.code, rw.snapHdr
	rw.mu.Unlock()
	return &http.Response{
		StatusCode: code,
		Status:     http.StatusText(code),
		Header:     hdr,
		Body:       pr,
		Request:    r,
	}, nil
}

// gwHarness wires a real Gateway, Upstream Manager, Store, Audit Writer, and
// in-memory HTTP upstream MCP server without opening TCP sockets.
type gwHarness struct {
	t        *testing.T
	st       *store.Store
	org      store.OrgID
	sink     *memSink
	aw       *audit.Writer
	rec      *telemetry.Recorder
	mgr      *upstream.Manager
	gw       *Gateway
	inflight sync.WaitGroup
}

func newTestGateway(t *testing.T, upstreams map[string]*mcp.Server) *gwHarness {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, err := st.EnsureOrg(ctx, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	sink := &memSink{}
	aw := audit.NewWriter(sink, audit.Options{FlushInterval: 10 * time.Millisecond})
	rec := telemetry.New(sink, telemetry.Options{})

	mux := http.NewServeMux()
	for alias, srv := range upstreams {
		h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
			Stateless:                  true,
			JSONResponse:               true,
			DisableLocalhostProtection: true,
		})
		mux.Handle("/"+alias+"/mcp", h)
	}
	mgr := upstream.New(upstream.Options{
		Store:      st,
		HTTPClient: &http.Client{Transport: handlerRT{h: mux}},
		RetryBase:  time.Hour,
		Logger:     slog.New(slog.DiscardHandler),
	})
	gw := &Gateway{
		Store:     st,
		Upstream:  mgr,
		Audit:     aw,
		Auth:      auth.New(st),
		Logger:    slog.New(slog.DiscardHandler),
		Telemetry: rec,
	}
	h := &gwHarness{t: t, st: st, org: org.ID, sink: sink, aw: aw, rec: rec, mgr: mgr, gw: gw}
	t.Cleanup(func() {
		h.inflight.Wait()
		h.rec.Close()
		h.aw.Close()
		h.mgr.Close()
	})
	return h
}

func (h *gwHarness) addServer(alias string, teams []string) string {
	h.t.Helper()
	ctx := context.Background()
	id := "srv-" + alias
	err := h.st.CreateServer(ctx, store.Server{
		OrgID:     h.org,
		ID:        id,
		Alias:     alias,
		Transport: "http",
		Config:    store.ServerConfig{URL: "http://upstream.example/" + alias + "/mcp"},
		Enabled:   true,
		CreatedAt: time.Now(),
		Teams:     teams,
	}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.mgr.Sync(ctx, h.org); err != nil {
		h.t.Fatal(err)
	}
	h.mgr.WaitSettled(ctx, id, 5*time.Second)
	st, ok := h.mgr.State(id)
	if !ok || st.Status != upstream.StatusUp {
		h.t.Fatalf("server %s status = %+v", alias, st)
	}
	return id
}

func (h *gwHarness) acceptPin(serverID, kind, name string) {
	h.t.Helper()
	ctx := context.Background()
	pins, err := h.st.ListPins(ctx, h.org, serverID)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, p := range pins {
		if p.Kind == kind && p.Name == name {
			if _, err := h.st.AcceptPins(ctx, h.org, serverID, []store.PinKey{{Kind: p.Kind, Name: p.Name, Hash: p.Hash}}); err != nil {
				h.t.Fatal(err)
			}
			return
		}
	}
	h.t.Fatalf("pin (%s, %s) not found on %s: %+v", kind, name, serverID, pins)
}

func (h *gwHarness) addMemberToken(userID, person string, teams ...string) (string, auth.Principal) {
	h.t.Helper()
	ctx := context.Background()
	now := time.Now()
	invID := "inv-" + userID
	if err := h.st.CreateInvite(ctx, h.org, store.NewInvite{
		ID:         invID,
		SecretHash: "h-" + invID,
		Person:     person,
		Roles:      []string{store.RoleMember},
		CreatedBy:  "test",
		Now:        now,
		ExpiresAt:  now.Add(time.Hour),
	}, store.AuditEvent{ID: "i-" + userID, CallID: "i-" + userID, Time: now, Actor: "test", Action: "invite.create", Decision: "allow", Outcome: "ok"}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.st.ClaimInvite(ctx, h.org, store.Claim{
		LinkID:      invID,
		UserID:      userID,
		Person:      person,
		Subject:     "gh-" + userID,
		Login:       person,
		DisplayName: person,
		Session:     store.Session{ID: "sess-" + userID, CSRF: "csrf-" + userID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)},
		Now:         now,
	}, store.AuditEvent{ID: "u-" + userID, CallID: "u-" + userID, Time: now, Actor: "test", Action: "invite.claim", Decision: "allow", Outcome: "ok"}); err != nil {
		h.t.Fatal(err)
	}
	for _, team := range teams {
		if err := h.st.AddTeamMember(ctx, h.org, team, userID, now, store.AuditEvent{
			ID: "tm-" + team + "-" + userID, CallID: "tm-" + team + "-" + userID, Time: now, Actor: "test", Action: "team.add", Decision: "allow", Outcome: "ok",
		}); err != nil {
			h.t.Fatal(err)
		}
	}
	tok, err := token.New()
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.st.CreateToken(ctx, store.APIToken{
		OrgID:      h.org,
		ID:         tok.ID,
		Name:       person + "-gw",
		Role:       store.RoleMember,
		Kind:       store.KindGateway,
		Person:     person,
		UserID:     userID,
		SecretHash: tok.SecretHash,
		CreatedAt:  now,
		ExpiresAt:  now.Add(time.Hour),
	}); err != nil {
		h.t.Fatal(err)
	}
	p := auth.Principal{
		OrgID:   h.org,
		TokenID: tok.ID,
		Name:    person + "-gw",
		Roles:   []string{store.RoleMember},
		Kind:    store.KindGateway,
		Person:  person,
		UserID:  userID,
	}
	return tok.Plain, p
}

type bearerRT struct {
	tok  string
	next http.RoundTripper
}

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return b.next.RoundTrip(r)
}

func (h *gwHarness) mcpSession(tok string) *mcp.ClientSession {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	h.t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "gw-test", Version: "0.0.0"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             "http://gateway.example/mcp",
		HTTPClient:           &http.Client{Transport: bearerRT{tok: tok, next: handlerRT{h: h.gw.Handler(), wg: &h.inflight}}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (h *gwHarness) rawRPC(tok, method string, params map[string]any) map[string]any {
	h.t.Helper()
	withMeta := map[string]any{}
	for k, v := range params {
		withMeta[k] = v
	}
	withMeta["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "gw-test", "version": "0.0.0"},
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": withMeta})
	if err != nil {
		h.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://gateway.example/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", method)
	if uri, ok := params["uri"].(string); ok {
		req.Header.Set("Mcp-Name", uri)
	}
	h.inflight.Add(1)
	rec := httptest.NewRecorder()
	h.gw.Handler().ServeHTTP(rec, req)
	h.inflight.Done()
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	payload := raw
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		payload = nil
		sc := bufio.NewScanner(bytes.NewReader(raw))
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
				payload = []byte(strings.TrimSpace(data))
				break
			}
		}
	}
	var out struct {
		Result map[string]any `json:"result"`
		Error  map[string]any `json:"error"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		h.t.Fatalf("%s decode %q: %v", method, payload, err)
	}
	if out.Error != nil {
		h.t.Fatalf("%s error: %v", method, out.Error)
	}
	return out.Result
}

func (h *gwHarness) waitAudits(minCount int) []store.AuditEvent {
	h.t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		h.sink.mu.Lock()
		n := len(h.sink.audits)
		h.sink.mu.Unlock()
		if n >= minCount {
			break
		}
	}
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	return append([]store.AuditEvent(nil), h.sink.audits...)
}

func TestResourceTemplatesRewrittenWithExpressionsIntact(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "gh", Version: "0.0.0"}, nil)
	srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "github://repos/{owner}/{repo}", Name: "repo", Description: "Repo info"},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		})
	srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "github://files/{+path}", Name: "file"},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		})
	srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "github://issues{?page,per_page}", Name: "issues"},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		})
	srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "github://pending/{id}", Name: "pending"},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		})
	srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "github://../escape/{id}", Name: "traversal"},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		})

	h := newTestGateway(t, map[string]*mcp.Server{"github": srv})
	if err := h.st.CreateTeam(ctx, h.org, store.Team{Slug: "designers", Name: "Designers", CreatedAt: time.Now()}, store.AuditEvent{
		ID: "t1", CallID: "t1", Time: time.Now(), Actor: "test", Action: "team.create", Decision: "allow", Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	srvID := h.addServer("github", []string{"designers"})
	h.acceptPin(srvID, upstream.KindResourceTemplate, "github://repos/{owner}/{repo}")
	h.acceptPin(srvID, upstream.KindResourceTemplate, "github://files/{+path}")
	h.acceptPin(srvID, upstream.KindResourceTemplate, "github://issues{?page,per_page}")
	h.acceptPin(srvID, upstream.KindResourceTemplate, "github://../escape/{id}")
	// Leave "github://pending/{id}" unreviewed (pending).

	tok, _ := h.addMemberToken("u-ada", "ada", "designers")
	cs := h.mcpSession(tok)

	listed, err := cs.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{})
	if err != nil {
		t.Fatal(err)
	}
	var gotURIs []string
	for _, rt := range listed.ResourceTemplates {
		gotURIs = append(gotURIs, rt.URITemplate)
		if !strings.HasPrefix(rt.Name, "github__") {
			t.Errorf("template name %q not prefixed", rt.Name)
		}
	}
	wantURIs := []string{
		"privo://github/github://files/{+path}",
		"privo://github/github://issues{?page,per_page}",
		"privo://github/github://repos/{owner}/{repo}",
	}
	slices.Sort(gotURIs)
	if !slices.Equal(gotURIs, wantURIs) {
		t.Fatalf("listed templates = %v, want %v", gotURIs, wantURIs)
	}

	// Verify cache hints on raw wire response (cacheScope=private, ttlMs=5000).
	raw := h.rawRPC(tok, "resources/templates/list", map[string]any{})
	if raw["cacheScope"] != "private" || raw["ttlMs"] != float64(5000) {
		t.Fatalf("cache hints = (%v, %v), want (private, 5000)", raw["cacheScope"], raw["ttlMs"])
	}

	// Toolset narrowing hides non-chosen templates.
	if err := h.st.SetTeamItemTools(ctx, h.org, srvID, "designers", store.ItemKindResourceTemplate, []string{"github://repos/{owner}/{repo}"}, store.AuditEvent{
		ID: "ts1", CallID: "ts1", Time: time.Now(), Actor: "test", Action: "ts.set", Decision: "allow", Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	// A fresh session immediately sees the narrowed list (the earlier session
	// cached listCache() for ttlMs = 5000 per SEP-2549).
	cs2 := h.mcpSession(tok)
	listed, err = cs2.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.ResourceTemplates) != 1 || listed.ResourceTemplates[0].URITemplate != "privo://github/github://repos/{owner}/{repo}" {
		t.Fatalf("after toolset narrowing: %+v", listed.ResourceTemplates)
	}
}

func TestReadResourceMatchesAcceptedTemplateOnly(t *testing.T) {
	ctx := context.Background()
	var upstreamReads []string
	srv := mcp.NewServer(&mcp.Implementation{Name: "gh", Version: "0.0.0"}, nil)
	srv.AddResource(&mcp.Resource{URI: "github://repos/acme/static", Name: "static"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			upstreamReads = append(upstreamReads, "static:"+req.Params.URI)
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "static-body"}}}, nil
		})
	srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "github://repos/{owner}/{repo}", Name: "repo"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			upstreamReads = append(upstreamReads, "tmpl:"+req.Params.URI)
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "repo-body:" + req.Params.URI}}}, nil
		})
	srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "github://files/{+path}", Name: "file"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			upstreamReads = append(upstreamReads, "plus:"+req.Params.URI)
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "file-body"}}}, nil
		})
	srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "github://items{?page,per_page}", Name: "items"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			upstreamReads = append(upstreamReads, "query:"+req.Params.URI)
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "items-body"}}}, nil
		})
	srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "github://unreviewed/{id}", Name: "pending"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			upstreamReads = append(upstreamReads, "PENDING:"+req.Params.URI)
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "secret"}}}, nil
		})

	h := newTestGateway(t, map[string]*mcp.Server{"github": srv})
	srvID := h.addServer("github", []string{store.TeamEveryone})
	h.acceptPin(srvID, upstream.KindResource, "github://repos/acme/static")
	h.acceptPin(srvID, upstream.KindResourceTemplate, "github://repos/{owner}/{repo}")
	h.acceptPin(srvID, upstream.KindResourceTemplate, "github://files/{+path}")
	h.acceptPin(srvID, upstream.KindResourceTemplate, "github://items{?page,per_page}")
	// Leave "github://unreviewed/{id}" pending!

	tok, _ := h.addMemberToken("u-ada", "ada")
	cs := h.mcpSession(tok)

	// 1. Static resource takes precedence over matching template.
	res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: ResourceURI("github", "github://repos/acme/static")})
	if err != nil || len(res.Contents) != 1 || res.Contents[0].Text != "static-body" {
		t.Fatalf("static read = %+v, %v", res, err)
	}

	// 2. Expanded template URI (raw slashes) matches accepted template, rewrites response URI to ResourceURI.
	res, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "privo://github/github://repos/acme/app"})
	if err != nil || len(res.Contents) != 1 || res.Contents[0].Text != "repo-body:github://repos/acme/app" {
		t.Fatalf("template read = %+v, %v", res, err)
	}
	if want := ResourceURI("github", "github://repos/acme/app"); res.Contents[0].URI != want {
		t.Fatalf("rewritten URI = %q, want %q", res.Contents[0].URI, want)
	}

	// 3. RFC 6570 percent-encoded variable value ({owner}="a/b" -> a%2Fb) passes to upstream as raw a%2Fb.
	res, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "privo://github/github://repos/a%2Fb/app"})
	if err != nil || res.Contents[0].Text != "repo-body:github://repos/a%2Fb/app" {
		t.Fatalf("encoded segment template read = %+v, %v", res, err)
	}

	// 4. Escaped ResourceURI form also matches accepted template.
	res, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: ResourceURI("github", "github://repos/octo/cli")})
	if err != nil || res.Contents[0].Text != "repo-body:github://repos/octo/cli" {
		t.Fatalf("escaped template read = %+v, %v", res, err)
	}

	// 5. {+path} and {?page,per_page} work.
	if res, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "privo://github/github://files/docs/intro.md"}); err != nil || res.Contents[0].Text != "file-body" {
		t.Fatalf("{+path} read = %+v, %v", res, err)
	}
	if res, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "privo://github/github://items?page=2&per_page=10"}); err != nil || res.Contents[0].Text != "items-body" {
		t.Fatalf("{?page,per_page} read = %+v, %v", res, err)
	}

	// 6. Verify read cache hints on wire (cacheScope=private, ttlMs=0).
	rawRead := h.rawRPC(tok, "resources/read", map[string]any{"uri": "privo://github/github://repos/acme/app"})
	if rawRead["cacheScope"] != "private" || rawRead["ttlMs"] != float64(0) {
		t.Fatalf("read cache hints = (%v, %v)", rawRead["cacheScope"], rawRead["ttlMs"])
	}

	// 7. Pending template, unmatched URI, extra query param, traversal, and bad alias tricks all fail with -32602 and NEVER reach upstream.
	before := len(upstreamReads)
	for _, bad := range []string{
		"privo://github/github://unreviewed/42",
		"privo://github/github://items?evil=1",
		"privo://github/github://unknown/path",
		"privo://github/github://files/../../etc/passwd",
		"privo://github/github://files/%2e%2e/secret",
		"privo://github?x=1/github://repos/acme/app",
		"privo://../github://repos/acme/app",
	} {
		_, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: bad})
		var rpc *jsonrpc.Error
		if !errors.As(err, &rpc) || rpc.Code != jsonrpc.CodeInvalidParams {
			t.Errorf("ReadResource(%q): err = %v, want jsonrpc -32602", bad, err)
		}
	}
	if len(upstreamReads) != before {
		t.Fatalf("rejected reads reached upstream: %v", upstreamReads[before:])
	}

	// 8. Policy rule targeting the template refuses the call through the full g.run pipeline.
	if err := h.st.CreatePolicies(ctx, h.org, store.Policy{
		ID:        "pol-deny-tpl",
		Server:    "github",
		Tool:      "github://repos/{owner}/{repo}",
		Subject:   "*",
		Action:    store.ActionDeny,
		CreatedBy: "admin",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	_, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "privo://github/github://repos/acme/app"})
	if err == nil || !strings.Contains(err.Error(), "Denied") {
		t.Fatalf("policy-denied template read err = %v, want Denied", err)
	}
}

func TestHiddenPromptIsUnknown(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "kit", Version: "0.0.0"}, nil)
	srv.AddPrompt(&mcp.Prompt{Name: "greet"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "hi"}}}}, nil
	})
	srv.AddPrompt(&mcp.Prompt{Name: "secret"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "shh"}}}}, nil
	})

	h := newTestGateway(t, map[string]*mcp.Server{"kit": srv})
	if err := h.st.CreateTeam(ctx, h.org, store.Team{Slug: "designers", Name: "Designers", CreatedAt: time.Now()}, store.AuditEvent{
		ID: "t1", CallID: "t1", Time: time.Now(), Actor: "test", Action: "team.create", Decision: "allow", Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	srvID := h.addServer("kit", []string{"designers"})
	h.acceptPin(srvID, upstream.KindPrompt, "greet")
	h.acceptPin(srvID, upstream.KindPrompt, "secret")
	if err := h.st.SetTeamItemTools(ctx, h.org, srvID, "designers", store.ItemKindPrompt, []string{"greet"}, store.AuditEvent{
		ID: "ts1", CallID: "ts1", Time: time.Now(), Actor: "test", Action: "ts.set", Decision: "allow", Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}

	tok, _ := h.addMemberToken("u-ada", "ada", "designers")
	cs := h.mcpSession(tok)

	listed, err := cs.ListPrompts(ctx, &mcp.ListPromptsParams{})
	if err != nil || len(listed.Prompts) != 1 || listed.Prompts[0].Name != "kit__greet" {
		t.Fatalf("prompts = %+v, %v", listed, err)
	}

	_, hiddenErr := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "kit__secret"})
	_, unknownErr := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "kit__nope"})
	var hiddenRPC, unknownRPC *jsonrpc.Error
	if !errors.As(hiddenErr, &hiddenRPC) || !errors.As(unknownErr, &unknownRPC) || hiddenRPC.Code != jsonrpc.CodeInvalidParams || unknownRPC.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("hidden=%v, unknown=%v; want -32602", hiddenErr, unknownErr)
	}
	if strings.ReplaceAll(hiddenRPC.Message, "kit__secret", "X") != strings.ReplaceAll(unknownRPC.Message, "kit__nope", "X") {
		t.Fatalf("hidden error %q differs from unknown %q", hiddenRPC.Message, unknownRPC.Message)
	}

	// Unknown prompt leaves no audit row; hidden prompt records denied:not_in_toolset.
	audits := h.waitAudits(2)
	var toolsetRefusals int
	for _, ev := range audits {
		if ev.Phase == "intent" && ev.Decision == "denied:not_in_toolset" && ev.Action == "prompts/get" && ev.Item == "secret" {
			toolsetRefusals++
		}
	}
	if toolsetRefusals != 1 {
		t.Fatalf("expected 1 denied:not_in_toolset audit intent for hidden prompt, got %+v", audits)
	}
}

func TestHiddenResourceIsUnknown(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "kit", Version: "0.0.0"}, nil)
	srv.AddResource(&mcp.Resource{URI: "kit://public", Name: "pub"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "kit://public", Text: "ok"}}}, nil
	})
	srv.AddResource(&mcp.Resource{URI: "kit://secret", Name: "sec"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "kit://secret", Text: "no"}}}, nil
	})
	srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "kit://private/{id}", Name: "priv"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "kit://private/1", Text: "no"}}}, nil
	})

	h := newTestGateway(t, map[string]*mcp.Server{"kit": srv})
	if err := h.st.CreateTeam(ctx, h.org, store.Team{Slug: "designers", Name: "Designers", CreatedAt: time.Now()}, store.AuditEvent{
		ID: "t1", CallID: "t1", Time: time.Now(), Actor: "test", Action: "team.create", Decision: "allow", Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	srvID := h.addServer("kit", []string{"designers"})
	h.acceptPin(srvID, upstream.KindResource, "kit://public")
	h.acceptPin(srvID, upstream.KindResource, "kit://secret")
	h.acceptPin(srvID, upstream.KindResourceTemplate, "kit://private/{id}")
	if err := h.st.SetTeamToolsets(ctx, h.org, srvID, "designers", map[string][]string{
		store.ItemKindResource:         {"kit://public"},
		store.ItemKindResourceTemplate: {},
	}, store.AuditEvent{
		ID: "ts1", CallID: "ts1", Time: time.Now(), Actor: "test", Action: "ts.set", Decision: "allow", Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}

	tokAda, _ := h.addMemberToken("u-ada", "ada", "designers")
	tokBob, _ := h.addMemberToken("u-bob", "bob", "designers")
	csAda := h.mcpSession(tokAda)
	csBob := h.mcpSession(tokBob)

	// Hidden static resource answers identically to unknown resource.
	hiddenURI := ResourceURI("kit", "kit://secret")
	unknownURI := ResourceURI("kit", "kit://nope")
	_, hiddenErr := csAda.ReadResource(ctx, &mcp.ReadResourceParams{URI: hiddenURI})
	_, unknownErr := csAda.ReadResource(ctx, &mcp.ReadResourceParams{URI: unknownURI})
	var hiddenRPC, unknownRPC *jsonrpc.Error
	if !errors.As(hiddenErr, &hiddenRPC) || !errors.As(unknownErr, &unknownRPC) || hiddenRPC.Code != jsonrpc.CodeInvalidParams || unknownRPC.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("hidden=%v, unknown=%v; want -32602", hiddenErr, unknownErr)
	}
	if strings.ReplaceAll(hiddenRPC.Message, hiddenURI, "X") != strings.ReplaceAll(unknownRPC.Message, unknownURI, "X") {
		t.Fatalf("hidden error %q differs from unknown %q", hiddenRPC.Message, unknownRPC.Message)
	}

	// Hidden template resource also answers identically to unknown resource.
	hiddenTmplURI := "privo://kit/kit://private/42"
	_, hiddenTmplErr := csBob.ReadResource(ctx, &mcp.ReadResourceParams{URI: hiddenTmplURI})
	var hiddenTmplRPC *jsonrpc.Error
	if !errors.As(hiddenTmplErr, &hiddenTmplRPC) || hiddenTmplRPC.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("hidden template err=%v, want -32602", hiddenTmplErr)
	}
	if strings.ReplaceAll(hiddenTmplRPC.Message, hiddenTmplURI, "X") != strings.ReplaceAll(unknownRPC.Message, unknownURI, "X") {
		t.Fatalf("hidden template error %q differs from unknown %q", hiddenTmplRPC.Message, unknownRPC.Message)
	}

	// Each caller records 1 intent + 1 completion = 4 events total.
	audits := h.waitAudits(4)
	var staticHits, tmplHits int
	for _, ev := range audits {
		if ev.Phase == "intent" && ev.Decision == "denied:not_in_toolset" && ev.Action == "resources/read" {
			switch ev.Item {
			case "kit://secret":
				staticHits++
			case "kit://private/{id}":
				tmplHits++
			}
		}
	}
	if staticHits != 1 || tmplHits != 1 {
		t.Fatalf("expected 1 static and 1 template denied:not_in_toolset audit intent, got static=%d tmpl=%d (%+v)", staticHits, tmplHits, audits)
	}
}
