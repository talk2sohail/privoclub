//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func readyEcho(t *testing.T, g *gateway) {
	t.Helper()
	g.registerEcho("echo", nil, nil)
	g.acceptPins("echo")
}

// F09: a deny rule for one role blocks that role only, and the refusal is audited.
func TestPolicyDeniesByRole(t *testing.T) {
	g := startGateway(t)
	readyEcho(t, g)
	member := g.issue("ada")

	if code, _ := g.raw(http.MethodPost, "/api/v1/policies", member, map[string]any{"server": "echo", "tool": "echo", "subject": "role:member", "action": "deny"}); code != http.StatusUnauthorized {
		t.Fatalf("member gateway token creating a policy: %d, want 401", code)
	}
	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{"server": "echo", "tool": "echo", "subject": "role:member", "action": "deny"}, http.StatusCreated)

	ms := g.connect(member)
	g.eventually("member sees echo", 5*time.Second, func() bool { return slices.Contains(toolNames(t, ms), "echo__echo") })
	if text, isErr := call(t, ms, "echo__echo", map[string]any{"text": "hi"}); !isErr || !strings.Contains(text, "Denied") {
		t.Fatalf("member call: %q (error=%v), want a denial", text, isErr)
	}
	os := g.connect(g.agent)
	if text, isErr := call(t, os, "echo__echo", map[string]any{"text": "hi"}); isErr || text != "hi" {
		t.Fatalf("owner call: %q (error=%v)", text, isErr)
	}
	g.eventually("denial audited", 5*time.Second, func() bool {
		for _, e := range auditEvents(g) {
			out, _ := e["outcome"].(string)
			if e["decision"] == "deny" && strings.HasPrefix(out, "denied") && e["actor"] == "ada" {
				return true
			}
		}
		return false
	})
}

// F09: reviewed annotations tighten the default; the read-only preset denies writes.
func TestDestructiveDefaultAndReadOnlyPreset(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("notes", []string{"-mode", "annotated"}, nil)
	g.acceptPins("notes")
	s := g.connect(g.agent)
	g.eventually("notes tools listed", 5*time.Second, func() bool { return len(toolNames(t, s)) == 2 })

	if text, isErr := call(t, s, "notes__read_note", nil); isErr || text != "note" {
		t.Fatalf("read_note: %q %v", text, isErr)
	}
	if text, isErr := call(t, s, "notes__delete_note", nil); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("destructive tool under default allow: %q, want an approval request", text)
	}

	res := g.api(http.MethodPost, "/api/v1/servers/notes/policy:preset", g.owner, map[string]any{"preset": "read-only"}, http.StatusCreated)
	if ps, _ := res["policies"].([]any); len(ps) != 2 {
		t.Fatalf("preset created %v", res["policies"])
	}
	if text, isErr := call(t, s, "notes__delete_note", nil); !isErr || !strings.Contains(text, "Denied") {
		t.Fatalf("delete_note under read-only preset: %q", text)
	}
	if text, isErr := call(t, s, "notes__read_note", nil); isErr || text != "note" {
		t.Fatalf("read_note under read-only preset: %q %v", text, isErr)
	}
}

