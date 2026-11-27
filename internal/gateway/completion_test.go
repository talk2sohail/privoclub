package gateway

import (
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
	"privo.club/privo/internal/token"
	"privo.club/privo/internal/upstream"
	"privo.club/privo/internal/vault"
)

type handlerRoundTripper struct {
	h  http.Handler
	wg sync.WaitGroup
}

type pipeResponseWriter struct {
	header http.Header
	pw     *io.PipeWriter
	req    *http.Request
	ready  chan *http.Response
	pr     *io.PipeReader
	once   sync.Once
}

func (w *pipeResponseWriter) Header() http.Header {
	return w.header
}

func (w *pipeResponseWriter) send(code int) {
	w.once.Do(func() {
		if code == 0 {
			code = http.StatusOK
		}
		w.ready <- &http.Response{
			StatusCode: code,
			Status:     http.StatusText(code),
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     w.header.Clone(),
			Body:       w.pr,
			Request:    w.req,
		}
	})
}

func (w *pipeResponseWriter) WriteHeader(code int) {
	w.send(code)
}

func (w *pipeResponseWriter) Write(p []byte) (int, error) {
	w.send(http.StatusOK)
	return w.pw.Write(p)
}

func (w *pipeResponseWriter) Flush() {
	w.send(http.StatusOK)
}

func (rt *handlerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	req := r.Clone(r.Context())
	if req.URL.Scheme == "" {
		req.URL.Scheme = "http"
	}
	if req.URL.Host == "" {
		req.URL.Host = "inprocess.local"
	}
	pr, pw := io.Pipe()
	w := &pipeResponseWriter{
		header: make(http.Header),
		pw:     pw,
		pr:     pr,
		req:    req,
		ready:  make(chan *http.Response, 1),
	}
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		defer func() {
			if req.Body != nil {
				_ = req.Body.Close()
			}
		}()
		defer func() {
			w.send(http.StatusOK)
			_ = pw.Close()
		}()
		rt.h.ServeHTTP(w, req)
	}()
	select {
	case resp := <-w.ready:
		return resp, nil
	case <-r.Context().Done():
		_ = pr.Close()
		return nil, r.Context().Err()
	}
}

type completionTestEnv struct {
	st          *store.Store
	org         store.OrgID
	kr          *keyring.Keyring
	aw          *audit.Writer
	rec         *telemetry.Recorder
	mgr         *upstream.Manager
	gw          *Gateway
	handler     http.Handler
	wg          sync.WaitGroup
	srv         store.Server
	ownerToken  string
	memberToken string
	memberUser  store.User
	calls       atomic.Int32
	lastRefMu   sync.Mutex
	lastRefs    []mcp.CompleteReference
	lastArgs    []mcp.CompleteParams
	mode        atomic.Value // "", "null_values", "input_required", "null_result", "poison_meta"
}

