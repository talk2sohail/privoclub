package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
	"privo.club/privo/internal/upstream"
)

// ADR 0017: a call to a server that runs with each person's own account tells
// the person what to do, and the text reaches the client unchanged.

func TestNoGrantGivesTheConnectLink(t *testing.T) {
	g := &Gateway{BaseURL: "https://privo.example/", Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	c := call{item: upstream.Item{Alias: "linear"}, action: "tools/call"}
	err := g.clientFacing(c, fmt.Errorf("wrapped: %w", g.connectFirst("linear")))
	if err == nil || !strings.Contains(err.Error(), "https://privo.example/connect/linear") || strings.Contains(err.Error(), "//connect") {
		t.Fatalf("client sees %v", err)
	}
	// The link always points to Privo, never to the provider; without a public
	// URL there is no link at all.
	g.BaseURL = ""
	if err := g.connectFirst("linear"); strings.Contains(err.Error(), "http") {
		t.Fatalf("link without a public URL: %v", err)
	}
	if err := (&Gateway{BaseURL: "https://privo.example"}).connectFirst("a/b"); !strings.Contains(err.Error(), "/connect/a%2Fb") {
		t.Fatalf("alias not escaped: %v", err)
	}
}

// F27: a token that belongs to no member is told to make its own, with the
// page to do it on; nothing in the text names anyone's account.
func TestTokenOfNobodyIsToldToMakeItsOwn(t *testing.T) {
	g := &Gateway{BaseURL: "https://privo.example/", Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	c := call{item: upstream.Item{Alias: "linear"}, action: "tools/call"}
	err := g.clientFacing(c, fmt.Errorf("wrapped: %w", g.noAccount("linear")))
	if err == nil || !strings.Contains(err.Error(), `"linear"`) || !strings.Contains(err.Error(), "https://privo.example/admin/tokens") || strings.Contains(err.Error(), "//admin") {
		t.Fatalf("client sees %v", err)
	}
	g.BaseURL = ""
	if err := g.noAccount("linear"); strings.Contains(err.Error(), "http") || !strings.Contains(err.Error(), "Tokens page") {
		t.Fatalf("without a public URL: %v", err)
	}
}

type scopeGateStore struct {
	servers []store.Server
	pins    map[store.PinKey]string
	users   map[string]store.User
}

func (s *scopeGateStore) ListServers(context.Context, store.OrgID) ([]store.Server, error) {
	return s.servers, nil
}
func (s *scopeGateStore) ServerCredentials(context.Context, store.OrgID, string) ([]store.Credential, error) {
	return nil, nil
}
func (s *scopeGateStore) RecordSeen(_ context.Context, _ store.OrgID, pins []store.Pin) error {
	for _, p := range pins {
		s.pins[store.PinKey{Kind: p.Kind, Name: p.Name, Hash: p.Hash}] = store.PinAccepted
	}
	return nil
}
func (s *scopeGateStore) PrunePending(context.Context, store.OrgID, string, []store.PinKey) (int, error) {
	return 0, nil
}
func (s *scopeGateStore) PinStatuses(context.Context, store.OrgID, string) (map[store.PinKey]string, error) {
	out := make(map[store.PinKey]string, len(s.pins))
	for k, v := range s.pins {
		out[k] = v
	}
	return out, nil
}
func (s *scopeGateStore) Setting(_ context.Context, _ store.OrgID, _, def string) (string, error) {
	return def, nil
}
func (s *scopeGateStore) ListPolicies(context.Context, store.OrgID) ([]store.Policy, error) {
	return nil, nil
}
func (s *scopeGateStore) UserByID(_ context.Context, _ store.OrgID, id string) (store.User, error) {
	u, ok := s.users[id]
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	return u, nil
}
func (s *scopeGateStore) AccessFor(context.Context, store.OrgID, string) (store.Access, error) {
	return store.Access{Servers: map[string]bool{"srv-linear": true}, Teams: []string{store.TeamEveryone}, Roles: []string{store.RoleMember}}, nil
}

type scopeGrants struct {
	mu      sync.Mutex
	tokens  map[string]string
	catalog string
}

func (g *scopeGrants) AccessToken(_ context.Context, _ store.OrgID, _ store.Server, userID string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	t, ok := g.tokens[userID]
	if !ok {
		return "", errors.New("not connected")
	}
	return t, nil
}
func (g *scopeGrants) CatalogAccessToken(ctx context.Context, org store.OrgID, srv store.Server) (string, string, error) {
	t, err := g.AccessToken(ctx, org, srv, g.catalog)
	return g.catalog, t, err
}
func (g *scopeGrants) RefreshAfterReject(_ context.Context, _ store.OrgID, _ store.Server, userID, _ string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tokens[userID], nil
}

type memRT func(*http.Request) (*http.Response, error)

func (f memRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInsufficientScopeNamesTheScope(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "linear", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "write", Description: "Write an issue."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
		})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	var hdr atomic.Value
	hdr.Store(`Bearer error="insufficient_scope", scope="write repo"`)
	httpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if bytes.Contains(body, []byte(`"tools/call"`)) {
			w.Header().Set("WWW-Authenticate", hdr.Load().(string))
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "RAW_SECRET_BODY")
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})

	st := &scopeGateStore{
		servers: []store.Server{{OrgID: "o", ID: "srv-linear", Alias: "linear", Transport: "http", Enabled: true,
			Config: store.ServerConfig{URL: "https://linear.example/mcp", Auth: store.AuthOAuth, OAuth: &store.ServerOAuth{Resource: "https://linear.example/mcp"}}}},
		pins:  map[store.PinKey]string{},
		users: map[string]store.User{"u-ada": {ID: "u-ada", Person: "ada", Roles: []string{store.RoleMember}}},
	}
	gs := &scopeGrants{tokens: map[string]string{"boss": "boss-tok", "u-ada": "ada-tok"}, catalog: "boss"}
	sink := &memSink{}
	rec := telemetry.New(sink, telemetry.Options{})
	aw := audit.NewWriter(sink, audit.Options{})
	mgr := upstream.New(upstream.Options{
		Store:  st,
		Grants: gs,
		HTTPClient: &http.Client{Transport: memRT(func(r *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			httpHandler.ServeHTTP(rec, r)
			return rec.Result(), nil
		})},
		RetryBase:      time.Hour,
		RetryMax:       time.Hour,
		ConnectTimeout: 5 * time.Second,
	})
	defer mgr.Close()
	if err := mgr.Sync(context.Background(), "o"); err != nil {
		t.Fatal(err)
	}
	mgr.WaitSettled(context.Background(), "srv-linear", 5*time.Second)

	var logs bytes.Buffer
	g := &Gateway{
		Store:     st,
		Upstream:  mgr,
		Audit:     aw,
		Telemetry: rec,
		Logger:    slog.New(slog.NewTextHandler(&logs, nil)),
		BaseURL:   "https://privo.example/",
	}
	p := auth.Principal{OrgID: "o", TokenID: "tok-ada", Name: "ada-laptop", UserID: "u-ada", Person: "ada", Client: "claude", Roles: []string{store.RoleMember}}
	res, err := g.callTool(context.Background(), p, &mcp.CallToolParamsRaw{Name: "linear__write"})
	if err != nil {
		t.Fatalf("callTool err = %v", err)
	}
	tr, ok := res.(*mcp.CallToolResult)
	if !ok || !tr.IsError || len(tr.Content) == 0 {
		t.Fatalf("callTool result = %#v", res)
	}
	msg := tr.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"linear", "write repo", "https://privo.example/connect/linear?scope=write+repo"} {
		if !strings.Contains(msg, want) {
			t.Errorf("tool error %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "//connect") || strings.Contains(msg, "RAW_SECRET_BODY") {
		t.Errorf("tool error leaked or malformed URL: %q", msg)
	}

	// Malformed 403 challenge does not reflect header text.
	hdr.Store("Bearer error=\"insufficient_scope\", scope=\"bad\tHEADER_LEAK\"")
	resBad, err := g.callTool(context.Background(), p, &mcp.CallToolParamsRaw{Name: "linear__write"})
	if err != nil {
		t.Fatalf("malformed callTool err = %v", err)
	}
	badMsg := resBad.(*mcp.CallToolResult).Content[0].(*mcp.TextContent).Text
	if strings.Contains(badMsg, "HEADER_LEAK") {
		t.Errorf("malformed header leaked to client: %q", badMsg)
	}

	rec.Close()
	aw.Close()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.stats) < 1 || sink.stats[0].Outcome != store.OutcomeError || sink.stats[0].ErrorClass != "insufficient_scope" {
		t.Fatalf("telemetry stats = %+v, want outcome=error error_class=insufficient_scope", sink.stats)
	}
	var sawAdmin, sawCompletion bool
	for _, ev := range sink.audits {
		if ev.Phase == "admin" && ev.Action == "upstream.scope_needed:linear:write,repo" {
			sawAdmin = true
			if ev.Server != "linear" || ev.UserID != "u-ada" || ev.Actor != "ada-laptop" || ev.ActorID != "tok-ada" || ev.Outcome != "refused" || ev.Decision != "allow" {
				t.Errorf("admin scope_needed event = %+v", ev)
			}
		}
		if ev.Phase == "completion" && ev.Outcome == "error" {
			sawCompletion = true
		}
		if strings.Contains(ev.Action, "HEADER_LEAK") {
			t.Errorf("malformed header leaked to audit: %+v", ev)
		}
	}
	if !sawAdmin || !sawCompletion {
		t.Fatalf("audits missing expected records (admin=%v completion=%v): %+v", sawAdmin, sawCompletion, sink.audits)
	}

	// BaseURL empty emits no URL, and scopeNeededAction caps long lists.
	gNoURL := &Gateway{}
	noURLErr := gNoURL.insufficientScope("linear", []string{"write"})
	if strings.Contains(noURLErr.Error(), "http") || !strings.Contains(noURLErr.Error(), "write") {
		t.Errorf("without BaseURL: %q", noURLErr.Error())
	}
	many := make([]string, 32)
	for i := range many {
		many[i] = strings.Repeat("s", 20)
	}
	if act := scopeNeededAction("linear", many); len(act) > maxScopeNeededAction || !strings.HasPrefix(act, "upstream.scope_needed:linear:") {
		t.Errorf("scopeNeededAction length = %d (%q)", len(act), act)
	}
}