// F10: ask, see the full arguments, approve once, run once.
func TestApprovalIsBoundToArgumentsAndSingleUse(t *testing.T) {
	g := startGateway(t)
	readyEcho(t, g)
	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{"server": "echo", "tool": "echo", "action": "ask"}, http.StatusCreated)
	member := g.issue("ada")
	approver := g.issueControl("bob", "approver", "bob")
	ms := g.connect(member)
	g.eventually("member sees echo", 5*time.Second, func() bool { return slices.Contains(toolNames(t, ms), "echo__echo") })

	text, isErr := call(t, ms, "echo__echo", map[string]any{"text": "deploy"})
	if !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("first call: %q", text)
	}
	if code, _ := g.raw(http.MethodGet, "/api/v1/approvals", member, nil); code != http.StatusUnauthorized {
		t.Fatalf("member gateway token listing approvals: %d, want 401", code)
	}
	pending := g.pendingApprovals(approver)
	if len(pending) != 1 {
		t.Fatalf("pending approvals: %v", pending)
	}
	ap := pending[0]
	args, _ := json.Marshal(ap["arguments"])
	if ap["requester"] != "ada" || ap["tool"] != "echo" || !strings.Contains(string(args), "deploy") {
		t.Fatalf("approval does not show the full request: %v", ap)
	}
	id, _ := ap["id"].(string)
	if !strings.Contains(text, id) {
		t.Fatalf("caller message %q does not name approval %s", text, id)
	}
	if code, _ := g.raw(http.MethodPost, "/api/v1/approvals/"+id+"/approve", member, nil); code != http.StatusUnauthorized {
		t.Fatalf("member gateway token approving: %d, want 401", code)
	}
	// Another token for the same person cannot approve either.
	adaApprover := g.issueControl("ada-approver", "approver", "ada")
	if code, _ := g.raw(http.MethodPost, "/api/v1/approvals/"+id+"/approve", adaApprover, nil); code != http.StatusForbidden {
		t.Fatalf("second token of the requester approving: %d, want 403", code)
	}
	g.api(http.MethodPost, "/api/v1/approvals/"+id+"/approve", approver, nil, http.StatusOK)

	if text, isErr := call(t, ms, "echo__echo", map[string]any{"text": "other"}); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("different arguments ran on someone else's approval: %q", text)
	}
	if text, isErr := call(t, ms, "echo__echo", map[string]any{"text": "deploy"}); isErr || text != "deploy" {
		t.Fatalf("approved retry: %q (error=%v)", text, isErr)
	}
	if text, isErr := call(t, ms, "echo__echo", map[string]any{"text": "deploy"}); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("approval was reused: %q", text)
	}
	g.eventually("approved call audited", 5*time.Second, func() bool {
		for _, e := range auditEvents(g) {
			if d, _ := e["decision"].(string); d == "approved:"+id && e["outcome"] == "ok" && strings.Contains(fmt.Sprint(e["approved_by"]), "bob") {
				return true
			}
		}
		return false
	})
}

// F10: a decision made while the call waits lets it continue; nobody approves
// their own call; a denial stands for the identical call.
func TestApprovalDuringHoldSelfApprovalAndDenial(t *testing.T) {
	g := startGateway(t, "PRIVO_APPROVAL_HOLD=5s")
	readyEcho(t, g)
	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{"server": "echo", "tool": "echo", "action": "ask"}, http.StatusCreated)
	admin := g.issueControl("carol", "admin", "carol")
	s := g.connect(g.agent)
	g.eventually("echo listed", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s), "echo__echo") })

	type result struct {
		text  string
		isErr bool
	}
	waitPending := func() string {
		var id string
		g.eventually("approval requested", 4*time.Second, func() bool {
			if p := g.pendingApprovals(admin); len(p) == 1 {
				id, _ = p[0]["id"].(string)
			}
			return id != ""
		})
		return id
	}

	done := make(chan result, 1)
	go func() {
		text, isErr := call(t, s, "echo__echo", map[string]any{"text": "held"})
		done <- result{text, isErr}
	}()
	id := waitPending()
	if code, _ := g.raw(http.MethodPost, "/api/v1/approvals/"+id+"/approve", g.owner, nil); code != http.StatusForbidden {
		t.Fatalf("self approval: %d, want 403", code)
	}
	g.api(http.MethodPost, "/api/v1/approvals/"+id+"/approve", admin, nil, http.StatusOK)
	if r := <-done; r.isErr || r.text != "held" {
		t.Fatalf("held call after approval: %+v", r)
	}

	go func() {
		text, isErr := call(t, s, "echo__echo", map[string]any{"text": "nope"})
		done <- result{text, isErr}
	}()
	id = waitPending()
	g.api(http.MethodPost, "/api/v1/approvals/"+id+"/deny", admin, nil, http.StatusOK)
	if r := <-done; !r.isErr || !strings.Contains(r.text, "denied") {
		t.Fatalf("held call after denial: %+v", r)
	}
	start := time.Now()
	if text, isErr := call(t, s, "echo__echo", map[string]any{"text": "nope"}); !isErr || !strings.Contains(text, "denied") {
		t.Fatalf("retry of a denied call: %q", text)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("retry of a denied call waited for a decision again")
	}
}