func newCompletionTestEnv(t *testing.T, serverTeams []string) *completionTestEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	orgObj, err := st.EnsureOrg(ctx, "default", "Default")
	if err != nil {
		t.Fatal(err)
	}
	org := orgObj.ID
	kek, _ := vault.NewKey()
	kr := keyring.New(st, vault.StaticKeyProvider(kek, nil))
	if err := kr.EnsureOrgKey(ctx, org); err != nil {
		t.Fatal(err)
	}

	env := &completionTestEnv{st: st, org: org, kr: kr}
	env.mode.Store("")

	upSrv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, &mcp.ServerOptions{
		CompletionHandler: func(_ context.Context, req *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
			env.calls.Add(1)
			env.lastRefMu.Lock()
			if req.Params.Ref != nil {
				env.lastRefs = append(env.lastRefs, *req.Params.Ref)
			}
			env.lastArgs = append(env.lastArgs, *req.Params)
			env.lastRefMu.Unlock()

			mode, _ := env.mode.Load().(string)
			switch mode {
			case "null_values":
				return &mcp.CompleteResult{Completion: mcp.CompletionResultDetails{Values: nil}}, nil
			case "poison_meta":
				return &mcp.CompleteResult{
					Meta: mcp.Meta{
						"io.modelcontextprotocol/serverInfo":   map[string]any{"name": "evil", "version": "9"},
						"io.modelcontextprotocol/related-task": map[string]any{"taskId": "t1"},
						"dev.mcp/internal":                     true,
						"com.example/keep":                     "yes",
					},
					Completion: mcp.CompletionResultDetails{Values: []string{"Ada"}},
				}, nil
			}
			if req.Params.Ref != nil && req.Params.Ref.Type == "ref/resource" {
				return &mcp.CompleteResult{Completion: mcp.CompletionResultDetails{Values: []string{"note-alpha", "note-beta"}, Total: 2}}, nil
			}
			return &mcp.CompleteResult{Completion: mcp.CompletionResultDetails{Values: []string{"Ada", "Alan"}, Total: 2}}, nil
		},
	})
	upSrv.AddPrompt(&mcp.Prompt{
		Name:      "greet",
		Arguments: []*mcp.PromptArgument{{Name: "name", Required: true}},
	}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "hi"}}}}, nil
	})
	upSrv.AddResource(&mcp.Resource{URI: "echo://note", Name: "note", MIMEType: "text/plain"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "echo://note", Text: "body"}}}, nil
	})

	mcpUpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upSrv }, &mcp.StreamableHTTPOptions{Stateless: true})
	rawUpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode, _ := env.mode.Load().(string)
		if mode == "input_required" || mode == "null_result" {
			body, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			var rpcReq struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			if err := json.Unmarshal(body, &rpcReq); err == nil && rpcReq.Method == "completion/complete" {
				env.calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if mode == "input_required" {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"jsonrpc": "2.0",
						"id":      rpcReq.ID,
						"result": map[string]any{
							"resultType": "input_required",
							"completion": map[string]any{"values": []string{}},
						},
					})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      rpcReq.ID,
					"result":  nil,
				})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		mcpUpHandler.ServeHTTP(w, r)
	})

	upRT := &handlerRoundTripper{h: rawUpHandler}
	logger := slog.New(slog.DiscardHandler)
	env.rec = telemetry.New(st, telemetry.Options{FlushInterval: 10 * time.Millisecond, Logger: logger})
	env.aw = audit.NewWriter(st, audit.Options{FlushInterval: 10 * time.Millisecond, Logger: logger})
	env.mgr = upstream.New(upstream.Options{
		Store:          st,
		Keys:           kr,
		HTTPClient:     &http.Client{Transport: upRT},
		ConnectTimeout: 3 * time.Second,
		RetryBase:      time.Hour,
		Logger:         logger,
		Diagnostics:    env.rec,
	})
	t.Cleanup(func() {
		env.wg.Wait()
		env.mgr.Close()
		upRT.wg.Wait()
		env.aw.Close()
		env.rec.Close()
	})

	env.srv = store.Server{
		OrgID:     org,
		ID:        "srv-echo",
		Alias:     "echo",
		Transport: "http",
		Enabled:   true,
		Teams:     serverTeams,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Config:    store.ServerConfig{URL: "http://inprocess.local/mcp"},
	}
	if err := st.CreateServer(ctx, env.srv, nil); err != nil {
		t.Fatal(err)
	}
	if err := env.mgr.Sync(ctx, org); err != nil {
		t.Fatal(err)
	}
	env.mgr.WaitSettled(ctx, env.srv.ID, 5*time.Second)

	// Create owner and member users + gateway tokens.
	env.ownerToken = env.issueUserToken(t, "boss", store.RoleOwner)
	env.memberUser, env.memberToken = env.issueUserAndToken(t, "ada", store.RoleMember)

	authn := auth.New(st)
	env.gw = &Gateway{
		Store:     st,
		Upstream:  env.mgr,
		Keys:      kr,
		Audit:     env.aw,
		Auth:      authn,
		Approval:  &approval.Service{Store: st, Keys: kr},
		Logger:    logger,
		Telemetry: env.rec,
	}
	inner := env.gw.Handler()
	env.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.wg.Add(1)
		defer env.wg.Done()
		inner.ServeHTTP(w, r)
	})
	return env
}

