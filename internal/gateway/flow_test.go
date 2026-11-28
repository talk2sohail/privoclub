package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/policy"
	"privo.club/privo/internal/runner"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/token"
	"privo.club/privo/internal/upstream"
	"privo.club/privo/internal/vault"
)

// TestFlowMCPHelperProcess runs an in-test stdio MCP server when spawned by
// runner.Local in gateway flow tests (no TCP sockets needed).
func TestFlowMCPHelperProcess(t *testing.T) {
	if os.Getenv("TEST_FLOW_HELPER") != "1" {
		return
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "flow-helper", Version: "0.0.1"}, nil)
	type textIn struct {
		Text string `json:"text"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "read_item", Description: "Read external item"},
		func(_ context.Context, _ *mcp.CallToolRequest, in textIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "src:" + in.Text}}}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "send_item", Description: "Send item externally"},
		func(_ context.Context, _ *mcp.CallToolRequest, in textIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "sent:" + in.Text}}}, nil, nil
		})
	s.AddPrompt(&mcp.Prompt{Name: "greet", Description: "Greet"},
		func(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "hi"}}}}, nil
		})
	s.AddResource(&mcp.Resource{URI: "flow://note", Name: "note", MIMEType: "text/plain"},
		func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "flow://note", MIMEType: "text/plain", Text: "untrusted-resource"}}}, nil
		})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

type handlerTransport struct {
	wg      *sync.WaitGroup
	handler http.Handler
	bearer  string
}

func (ht *handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	if ht.bearer != "" {
		clone.Header.Set("Authorization", "Bearer "+ht.bearer)
	}
	rec := httptest.NewRecorder()
	ht.wg.Add(1)
	defer ht.wg.Done()
	ht.handler.ServeHTTP(rec, clone)
	res := rec.Result()
	res.Request = clone
	return res, nil
}

type flowGatewayEnv struct {
	st   *store.Store
	org  store.OrgID
	aw   *audit.Writer
	mgr  *upstream.Manager
	gw   *Gateway
	wg   sync.WaitGroup
	srvA store.Server
	srvB store.Server
}

func newFlowGatewayEnv(t *testing.T) *flowGatewayEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, err := st.EnsureOrg(ctx, "flow-org", "Flow Org")
	if err != nil {
		t.Fatal(err)
	}
	kek, _ := vault.NewKey()
	kr := keyring.New(st, vault.StaticKeyProvider(kek, nil))
	if err := kr.EnsureOrgKey(ctx, org.ID); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	aw := audit.NewWriter(st, audit.Options{Logger: logger})
	mgr := upstream.New(upstream.Options{
		Store: st, Keys: kr, Runner: runner.Local{},
		RetryBase: 20 * time.Millisecond, RetryMax: 50 * time.Millisecond, Logger: logger,
	})
	authn := auth.New(st)
	appr := &approval.Service{Store: st, Keys: kr, Hold: 5 * time.Millisecond, Poll: time.Millisecond}
	// Leave gw.Flow nil to verify lazy initialization on &Gateway{...}.
	gw := &Gateway{
		Store: st, Upstream: mgr, Keys: kr, Audit: aw, Auth: authn, Approval: appr,
		Logger: logger, BaseURL: "https://privo.example",
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := runner.FileDigest(exe)
	if err != nil {
		t.Fatal(err)
	}
	addHelper := func(alias string) store.Server {
		srv := store.Server{
			OrgID: org.ID, ID: ids.New(), Alias: alias, Transport: "stdio", Enabled: true, CreatedAt: time.Now(),
			Config: store.ServerConfig{Path: exe, SHA256: digest, Args: []string{"-test.run=^TestFlowMCPHelperProcess$"}, EnvNames: []string{"TEST_FLOW_HELPER"}},
		}
		version, dek, err := kr.Current(ctx, org.ID)
		if err != nil {
			t.Fatal(err)
		}
		credID := ids.New()
		ct, err := vault.Seal(dek, []byte("1"), vault.Binding{OrgID: string(org.ID), CredentialID: credID, ServerID: srv.ID, Purpose: "env:TEST_FLOW_HELPER", KeyVersion: version, RowVersion: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CreateServer(ctx, srv, []store.Credential{{ID: credID, ServerID: srv.ID, Purpose: "env:TEST_FLOW_HELPER", KeyVersion: version, RowVersion: 1, Ciphertext: ct}}); err != nil {
			t.Fatal(err)
		}
		return srv
	}
	srvA := addHelper("crm")
	srvB := addHelper("slack")
	if err := mgr.Sync(ctx, org.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{srvA.ID, srvB.ID} {
		mgr.WaitSettled(ctx, id, 10*time.Second)
		pins, err := st.ListPins(ctx, org.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		var keys []store.PinKey
		for _, p := range pins {
			keys = append(keys, store.PinKey{Kind: p.Kind, Name: p.Name, Hash: p.Hash})
		}
		if len(keys) == 0 {
			t.Fatalf("server %s discovered no pins", id)
		}
		if _, err := st.AcceptPins(ctx, org.ID, id, keys); err != nil {
			t.Fatal(err)
		}
	}

	env := &flowGatewayEnv{st: st, org: org.ID, aw: aw, mgr: mgr, gw: gw, srvA: srvA, srvB: srvB}
	t.Cleanup(func() {
		env.wg.Wait()
		aw.Close()
		mgr.Close()
	})
	return env
}

func (e *flowGatewayEnv) createGatewayUser(t *testing.T, person string) (store.User, string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: now, Actor: "test", Action: "seed", Decision: "allow", Outcome: "ok"}
	linkID := ids.New()
	if err := e.st.CreateSetupLink(ctx, e.org, store.NewSetupLink{ID: linkID, SecretHash: ids.New(), CreatedBy: "test", Now: now, ExpiresAt: now.Add(time.Hour)}, ev); err != nil {
		t.Fatal(err)
	}
	ev.ID, ev.CallID = ids.New(), ids.New()
	u, err := e.st.ClaimSetup(ctx, e.org, store.Claim{
		LinkID: linkID, UserID: ids.New(), Person: person, Subject: person, Login: person, DisplayName: person,
		Session: store.Session{ID: ids.New(), CSRF: "c", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}, Now: now,
	}, ev)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := token.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateToken(ctx, store.APIToken{
		OrgID: e.org, ID: tok.ID, Name: person + "-agent", Role: store.RoleMember, Kind: store.KindGateway,
		Person: person, UserID: u.ID, SecretHash: tok.SecretHash, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return u, tok.Plain
}

func (e *flowGatewayEnv) connectClient(t *testing.T, bearer string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	s, err := c.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             "http://privo.local/mcp",
		HTTPClient:           &http.Client{Transport: &handlerTransport{wg: &e.wg, handler: e.gw.Handler(), bearer: bearer}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestFlowRuleEscalatesSinkAfterUntrustedSource(t *testing.T) {
	ctx := context.Background()
	e := newFlowGatewayEnv(t)
	ada, adaTok := e.createGatewayUser(t, "ada")
	bob, _ := e.createGatewayUser(t, "bob")

	// Tag crm as server-wide untrusted_source, and slack/send_item + slack/greet + slack/flow://note as egress_sink.
	if err := e.st.SetFlowTags(ctx, e.org, e.srvA.ID, []store.FlowTag{{ToolName: "", Tag: store.FlowTagUntrustedSource}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetFlowTags(ctx, e.org, e.srvB.ID, []store.FlowTag{
		{ToolName: "send_item", Tag: store.FlowTagEgressSink},
		{ToolName: "greet", Tag: store.FlowTagEgressSink},
		{ToolName: "flow://note", Tag: store.FlowTagEgressSink},
	}); err != nil {
		t.Fatal(err)
	}

	// Give ada an explicit allow rule AND an F10 scoped grant on slack/send_item:
	// flow rule MUST still escalate both to ask when tainted!
	if err := e.st.CreatePolicies(ctx, e.org,
		store.Policy{ID: "allow-rule", Server: "slack", Tool: "send_item", Subject: "user:" + ada.ID, Action: store.ActionAllow, Priority: 5, CreatedBy: "test", CreatedAt: time.Now()},
		store.Policy{ID: "grant-rule", Server: "slack", Tool: "send_item", Subject: "user:" + ada.ID, Action: store.ActionAllow, Priority: 100, GrantOf: "prior-appr", CreatedBy: "bob", CreatedAt: time.Now()},
	); err != nil {
		t.Fatal(err)
	}

	client := e.connectClient(t, adaTok)

	// 1. Before any untrusted source read, sink succeeds cleanly.
	res, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "slack__send_item", Arguments: map[string]any{"text": "clean"}})
	if err != nil || res.IsError {
		t.Fatalf("clean sink call failed: res=%+v err=%v", res, err)
	}
	// Untagged tool on slack does not escalate even later.
	res, err = client.CallTool(ctx, &mcp.CallToolParams{Name: "slack__read_item", Arguments: map[string]any{"text": "ok"}})
	if err != nil || res.IsError {
		t.Fatalf("untagged tool failed: res=%+v err=%v", res, err)
	}

	// 2. Reading from crm (untrusted_source) taints ada's session.
	res, err = client.CallTool(ctx, &mcp.CallToolParams{Name: "crm__read_item", Arguments: map[string]any{"text": "secret-lead"}})
	if err != nil || res.IsError {
		t.Fatalf("source read failed: res=%+v err=%v", res, err)
	}

	// 3. Calling slack__send_item now escalates to ask (even over allow-rule and grant-rule!).
	res, err = client.CallTool(ctx, &mcp.CallToolParams{Name: "slack__send_item", Arguments: map[string]any{"text": "exfil"}})
	if err != nil || !res.IsError {
		t.Fatalf("expected error tool result asking for approval, got res=%+v err=%v", res, err)
	}
	msg := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(msg, "Approval required") {
		t.Fatalf("expected approval required message, got %q", msg)
	}

	// 4. Verify pending approval carries the cross-server flow reason.
	pending, err := e.st.ListApprovals(ctx, e.org, store.ApprovalPending, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, err=%v", pending, err)
	}
	if !strings.Contains(pending[0].Reason, "cross-server flow: read from crm/read_item") {
		t.Fatalf("approval reason = %q, want cross-server flow explanation", pending[0].Reason)
	}

	// 5. prompts/get and resources/read on egress_sink fail closed on ask as denied.
	if _, err := client.GetPrompt(ctx, &mcp.GetPromptParams{Name: "slack__greet"}); err == nil {
		t.Fatal("tainted prompt sink succeeded, expected denied")
	}
	if _, err := client.ReadResource(ctx, &mcp.ReadResourceParams{URI: "privo://slack/flow://note"}); err == nil {
		t.Fatal("tainted resource sink succeeded, expected denied")
	}

	// 6. Approving lets the exact call through once.
	if err := e.gw.Approval.Decide(ctx, e.org, pending[0].ID, approval.Decider{UserID: bob.ID, Name: bob.DisplayName, Person: bob.Person}, true); err != nil {
		t.Fatal(err)
	}
	res, err = client.CallTool(ctx, &mcp.CallToolParams{Name: "slack__send_item", Arguments: map[string]any{"text": "exfil"}})
	if err != nil || res.IsError {
		t.Fatalf("approved retry failed: res=%+v err=%v", res, err)
	}

	// 7. A resource/read on untrusted_source also taints the caller.
	e.gw.flowTracker().RecordSource(string(e.org), "user:"+ada.ID, "", "crm", "reset", time.Now().Add(-time.Hour)) // push old source outside 10m window
	if _, err := client.ReadResource(ctx, &mcp.ReadResourceParams{URI: "privo://crm/flow://note"}); err != nil {
		t.Fatalf("source resource read failed: %v", err)
	}
	res, err = client.CallTool(ctx, &mcp.CallToolParams{Name: "slack__send_item", Arguments: map[string]any{"text": "after-resource"}})
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "Approval required") {
		t.Fatalf("expected approval after resource source read, got res=%+v err=%v", res, err)
	}
}

func TestFlowRuleNeverOverridesDeny(t *testing.T) {
	ctx := context.Background()
	e := newFlowGatewayEnv(t)
	ada, adaTok := e.createGatewayUser(t, "ada")

	if err := e.st.SetFlowTags(ctx, e.org, e.srvA.ID, []store.FlowTag{{ToolName: "", Tag: store.FlowTagUntrustedSource}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetFlowTags(ctx, e.org, e.srvB.ID, []store.FlowTag{{ToolName: "send_item", Tag: store.FlowTagEgressSink}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreatePolicies(ctx, e.org,
		store.Policy{ID: "deny-sink", Server: "slack", Tool: "send_item", Subject: "user:" + ada.ID, Action: store.ActionDeny, Priority: 1, CreatedBy: "test", CreatedAt: time.Now()},
	); err != nil {
		t.Fatal(err)
	}

	client := e.connectClient(t, adaTok)
	if res, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "crm__read_item", Arguments: map[string]any{"text": "x"}}); err != nil || res.IsError {
		t.Fatalf("source read failed: res=%+v err=%v", res, err)
	}
	res, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "slack__send_item", Arguments: map[string]any{"text": "y"}})
	if err != nil || !res.IsError {
		t.Fatalf("expected denial, got res=%+v err=%v", res, err)
	}
	if msg := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(msg, "Denied") {
		t.Fatalf("deny was overridden: got %q", msg)
	}
	if pending, _ := e.st.ListApprovals(ctx, e.org, store.ApprovalPending, 10); len(pending) != 0 {
		t.Fatalf("denied call created an approval: %+v", pending)
	}
}

type flowUnitStore struct {
	Store
	tags       []store.FlowTag
	tagsErr    error
	flowWindow string
	settingErr error
}

func (s *flowUnitStore) ListPolicies(context.Context, store.OrgID) ([]store.Policy, error) {
	return nil, nil
}
func (s *flowUnitStore) Setting(_ context.Context, _ store.OrgID, key, def string) (string, error) {
	if s.settingErr != nil {
		return "", s.settingErr
	}
	if key == SettingPolicyFlowWindow {
		return s.flowWindow, nil
	}
	return def, nil
}
func (s *flowUnitStore) OrgFlowTags(context.Context, store.OrgID) ([]store.FlowTag, error) {
	return s.tags, s.tagsErr
}
func (s *flowUnitStore) AccessFor(context.Context, store.OrgID, string) (store.Access, error) {
	return store.Access{Teams: []string{store.TeamEveryone}, Roles: []string{store.RoleMember}}, nil
}

func TestFlowDecideEdgeCasesFailClosed(t *testing.T) {
	ctx := context.Background()
	sinkItem := upstream.Item{ServerID: "srv-sink", Alias: "slack", Kind: upstream.KindTool, Name: "send", Hash: "h1"}

	t.Run("store OrgFlowTags failure fails closed", func(t *testing.T) {
		g := &Gateway{Store: &flowUnitStore{tagsErr: errors.New("db down")}}
		_, err := g.decide(ctx, call{p: auth.Principal{OrgID: "o", UserID: "u1"}, item: sinkItem, action: "tools/call"}, "d", "c1")
		if err == nil {
			t.Fatal("expected decide to fail closed when OrgFlowTags fails")
		}
	})

	t.Run("unidentified caller on egress_sink fails closed", func(t *testing.T) {
		g := &Gateway{
			Store:  &flowUnitStore{tags: []store.FlowTag{{ServerID: "srv-sink", ToolName: "", Tag: store.FlowTagEgressSink}}},
			Logger: slog.New(slog.DiscardHandler),
			Audit:  audit.NewWriter(nopAuditSink{}, audit.Options{}),
		}
		defer g.Audit.Close()
		_, err := g.decide(ctx, call{p: auth.Principal{OrgID: "o"}, item: sinkItem, action: "tools/call"}, "d", "c1")
		var r *refusal
		if !errors.As(err, &r) || r.class != "approvals_unavailable" {
			t.Fatalf("expected approvals_unavailable escalation for unidentified caller, got %v", err)
		}
	})

	t.Run("invalid flow_window in DB falls back to default 10m", func(t *testing.T) {
		st := &flowUnitStore{
			tags:       []store.FlowTag{{ServerID: "srv-sink", ToolName: "send", Tag: store.FlowTagEgressSink}},
			flowWindow: "not-a-duration",
		}
		g := &Gateway{Store: st, Logger: slog.New(slog.DiscardHandler), Audit: audit.NewWriter(nopAuditSink{}, audit.Options{})}
		defer g.Audit.Close()
		g.flowTracker().RecordSource("o", "user:u1", "claude", "crm", "read", time.Now().Add(-5*time.Minute))
		_, err := g.decide(ctx, call{p: auth.Principal{OrgID: "o", UserID: "u1", Client: "claude"}, item: sinkItem, action: "tools/call"}, "d", "c1")
		var r *refusal
		if !errors.As(err, &r) || r.class != "approvals_unavailable" {
			t.Fatalf("expected escalation under default 10m fallback, got %v", err)
		}
	})

	t.Run("prompts/get on untrusted_source does not taint", func(t *testing.T) {
		st := &flowUnitStore{
			tags: []store.FlowTag{{ServerID: "srv-src", ToolName: "", Tag: store.FlowTagUntrustedSource}},
		}
		g := &Gateway{Store: st, Logger: slog.New(slog.DiscardHandler)}
		srcPrompt := upstream.Item{ServerID: "srv-src", Alias: "crm", Kind: upstream.KindPrompt, Name: "p1", Hash: "h1"}
		dec, err := g.decide(ctx, call{p: auth.Principal{OrgID: "o", UserID: "u1"}, item: srcPrompt, action: "prompts/get"}, "d", "c1")
		if err != nil {
			t.Fatal(err)
		}
		// Even with dec.isSource true, prompts/get does not call RecordSource in run,
		// and CheckSink stays untainted.
		if !dec.isSource {
			t.Fatal("expected item tag detected")
		}
		if tainted, _, _ := g.flowTracker().CheckSink("o", "user:u1", "", policy.DefaultFlowWindow, time.Now()); tainted {
			t.Fatal("prompts/get should not record source taint")
		}
	})

	t.Run("refused untrusted_source call does not taint", func(t *testing.T) {
		st := &flowUnitStore{
			tags: []store.FlowTag{{ServerID: "srv-src", ToolName: "", Tag: store.FlowTagUntrustedSource}},
		}
		g := &Gateway{
			Store: st, Upstream: upstream.New(upstream.Options{}),
			Audit: audit.NewWriter(nopAuditSink{}, audit.Options{}), Logger: slog.New(slog.DiscardHandler),
		}
		defer func() {
			g.Audit.Close()
			g.Upstream.Close()
		}()
		srcTool := upstream.Item{ServerID: "srv-src", Alias: "crm", Kind: upstream.KindTool, Name: "read", Hash: "h1"}
		// g.session fails with ErrUnavailable: call never started upstream -> no taint!
		err := g.run(ctx, call{p: auth.Principal{OrgID: "o", UserID: "u1"}, item: srcTool, action: "tools/call", args: json.RawMessage(`{}`)},
			func(context.Context, *mcp.ClientSession) (string, any, error) {
				return "ok", nil, nil
			})
		if !errors.Is(err, upstream.ErrUnavailable) {
			t.Fatalf("expected ErrUnavailable, got %v", err)
		}
		if tainted, _, _ := g.flowTracker().CheckSink("o", "user:u1", "", policy.DefaultFlowWindow, time.Now()); tainted {
			t.Fatal("failed upstream connect recorded source taint")
		}
	})
}

type nopAuditSink struct{}

func (nopAuditSink) InsertAudit(context.Context, store.OrgID, ...store.AuditEvent) error {
	return nil
}

func TestFlowWindowRespectsConfiguredSetting(t *testing.T) {
	ctx := context.Background()
	st := &flowUnitStore{
		tags:       []store.FlowTag{{ServerID: "srv-sink", ToolName: "send", Tag: store.FlowTagEgressSink}},
		flowWindow: "15s",
	}
	g := &Gateway{Store: st, Logger: slog.New(slog.DiscardHandler)}
	sinkItem := upstream.Item{ServerID: "srv-sink", Alias: "slack", Kind: upstream.KindTool, Name: "send", Hash: "h1"}
	g.flowTracker().RecordSource("o", "user:u1", "", "crm", "read", time.Now().Add(-30*time.Second))
	dec, err := g.decide(ctx, call{p: auth.Principal{OrgID: "o", UserID: "u1"}, item: sinkItem, action: "tools/call"}, "d", "c1")
	if err != nil || dec.label != store.ActionAllow {
		t.Fatalf("expected allow after 15s window expired, got dec=%+v err=%v", dec, err)
	}
}

func TestFlowGatewayCallerKeyAndToolLevelTags(t *testing.T) {
	if got := flowCallerKey(auth.Principal{UserID: "u1", TokenID: "t1"}); got != "user:u1" {
		t.Fatalf("user principal key = %q, want user:u1", got)
	}
	if got := flowCallerKey(auth.Principal{TokenID: "t1"}); got != "token:t1" {
		t.Fatalf("token-only principal key = %q, want token:t1", got)
	}
	if got := flowCallerKey(auth.Principal{}); got != "" {
		t.Fatalf("empty principal key = %q, want empty", got)
	}

	tags := []store.FlowTag{
		{ServerID: "srv-src", ToolName: "read_item", Tag: store.FlowTagUntrustedSource},
		{ServerID: "srv-sink", ToolName: "", Tag: store.FlowTagEgressSink},
	}
	if src, sink := itemFlowTags(tags, "srv-src", "read_item"); !src || sink {
		t.Fatalf("srv-src/read_item: src=%v sink=%v", src, sink)
	}
	if src, sink := itemFlowTags(tags, "srv-src", "other_tool"); src || sink {
		t.Fatalf("srv-src/other_tool: src=%v sink=%v", src, sink)
	}
	if src, sink := itemFlowTags(tags, "srv-sink", "any_tool"); src || !sink {
		t.Fatalf("srv-sink/any_tool: src=%v sink=%v", src, sink)
	}

	// Client isolation inside Gateway.decide: claude taint does not taint cursor.
	ctx := context.Background()
	st := &flowUnitStore{tags: tags}
	g := &Gateway{Store: st, Logger: slog.New(slog.DiscardHandler), Audit: audit.NewWriter(nopAuditSink{}, audit.Options{})}
	defer g.Audit.Close()
	g.flowTracker().RecordSource("o", "user:u1", "claude", "crm", "read_item", time.Now())
	sinkItem := upstream.Item{ServerID: "srv-sink", Alias: "slack", Kind: upstream.KindTool, Name: "send", Hash: "h1"}

	dec, err := g.decide(ctx, call{p: auth.Principal{OrgID: "o", UserID: "u1", Client: "cursor"}, item: sinkItem, action: "tools/call"}, "d", "c1")
	if err != nil || dec.label != store.ActionAllow {
		t.Fatalf("cursor client was tainted by claude: dec=%+v err=%v", dec, err)
	}
	_, err = g.decide(ctx, call{p: auth.Principal{OrgID: "o", UserID: "u1", Client: "claude"}, item: sinkItem, action: "tools/call"}, "d", "c2")
	var r *refusal
	if !errors.As(err, &r) || r.class != "approvals_unavailable" {
		t.Fatalf("claude client did not escalate: err=%v", err)
	}
}
