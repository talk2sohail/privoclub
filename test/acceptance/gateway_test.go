//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	envSecret    = "super-secret-value-7d1e"
	headerSecret = "super-header-secret-41aa"
)

// Ported from "aggregates a stdio server and refuses a missing token".
// New per ADRs: tools stay hidden until their pins are accepted (0001, 0002);
// the 401 points clients at protected resource metadata (0003); secrets are
// never stored in plain text (0003).
func TestAggregatesStdioServerAndRejectsMissingToken(t *testing.T) {
	g := startGateway(t)

	code, _ := g.raw(http.MethodPost, "/mcp", "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", code)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, g.base+"/mcp", strings.NewReader("{}"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatalf("401 lacks resource_metadata in WWW-Authenticate: %q", resp.Header.Get("WWW-Authenticate"))
	}

	created := g.registerEcho("echo", nil, map[string]string{"SECRET": envSecret})
	if b, _ := json.Marshal(created); strings.Contains(string(b), envSecret) {
		t.Fatal("registration response leaks the env secret")
	}

	s := g.connect(g.agent)
	if names := toolNames(t, s); len(names) != 0 {
		t.Fatalf("tools visible before pin review: %v", names)
	}

	g.acceptPins("echo")
	g.eventually("echo__echo listed", 5*time.Second, func() bool {
		return slices.Equal(toolNames(t, s), []string{"echo__echo"})
	})
	if got := callEcho(t, s, "hello"); got != "hello" {
		t.Fatalf("echo returned %q", got)
	}

	_, body := g.raw(http.MethodGet, "/api/v1/servers", g.owner, nil)
	if strings.Contains(string(body), envSecret) {
		t.Fatal("server list leaks the env secret")
	}
	if status := g.serverStatus("echo"); status != "up" {
		t.Fatalf("echo status %q, want up", status)
	}
	g.assertNotOnDisk(envSecret)
}

// Ported from "registers a remote server without leaking its header".
// Changed per ADRs: loopback targets are refused unless allow-listed (0008);
// the header is encrypted at rest instead of stored in a config file (0003).
func TestRemoteServerSecretsNeverLeak(t *testing.T) {
	remote := map[string]any{
		"alias":     "internal",
		"transport": "http",
		"url":       "http://127.0.0.1:1/mcp",
		"headers":   map[string]string{"Authorization": "Bearer " + headerSecret},
	}

	blocked := startGateway(t)
	code, body := blocked.raw(http.MethodPost, "/api/v1/servers", blocked.owner, remote)
	if code != http.StatusUnprocessableEntity || !strings.Contains(strings.ToLower(string(body)), "egress") {
		t.Fatalf("loopback registration: status %d body %s, want 422 mentioning egress", code, body)
	}

	g := startGateway(t, "PRIVO_EGRESS_ALLOW_CIDRS=127.0.0.1/32")
	created := g.api(http.MethodPost, "/api/v1/servers", g.owner, remote, http.StatusCreated)
	if b, _ := json.Marshal(created); strings.Contains(string(b), headerSecret) {
		t.Fatal("registration response leaks the header")
	}
	g.eventually("internal reported down", 10*time.Second, func() bool { return g.serverStatus("internal") == "down" })
	g.assertNotOnDisk(headerSecret)

	g.api(http.MethodDelete, "/api/v1/servers/internal", g.owner, nil, http.StatusOK)
	if status := g.serverStatus("internal"); status != "" {
		t.Fatalf("internal still listed with status %q", status)
	}
}