func (e *completionTestEnv) issueUserToken(t *testing.T, person, role string) string {
	t.Helper()
	_, tok := e.issueUserAndToken(t, person, role)
	return tok
}

func (e *completionTestEnv) issueUserAndToken(t *testing.T, person, role string) (store.User, string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	linkID := ids.New()
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: now, Actor: "t", Action: "seed", Decision: "allow", Outcome: "ok"}
	if err := e.st.CreateSetupLink(ctx, e.org, store.NewSetupLink{ID: linkID, SecretHash: "h-" + person, CreatedBy: "t", Now: now, ExpiresAt: now.Add(time.Hour)}, ev); err != nil {
		t.Fatal(err)
	}
	ev.ID, ev.CallID = ids.New(), ids.New()
	u, err := e.st.ClaimSetup(ctx, e.org, store.Claim{
		LinkID:      linkID,
		UserID:      "u-" + person,
		Person:      person,
		Subject:     "sub-" + person,
		Login:       person,
		DisplayName: person,
		Session:     store.Session{ID: "sess-" + person, CSRF: "csrf-" + person, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)},
		Now:         now,
	}, ev)
	if err != nil {
		t.Fatal(err)
	}
	if role != store.RoleOwner {
		ev.ID, ev.CallID = ids.New(), ids.New()
		roles, err := e.st.SetUserRoles(ctx, e.org, u.ID, []string{role}, ev)
		if err != nil {
			t.Fatal(err)
		}
		u.Roles = roles
	}
	tok, err := token.New()
	if err != nil {
		t.Fatal(err)
	}
	ev.ID, ev.CallID = ids.New(), ids.New()
	if err := e.st.CreateTokenAudited(ctx, store.APIToken{
		OrgID:      e.org,
		ID:         tok.ID,
		Name:       person + "-gw",
		Role:       store.RoleMember,
		Kind:       store.KindGateway,
		Person:     person,
		UserID:     u.ID,
		SecretHash: tok.SecretHash,
		CreatedAt:  now,
		ExpiresAt:  now.Add(time.Hour),
	}, ev); err != nil {
		t.Fatal(err)
	}
	return u, tok.Plain
}

func (e *completionTestEnv) acceptAllPins(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pins, err := e.st.ListPins(ctx, e.org, e.srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	var keys []store.PinKey
	for _, p := range pins {
		if p.Status == store.PinPending {
			keys = append(keys, store.PinKey{Kind: p.Kind, Name: p.Name, Hash: p.Hash})
		}
	}
	if len(keys) == 0 {
		t.Fatal("no pending pins")
	}
	if _, err := e.st.AcceptPins(ctx, e.org, e.srv.ID, keys); err != nil {
		t.Fatal(err)
	}
}

type authRT struct {
	token string
	next  http.RoundTripper
}

func (a authRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+a.token)
	return a.next.RoundTrip(r2)
}

func (e *completionTestEnv) connect(t *testing.T, bearer string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	gwRT := &handlerRoundTripper{h: e.handler}
	t.Cleanup(gwRT.wg.Wait)
	s, err := c.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             "http://inprocess.local/mcp",
		HTTPClient:           &http.Client{Transport: authRT{token: bearer, next: gwRT}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func (e *completionTestEnv) rawRPC(t *testing.T, bearer, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://inprocess.local/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "completion/complete")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rawRPC status = %d, body = %s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.Bytes()
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		for _, line := range strings.Split(string(raw), "\n") {
			if data, ok := strings.CutPrefix(line, "data:"); ok && strings.TrimSpace(data) != "" {
				raw = []byte(strings.TrimSpace(data))
				break
			}
		}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode RPC response %q: %v", raw, err)
	}
	return out
}

func assertJSONRPCError(t *testing.T, err error, wantCode int64) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected JSON-RPC error code %d, got nil", wantCode)
	}
	var rpc *jsonrpc.Error
	if !errors.As(err, &rpc) || rpc.Code != wantCode {
		t.Fatalf("got error %#v, want JSON-RPC code %d", err, wantCode)
	}
}