// The attack chain from the F10 review: an agent holding an owner gateway
// token must not be able to administer Privo or approve its own calls.
func TestGatewayTokensCannotAdministerOrApprove(t *testing.T) {
	g := startGateway(t)
	readyEcho(t, g)
	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{"server": "echo", "tool": "echo", "action": "ask"}, http.StatusCreated)
	s := g.connect(g.agent)
	g.eventually("echo listed", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s), "echo__echo") })
	_, _ = call(t, s, "echo__echo", map[string]any{"text": "x"})
	id, _ := g.pendingApprovals(g.owner)[0]["id"].(string)

	for _, try := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/approvals/" + id + "/approve"},
		{http.MethodPost, "/api/v1/tokens"},
		{http.MethodPatch, "/api/v1/settings/approvals"},
		{http.MethodPost, "/api/v1/policies"},
	} {
		if code, _ := g.raw(try.method, try.path, g.agent, map[string]any{}); code != http.StatusUnauthorized {
			t.Errorf("owner gateway token on %s %s: %d, want 401", try.method, try.path, code)
		}
	}
	// The same person's control token is refused as a self-approval.
	if code, _ := g.raw(http.MethodPost, "/api/v1/approvals/"+id+"/approve", g.owner, nil); code != http.StatusForbidden {
		t.Fatalf("owner approving their own agent's call: %d, want 403", code)
	}
	// Control tokens do not work on /mcp.
	code, _ := g.raw(http.MethodPost, "/mcp", g.owner, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if code != http.StatusUnauthorized {
		t.Fatalf("control token on /mcp: %d, want 401", code)
	}
	// Approvers get no gateway tokens.
	if code, _ := g.raw(http.MethodPost, "/api/v1/tokens", g.owner, map[string]any{"name": "x", "role": "approver", "kind": "gateway"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("approver gateway token: %d, want 422", code)
	}
}

// Rules cover prompts and resources too: a default deny blocks them.
func TestDefaultDenyCoversPromptsAndResources(t *testing.T) {
	g := startGateway(t)
	readyEcho(t, g)
	g.api(http.MethodPatch, "/api/v1/settings/policy", g.owner, map[string]any{"default": "deny"}, http.StatusOK)
	s := g.connect(g.agent)
	ctx := context.Background()
	if _, err := s.GetPrompt(ctx, &mcp.GetPromptParams{Name: "echo__greet", Arguments: map[string]string{"name": "Ada"}}); err == nil {
		t.Fatal("prompt served under default deny")
	}
	res, err := s.ListResources(ctx, &mcp.ListResourcesParams{})
	if err != nil || len(res.Resources) == 0 {
		t.Fatalf("resources: %v", err)
	}
	if _, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: res.Resources[0].URI}); err == nil {
		t.Fatal("resource read under default deny")
	}
	if text, isErr := call(t, s, "echo__echo", map[string]any{"text": "x"}); !isErr || !strings.Contains(text, "Denied") {
		t.Fatalf("tool under default deny: %q", text)
	}
}

// A denial cannot be dodged by reformatting the same arguments.
func TestDenialSurvivesReformattedArguments(t *testing.T) {
	g := startGateway(t)
	readyEcho(t, g)
	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{"server": "echo", "tool": "echo", "action": "ask"}, http.StatusCreated)
	admin := g.issueControl("carol", "admin", "carol")
	s := g.connect(g.agent)
	g.eventually("echo listed", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s), "echo__echo") })
	_, _ = call(t, s, "echo__echo", map[string]any{"text": "prod"})
	id, _ := g.pendingApprovals(admin)[0]["id"].(string)
	g.api(http.MethodPost, "/api/v1/approvals/"+id+"/deny", admin, nil, http.StatusOK)

	// Raw JSON-RPC with extra whitespace: same arguments, different bytes.
	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo__echo","arguments":{ "text" :  "prod" }}}`
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, g.base+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+g.agent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "denied") {
		t.Fatalf("reformatted retry was not treated as the denied call: %s", raw)
	}
	if n := len(g.pendingApprovals(admin)); n != 0 {
		t.Fatalf("reformatted retry created %d new approval requests", n)
	}
}