// Ported from "personal tokens can call tools and cannot change the gateway".
// Changed per ADRs: revocation must take effect within 5 seconds (0003);
// members cannot read the audit log (0011).
func TestMemberTokenCannotAdminister(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("echo", nil, nil)
	g.acceptPins("echo")

	issued := g.api(http.MethodPost, "/api/v1/tokens", g.owner, map[string]any{"name": "ada", "role": "member"}, http.StatusCreated)
	token, _ := issued["token"].(string)
	id, _ := issued["id"].(string)
	if !strings.HasPrefix(token, "privo_pat_") || id == "" {
		t.Fatalf("issued %v", issued)
	}
	_, list := g.raw(http.MethodGet, "/api/v1/tokens", g.owner, nil)
	if strings.Contains(string(list), token) {
		t.Fatal("token list contains the token secret")
	}
	g.assertNotOnDisk(token)

	s := g.connect(token)
	g.eventually("member sees echo__echo", 5*time.Second, func() bool {
		return slices.Equal(toolNames(t, s), []string{"echo__echo"})
	})

	// A gateway token never reaches the admin API (401), whatever its role.
	if code, _ := g.raw(http.MethodPost, "/api/v1/servers", token, map[string]any{"alias": "nope", "transport": "http", "url": "https://example.com/mcp"}); code != http.StatusUnauthorized {
		t.Fatalf("member registering a server: status %d, want 401", code)
	}
	if code, _ := g.raw(http.MethodGet, "/api/v1/audit/events", token, nil); code != http.StatusUnauthorized {
		t.Fatalf("member reading audit: status %d, want 401", code)
	}
	auditor := g.issueControl("audrey", "auditor", "audrey")
	if code, _ := g.raw(http.MethodPost, "/api/v1/servers", auditor, map[string]any{"alias": "nope", "transport": "http", "url": "https://example.com/mcp"}); code != http.StatusForbidden {
		t.Fatalf("auditor registering a server: status %d, want 403", code)
	}

	g.api(http.MethodDelete, "/api/v1/tokens/"+id, g.owner, nil, http.StatusOK)
	g.eventually("revoked token rejected within 5s", 6*time.Second, func() bool {
		code, _ := g.raw(http.MethodPost, "/mcp", token, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
		return code == http.StatusUnauthorized
	})
}

func auditEvents(g *gateway) []map[string]any {
	g.t.Helper()
	res := g.api(http.MethodGet, "/api/v1/audit/events?limit=50", g.owner, nil, http.StatusOK)
	raw, _ := res["events"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// callEvents returns completed echo calls, newest first. Completions are
// written asynchronously in batches (ADR 0006), so a call can briefly show
// as "pending".
func callEvents(g *gateway) []map[string]any {
	var calls []map[string]any
	for _, e := range auditEvents(g) {
		if e["tool"] == "echo" && e["outcome"] != "pending" {
			calls = append(calls, e)
		}
	}
	return calls
}

// Ported from "audit log hides arguments until the admin records them".
// Changed per ADRs: recording is off by default and switchable (0006);
// changing audit settings needs an admin role (0011).
func TestAuditRecordsCallsWithoutArgumentsByDefault(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("echo", nil, nil)
	g.acceptPins("echo")
	s := g.connect(g.agent)
	g.eventually("echo listed", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s), "echo__echo") })

	const first, second = "needle-arg-9f3c", "needle-after-9f3c"
	callEcho(t, s, first)
	g.eventually("first call audited", 5*time.Second, func() bool { return len(callEvents(g)) == 1 })
	ev := callEvents(g)[0]
	if ev["server"] != "echo" || ev["decision"] != "allow" || ev["outcome"] != "ok" || ev["actor"] == nil {
		t.Fatalf("audit event %v", ev)
	}
	if _, ok := ev["arguments"]; ok {
		t.Fatalf("arguments recorded by default: %v", ev)
	}

	g.api(http.MethodPatch, "/api/v1/settings/audit", g.owner, map[string]any{"record_arguments": true}, http.StatusOK)
	callEcho(t, s, first)
	g.eventually("second call audited", 5*time.Second, func() bool { return len(callEvents(g)) == 2 })
	if args, _ := json.Marshal(callEvents(g)[0]["arguments"]); !strings.Contains(string(args), first) {
		t.Fatalf("arguments not recorded while switched on: %s", args)
	}

	g.api(http.MethodPatch, "/api/v1/settings/audit", g.owner, map[string]any{"record_arguments": false}, http.StatusOK)
	callEcho(t, s, second)
	g.eventually("third call audited", 5*time.Second, func() bool { return len(callEvents(g)) == 3 })
	events := callEvents(g)
	if _, ok := events[0]["arguments"]; ok {
		t.Fatal("arguments recorded after switching off")
	}
	if args, _ := json.Marshal(events[1]["arguments"]); !strings.Contains(string(args), first) {
		t.Fatal("arguments recorded earlier were lost when switching off")
	}

	member := g.api(http.MethodPost, "/api/v1/tokens", g.owner, map[string]any{"name": "bea", "role": "member", "kind": "gateway"}, http.StatusCreated)
	memberToken, _ := member["token"].(string)
	if code, _ := g.raw(http.MethodPatch, "/api/v1/settings/audit", memberToken, map[string]any{"record_results": true}); code != http.StatusUnauthorized {
		t.Fatalf("member gateway token changing audit settings: status %d, want 401", code)
	}
}