func TestCompletionCompleteEnforcesTeamsPinsPolicyAndAudit(t *testing.T) {
	// Put echo server on team "eng" only.
	ctx := context.Background()
	e := newCompletionTestEnv(t, nil)
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: "boss", Action: "team.create", Decision: "allow", Outcome: "ok"}
	if err := e.st.CreateTeam(ctx, e.org, store.Team{Slug: "eng", Name: "Engineering", CreatedAt: time.Now()}, ev); err != nil {
		t.Fatal(err)
	}
	ev.ID, ev.CallID = ids.New(), ids.New()
	if err := e.st.SetServerTeams(ctx, e.org, e.srv.ID, []string{"eng"}, ev); err != nil {
		t.Fatal(err)
	}

	// Owner is on "eng"; member "ada" is NOT on "eng".
	bossUser, err := e.st.UserByPerson(ctx, e.org, "boss")
	if err != nil {
		t.Fatal(err)
	}
	ev.ID, ev.CallID = ids.New(), ids.New()
	if err := e.st.AddTeamMember(ctx, e.org, "eng", bossUser.ID, time.Now(), ev); err != nil {
		t.Fatal(err)
	}

	bossSess := e.connect(t, e.ownerToken)
	adaSess := e.connect(t, e.memberToken)

	// 1. Gateway advertises completion capabilities.
	initRes := bossSess.InitializeResult()
	if initRes == nil || initRes.Capabilities == nil || initRes.Capabilities.Completions == nil {
		t.Fatalf("gateway did not advertise Completions capability: %+v", initRes)
	}

	// 2. Unaccepted pins are refused with -32602 and never reach upstream.
	_, err = bossSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "echo__greet"},
		Argument: mcp.CompleteParamsArgument{Name: "name", Value: "A"},
	})
	assertJSONRPCError(t, err, jsonrpc.CodeInvalidParams)
	_, err = bossSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/resource", URI: ResourceURI("echo", "echo://note")},
		Argument: mcp.CompleteParamsArgument{Name: "id", Value: "1"},
	})
	assertJSONRPCError(t, err, jsonrpc.CodeInvalidParams)
	if n := e.calls.Load(); n != 0 {
		t.Fatalf("unaccepted pins reached upstream %d times", n)
	}

	// 3. Accept pins.
	e.acceptAllPins(t)

	// 4. Invalid params cases (nil params, nil ref, unknown ref type, bad resource URI).
	ti, verr := e.gw.Auth.MCPVerifier()(ctx, e.ownerToken, nil)
	if verr != nil {
		t.Fatal(verr)
	}
	mw := e.gw.middleware(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		t.Fatal("invalid completion request fell through middleware")
		return nil, nil
	})
	for _, badReq := range []*mcp.CompleteRequest{
		{Params: nil, Extra: &mcp.RequestExtra{TokenInfo: ti}},
		{Params: &mcp.CompleteParams{Ref: nil}, Extra: &mcp.RequestExtra{TokenInfo: ti}},
		{Params: &mcp.CompleteParams{Ref: &mcp.CompleteReference{Type: "ref/unknown"}}, Extra: &mcp.RequestExtra{TokenInfo: ti}},
	} {
		_, err = mw(ctx, "completion/complete", badReq)
		assertJSONRPCError(t, err, jsonrpc.CodeInvalidParams)
	}
	_, err = bossSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/resource", URI: "https://evil.example/note"},
		Argument: mcp.CompleteParamsArgument{Name: "x", Value: "y"},
	})
	assertJSONRPCError(t, err, jsonrpc.CodeInvalidParams)

	// 5. Non-team member "ada" is refused with -32602 (anti-probing) AND
	// recordHidden audits denied:not_in_team for completion/complete.
	_, err = adaSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "echo__greet"},
		Argument: mcp.CompleteParamsArgument{Name: "name", Value: "A"},
	})
	assertJSONRPCError(t, err, jsonrpc.CodeInvalidParams)
	_, err = adaSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/resource", URI: ResourceURI("echo", "echo://note")},
		Argument: mcp.CompleteParamsArgument{Name: "id", Value: "1"},
	})
	assertJSONRPCError(t, err, jsonrpc.CodeInvalidParams)
	if n := e.calls.Load(); n != 0 {
		t.Fatalf("non-team member reached upstream %d times", n)
	}
	// Wait for async recordHidden goroutine before continuing/closing.
	deadline := time.Now().Add(3 * time.Second)
	for {
		calls, _ := e.st.RecentCalls(ctx, e.org, 20)
		if slices.ContainsFunc(calls, func(r store.CallRecord) bool {
			return r.Intent.Action == "completion/complete" && r.Intent.Decision == "denied:not_in_team"
		}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hidden completion/complete refusal was not audited: %+v", calls)
		}
		time.Sleep(15 * time.Millisecond)
	}

	// 6. Owner on team "eng" completes ref/prompt and ref/resource.
	// Turn on argument recording to verify deterministic freezing & encryption.
	if err := e.st.SetSetting(ctx, e.org, SettingRecordArguments, "true"); err != nil {
		t.Fatal(err)
	}
	promptRes, err := bossSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "echo__greet"},
		Argument: mcp.CompleteParamsArgument{Name: "name", Value: "A"},
		Context:  &mcp.CompleteContext{Arguments: map[string]string{"z": "2", "a": "1"}},
	})
	if err != nil {
		t.Fatalf("prompt Complete: %v", err)
	}
	if !slices.Equal(promptRes.Completion.Values, []string{"Ada", "Alan"}) {
		t.Fatalf("prompt completion values = %v", promptRes.Completion.Values)
	}
	resRes, err := bossSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/resource", URI: ResourceURI("echo", "echo://note")},
		Argument: mcp.CompleteParamsArgument{Name: "id", Value: "n"},
	})
	if err != nil {
		t.Fatalf("resource Complete: %v", err)
	}
	if !slices.Equal(resRes.Completion.Values, []string{"note-alpha", "note-beta"}) {
		t.Fatalf("resource completion values = %v", resRes.Completion.Values)
	}

	e.lastRefMu.Lock()
	refs := slices.Clone(e.lastRefs)
	e.lastRefMu.Unlock()
	if len(refs) != 2 || refs[0].Type != "ref/prompt" || refs[0].Name != "greet" ||
		refs[1].Type != "ref/resource" || refs[1].URI != "echo://note" {
		t.Fatalf("upstream received refs %+v, want rewritten greet & echo://note", refs)
	}

	// 7. Policy deny rule on prompt blocks completion/complete before upstream.
	if err := e.st.CreatePolicies(ctx, e.org, store.Policy{
		ID:        "pol-deny-greet",
		Server:    "echo",
		Tool:      "greet",
		Subject:   "*",
		Action:    store.ActionDeny,
		Priority:  10,
		CreatedBy: "boss",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	callsBefore := e.calls.Load()
	_, err = bossSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "echo__greet"},
		Argument: mcp.CompleteParamsArgument{Name: "name", Value: "A"},
	})
	assertJSONRPCError(t, err, jsonrpc.CodeInvalidRequest)
	if !strings.Contains(err.Error(), "Denied") {
		t.Fatalf("deny err = %v, want Denied", err)
	}
	if e.calls.Load() != callsBefore {
		t.Fatal("denied completion reached upstream")
	}

	// 8. Audit intent failure refuses call fail-closed.
	failGw := &Gateway{
		Store:     e.gw.Store,
		Upstream:  e.gw.Upstream,
		Keys:      e.gw.Keys,
		Audit:     audit.NewWriter(failingGatewayAuditStore{}, audit.Options{Logger: slog.New(slog.DiscardHandler)}),
		Auth:      e.gw.Auth,
		Approval:  e.gw.Approval,
		Logger:    e.gw.Logger,
		Telemetry: e.gw.Telemetry,
	}
	t.Cleanup(failGw.Audit.Close)
	_, err = failGw.complete(ctx, auth.Principal{OrgID: e.org, UserID: bossUser.ID, Roles: []string{store.RoleOwner}}, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/resource", URI: ResourceURI("echo", "echo://note")},
		Argument: mcp.CompleteParamsArgument{Name: "id", Value: "1"},
	})
	if err == nil || !strings.Contains(err.Error(), "audit unavailable") || e.calls.Load() != callsBefore {
		t.Fatalf("audit failure err = %v (calls %d vs %d)", err, e.calls.Load(), callsBefore)
	}

	// Wait for completions & telemetry flush.
	e.wg.Wait()
	e.aw.Close()
	e.rec.Close()

	calls, err := e.st.RecentCalls(ctx, e.org, 50)
	if err != nil {
		t.Fatal(err)
	}
	var okPrompt, okRes, deniedPrompt bool
	for _, c := range calls {
		if c.Intent.Action != "completion/complete" {
			continue
		}
		if c.Intent.Item == "greet" && c.Intent.Decision == store.ActionAllow && c.Completion != nil && c.Completion.Outcome == store.OutcomeOK {
			okPrompt = true
			if len(c.Intent.Payload) <= 4 {
				t.Errorf("recorded payload missing for completion/complete")
			}
		}
		if c.Intent.Item == "echo://note" && c.Intent.Decision == store.ActionAllow && c.Completion != nil && c.Completion.Outcome == store.OutcomeOK {
			okRes = true
		}
		if c.Intent.Item == "greet" && c.Intent.Decision == store.ActionDeny && c.Completion != nil && strings.HasPrefix(c.Completion.Outcome, "denied:") {
			deniedPrompt = true
		}
	}
	if !okPrompt || !okRes || !deniedPrompt {
		t.Fatalf("missing audit records: okPrompt=%v okRes=%v deniedPrompt=%v in %+v", okPrompt, okRes, deniedPrompt, calls)
	}

	stats, _, err := e.st.CallStats(ctx, e.org, store.CallStatQuery{Method: "completion/complete"})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 4 || stats[3].ErrorClass != "audit_unavailable" {
		t.Fatalf("CallStats for completion/complete = %d, want 4 (last audit_unavailable): %+v", len(stats), stats)
	}
}

type failingGatewayAuditStore struct{}

func (failingGatewayAuditStore) InsertAudit(context.Context, store.OrgID, ...store.AuditEvent) error {
	return errors.New("audit down")
}

func TestCompletionCompleteAskFailsClosed(t *testing.T) {
	ctx := context.Background()
	e := newCompletionTestEnv(t, nil)
	e.acceptAllPins(t)

	// Org default = ask (or rule = ask); completions must fail closed as denied.
	if err := e.st.SetSetting(ctx, e.org, SettingPolicyDefault, store.ActionAsk); err != nil {
		t.Fatal(err)
	}
	sess := e.connect(t, e.ownerToken)

	for _, params := range []*mcp.CompleteParams{
		{
			Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "echo__greet"},
			Argument: mcp.CompleteParamsArgument{Name: "name", Value: "A"},
		},
		{
			Ref:      &mcp.CompleteReference{Type: "ref/resource", URI: ResourceURI("echo", "echo://note")},
			Argument: mcp.CompleteParamsArgument{Name: "id", Value: "1"},
		},
	} {
		_, err := sess.Complete(ctx, params)
		assertJSONRPCError(t, err, jsonrpc.CodeInvalidRequest)
		if !strings.Contains(err.Error(), "Denied") {
			t.Fatalf("ask err = %v, want Denied", err)
		}
	}
	if n := e.calls.Load(); n != 0 {
		t.Fatalf("ask policy reached upstream %d times", n)
	}
	approvals, err := e.st.ListApprovals(ctx, e.org, store.ApprovalPending, 50)
	if err != nil || len(approvals) != 0 {
		t.Fatalf("approvals created for completion/complete: %+v, err=%v", approvals, err)
	}

	e.wg.Wait()
	e.aw.Close()
	e.rec.Close()

	calls, err := e.st.RecentCalls(ctx, e.org, 20)
	if err != nil {
		t.Fatal(err)
	}
	askCount := 0
	for _, c := range calls {
		if c.Intent.Action == "completion/complete" && c.Intent.Decision == store.ActionAsk &&
			c.Completion != nil && c.Completion.Outcome == "denied:approval not supported for completion/complete" {
			askCount++
		}
	}
	if askCount != 2 {
		t.Fatalf("ask fail-closed audit count = %d, want 2: %+v", askCount, calls)
	}
	stats, _, err := e.st.CallStats(ctx, e.org, store.CallStatQuery{Method: "completion/complete", Outcome: store.OutcomeRefused})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 || stats[0].ErrorClass != "denied" || stats[1].ErrorClass != "denied" {
		t.Fatalf("telemetry stats = %+v, want 2 refused/denied", stats)
	}
}