// Ported from "aggregates prompts and resources and skips a tools-only server".
// Changed per ADRs: resource URIs are rewritten to privo://<alias>/... (0001).
func TestPromptsAndResourcesAreAggregated(t *testing.T) {
	g := startGateway(t)
	flagFile := filepath.Join(t.TempDir(), "flag")
	g.registerEcho("echo", nil, nil)
	g.registerEcho("only", []string{"-mode", "tools-only"}, map[string]string{"FLAG": flagFile})
	g.acceptPins("echo")
	g.acceptPins("only")

	s := g.connect(g.agent)
	ctx := context.Background()
	g.eventually("both servers listed", 5*time.Second, func() bool {
		names := toolNames(t, s)
		return slices.Contains(names, "echo__echo") && slices.Contains(names, "only__ping")
	})

	prompts, err := s.ListPrompts(ctx, &mcp.ListPromptsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts.Prompts) != 1 || prompts.Prompts[0].Name != "echo__greet" {
		t.Fatalf("prompts %+v", prompts.Prompts)
	}
	greeting, err := s.GetPrompt(ctx, &mcp.GetPromptParams{Name: "echo__greet", Arguments: map[string]string{"name": "Ada"}})
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := greeting.Messages[0].Content.(*mcp.TextContent); !ok || text.Text != "Hello Ada" {
		t.Fatalf("greeting %+v", greeting.Messages[0].Content)
	}

	resources, err := s.ListResources(ctx, &mcp.ListResourcesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Resources) != 1 || resources.Resources[0].Name != "echo__note" ||
		!strings.HasPrefix(resources.Resources[0].URI, "privo://echo/") {
		t.Fatalf("resources %+v", resources.Resources)
	}
	note, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: resources.Resources[0].URI})
	if err != nil {
		t.Fatal(err)
	}
	if note.Contents[0].Text != "note-body" {
		t.Fatalf("note %+v", note.Contents[0])
	}

	if _, err := os.Stat(flagFile); !os.IsNotExist(err) {
		b, _ := os.ReadFile(flagFile)
		t.Fatalf("gateway asked a tools-only server for prompts or resources: %s", b)
	}
}

// Ported from "retries a down server and can disable it without deleting config".
func TestRetriesDownServerAndDisables(t *testing.T) {
	g := startGateway(t)
	marker := filepath.Join(t.TempDir(), "marker")
	g.registerEcho("flaky", []string{"-mode", "flaky"}, map[string]string{"MARKER": marker})

	g.eventually("flaky comes up after a retry", 10*time.Second, func() bool { return g.serverStatus("flaky") == "up" })
	if b, _ := os.ReadFile(marker); len(b) < 2 {
		t.Fatalf("server started %d times, want at least 2", len(b))
	}

	g.api(http.MethodPatch, "/api/v1/servers/flaky", g.owner, map[string]any{"enabled": false}, http.StatusOK)
	g.eventually("flaky disabled", 5*time.Second, func() bool { return g.serverStatus("flaky") == "disabled" })
}