func TestCompletionCompleteMalformedReplyRefused(t *testing.T) {
	ctx := context.Background()
	e := newCompletionTestEnv(t, nil)
	e.acceptAllPins(t)
	sess := e.connect(t, e.ownerToken)

	// 1. Null/missing completion.values
	e.mode.Store("null_values")
	_, err := sess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "echo__greet"},
		Argument: mcp.CompleteParamsArgument{Name: "name", Value: "A"},
	})
	assertJSONRPCError(t, err, jsonrpc.CodeInternalError)
	if !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("null_values err = %v, want malformed", err)
	}

	// 2. resultType: input_required
	e.mode.Store("input_required")
	_, err = sess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "echo__greet"},
		Argument: mcp.CompleteParamsArgument{Name: "name", Value: "A"},
	})
	assertJSONRPCError(t, err, jsonrpc.CodeInternalError)
	if !errors.Is(err, errNeedsInput) && !strings.Contains(err.Error(), "input") {
		t.Fatalf("input_required err = %v, want errNeedsInput", err)
	}

	// 3. null JSON result
	e.mode.Store("null_result")
	_, err = sess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "echo__greet"},
		Argument: mcp.CompleteParamsArgument{Name: "name", Value: "A"},
	})
	assertJSONRPCError(t, err, jsonrpc.CodeInternalError)

	// 4. Reserved _meta stripped while non-reserved _meta preserved (raw JSON check)
	e.mode.Store("poison_meta")
	raw := e.rawRPC(t, e.ownerToken, `{"jsonrpc":"2.0","id":9,"method":"completion/complete","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"ref":{"type":"ref/prompt","name":"echo__greet"},"argument":{"name":"name","value":"A"}}}`)
	resultObj, _ := raw["result"].(map[string]any)
	if resultObj == nil {
		t.Fatalf("poison_meta returned error: %v", raw)
	}
	if resultObj["resultType"] != "complete" {
		t.Errorf("resultType = %v, want complete", resultObj["resultType"])
	}
	meta, _ := resultObj["_meta"].(map[string]any)
	if meta["com.example/keep"] != "yes" {
		t.Errorf("non-reserved _meta dropped: %v", meta)
	}
	if _, bad := meta["io.modelcontextprotocol/related-task"]; bad {
		t.Errorf("reserved _meta related-task was relayed: %v", meta)
	}
	if _, bad := meta["dev.mcp/internal"]; bad {
		t.Errorf("reserved _meta dev.mcp/internal was relayed: %v", meta)
	}
	if info, _ := meta["io.modelcontextprotocol/serverInfo"].(map[string]any); info == nil || info["name"] != "privo" {
		t.Errorf("serverInfo = %v, want gateway serverInfo (not evil)", info)
	}

	e.wg.Wait()
	e.aw.Close()
	e.rec.Close()

	calls, err := e.st.RecentCalls(ctx, e.org, 20)
	if err != nil {
		t.Fatal(err)
	}
	errCount, okCount := 0, 0
	for _, c := range calls {
		if c.Intent.Action == "completion/complete" && c.Completion != nil {
			switch c.Completion.Outcome {
			case store.OutcomeError:
				errCount++
			case store.OutcomeOK:
				okCount++
			}
		}
	}
	if errCount != 3 || okCount != 1 {
		t.Fatalf("audit outcomes: error=%d (want 3), ok=%d (want 1)", errCount, okCount)
	}
	stats, _, err := e.st.CallStats(ctx, e.org, store.CallStatQuery{Method: "completion/complete", Outcome: store.OutcomeError})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 3 {
		t.Fatalf("malformed telemetry stats = %d, want 3: %+v", len(stats), stats)
	}
	for _, s := range stats {
		if s.ErrorClass != "malformed" {
			t.Errorf("stat ErrorClass = %q, want malformed", s.ErrorClass)
		}
	}
}
