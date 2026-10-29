//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The owner's manual test plan, as automated tests. Each test drives the real
// binary through the admin API and a real MCP client (the Go SDK's) against the
// echo server, so a regression in any of these flows fails the build and not a
// session in Claude Code.
//
//	Deny, then lift ................ TestPlanDenyRuleCanBeListedAndRemoved
//	Ask, approve, allow_self ....... TestPlanAskApprovalAndAllowSelfToggle
//	Revocation and a new token ..... TestPlanRevocationOnALiveSession
//	Presets and default policy ..... TestPlanPresetsAndDefaultPolicy
//	Audit fields and outcomes ...... TestPlanAuditFieldsAndOutcomes
//	Prompts and resources .......... TestPlanPromptsAndResourcesThroughTheClient
//
// Flows that policy_test.go already covers are not repeated here: deny by role,
// approval bound to arguments and single use, a decision during the hold,
// gateway tokens that cannot administer, default deny over prompts and
// resources, and the read-only preset.

// adminActions returns the actions in the admin audit, newest first.
func adminActions(g *gateway) []string {
	g.t.Helper()
	res := g.api(http.MethodGet, "/api/v1/audit/events?kind=admin&limit=500", g.owner, nil, http.StatusOK)
	raw, _ := res["events"].([]any)
	var out []string
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			if a, _ := m["action"].(string); a != "" {
				out = append(out, a)
			}
		}
	}
	return out
}

// wantAdmin waits for an admin audit action (admin events are batched like calls).
func wantAdmin(g *gateway, action string) {
	g.t.Helper()
	g.eventually("admin audit "+action, 5*time.Second, func() bool { return slices.Contains(adminActions(g), action) })
}

// findCall returns the newest call event that matches, from the call audit.
func findCall(g *gateway, match func(map[string]any) bool) map[string]any {
	g.t.Helper()
	res := g.api(http.MethodGet, "/api/v1/audit/events?limit=200", g.owner, nil, http.StatusOK)
	raw, _ := res["events"].([]any)
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok && match(m) {
			return m
		}
	}
	return nil
}

// wantCall waits for a call event with the given server, item and outcome prefix.
func wantCall(g *gateway, server, item, action, decisionPrefix, outcomePrefix string) map[string]any {
	g.t.Helper()
	var found map[string]any
	g.eventually(fmt.Sprintf("audit row %s/%s %s decision %q outcome %q", server, item, action, decisionPrefix, outcomePrefix), 5*time.Second, func() bool {
		found = findCall(g, func(e map[string]any) bool {
			d, _ := e["decision"].(string)
			o, _ := e["outcome"].(string)
			return e["server"] == server && e["item"] == item && e["action"] == action &&
				strings.HasPrefix(d, decisionPrefix) && strings.HasPrefix(o, outcomePrefix)
		})
		return found != nil
	})
	return found
}

func idOfPolicy(t *testing.T, created map[string]any) string {
	t.Helper()
	pol, _ := created["policy"].(map[string]any)
	id, _ := pol["id"].(string)
	if id == "" {
		t.Fatalf("created policy has no id: %v", created)
	}
	return id
}

// Deny: a rule for one tool refuses the call without telling the model why;
// it is listed, audited, and gone after it is deleted.
func TestPlanDenyRuleCanBeListedAndRemoved(t *testing.T) {
	g := startGateway(t)
	readyEcho(t, g)
	s := g.connect(g.agent)
	g.eventually("echo listed", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s), "echo__echo") })

	for name, body := range map[string]map[string]any{
		"unknown action":        {"server": "echo", "tool": "echo", "action": "block"},
		"bad subject":           {"server": "echo", "tool": "echo", "subject": "bob", "action": "deny"},
		"bad server":            {"server": "not an alias!", "tool": "echo", "action": "deny"},
		"priority out of range": {"server": "echo", "tool": "echo", "action": "deny", "priority": 5000},
	} {
		if code, b := g.raw(http.MethodPost, "/api/v1/policies", g.owner, body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422: %s", name, code, b)
		}
	}
	if list := g.api(http.MethodGet, "/api/v1/policies", g.owner, nil, http.StatusOK); len(list["policies"].([]any)) != 0 {
		t.Fatalf("a refused rule was stored: %v", list["policies"])
	}

	id := idOfPolicy(t, g.api(http.MethodPost, "/api/v1/policies", g.owner,
		map[string]any{"server": "echo", "tool": "echo", "action": "deny", "note": "plan-note"}, http.StatusCreated))
	listed := g.api(http.MethodGet, "/api/v1/policies", g.owner, nil, http.StatusOK)
	if listed["default"] != "allow" || len(listed["policies"].([]any)) != 1 {
		t.Fatalf("policy list = %v", listed)
	}

	text, isErr := call(t, s, "echo__echo", map[string]any{"text": "x"})
	if !isErr || !strings.Contains(text, "Denied") {
		t.Fatalf("denied call: %q (error=%v)", text, isErr)
	}
	if strings.Contains(text, "plan-note") || strings.Contains(text, id) {
		t.Errorf("the refusal tells the model about the rule: %q", text)
	}
	denied := wantCall(g, "echo", "echo", "tools/call", "deny", "denied")
	if denied["actor"] != "owner-agent" || denied["tool"] != "echo" {
		t.Errorf("denial row = %v", denied)
	}
	wantAdmin(g, "policy.create:deny echo echo *")

	if got := g.api(http.MethodDelete, "/api/v1/policies/"+id, g.owner, nil, http.StatusOK); got["deleted"] != id {
		t.Fatalf("delete answered %v", got)
	}
	g.eventually("call allowed after the rule is deleted", 5*time.Second, func() bool {
		text, isErr := call(t, s, "echo__echo", map[string]any{"text": "again"})
		return !isErr && text == "again"
	})
	if code, _ := g.raw(http.MethodDelete, "/api/v1/policies/"+id, g.owner, nil); code != http.StatusNotFound {
		t.Fatalf("deleting a rule twice: %d, want 404", code)
	}
	wantAdmin(g, "policy.delete:"+id)
}

// Ask: the first call is refused with a request id, the owner cannot approve
// their own agent's call until allow_self is switched on, and switching it back
// restores the rule.
func TestPlanAskApprovalAndAllowSelfToggle(t *testing.T) {
	g := startGateway(t)
	readyEcho(t, g)
	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{"server": "echo", "tool": "echo", "action": "ask"}, http.StatusCreated)
	s := g.connect(g.agent)
	g.eventually("echo listed", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s), "echo__echo") })

	firstPending := func(text string) string {
		t.Helper()
		pending := g.pendingApprovals(g.owner)
		if len(pending) != 1 {
			t.Fatalf("pending approvals: %v", pending)
		}
		id, _ := pending[0]["id"].(string)
		if !strings.Contains(text, id) {
			t.Fatalf("caller message %q does not name approval %s", text, id)
		}
		return id
	}

	text, isErr := call(t, s, "echo__echo", map[string]any{"text": "one"})
	if !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("first call: %q", text)
	}
	id1 := firstPending(text)

	if code, _ := g.raw(http.MethodPost, "/api/v1/approvals/"+id1+"/approve", g.owner, nil); code != http.StatusForbidden {
		t.Fatalf("owner approving their own agent's call: %d, want 403", code)
	}
	if code, _ := g.raw(http.MethodPatch, "/api/v1/settings/approvals", g.owner, map[string]any{}); code != http.StatusUnprocessableEntity {
		t.Fatalf("settings change with nothing in it: %d, want 422", code)
	}
	if got := g.api(http.MethodPatch, "/api/v1/settings/approvals", g.owner, map[string]any{"allow_self": true}, http.StatusOK); got["allow_self"] != true {
		t.Fatalf("allow_self answered %v", got)
	}
	g.api(http.MethodPost, "/api/v1/approvals/"+id1+"/approve", g.owner, nil, http.StatusOK)
	if text, isErr := call(t, s, "echo__echo", map[string]any{"text": "one"}); isErr || text != "one" {
		t.Fatalf("retry after approval: %q (error=%v)", text, isErr)
	}

	g.api(http.MethodPatch, "/api/v1/settings/approvals", g.owner, map[string]any{"allow_self": false}, http.StatusOK)
	text, isErr = call(t, s, "echo__echo", map[string]any{"text": "two"})
	if !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("second call: %q", text)
	}
	id2 := firstPending(text)
	if code, _ := g.raw(http.MethodPost, "/api/v1/approvals/"+id2+"/approve", g.owner, nil); code != http.StatusForbidden {
		t.Fatalf("self approval after allow_self=false: %d, want 403", code)
	}
	carol := g.issueControl("carol", "admin", "carol")
	g.api(http.MethodPost, "/api/v1/approvals/"+id2+"/deny", carol, nil, http.StatusOK)
	if code, _ := g.raw(http.MethodPost, "/api/v1/approvals/"+id2+"/approve", carol, nil); code != http.StatusConflict {
		t.Fatalf("deciding twice: %d, want 409", code)
	}
	if text, isErr := call(t, s, "echo__echo", map[string]any{"text": "two"}); !isErr || !strings.Contains(text, "denied") {
		t.Fatalf("call after a denial: %q", text)
	}

	// What an approver can read afterwards: the decision, no arguments.
	one := g.api(http.MethodGet, "/api/v1/approvals/"+id1, g.owner, nil, http.StatusOK)["approval"].(map[string]any)
	if one["status"] != "consumed" || one["arguments"] != nil {
		t.Errorf("used approval = %v, want status consumed and no arguments", one)
	}
	two := g.api(http.MethodGet, "/api/v1/approvals/"+id2, g.owner, nil, http.StatusOK)["approval"].(map[string]any)
	if two["status"] != "denied" || !strings.HasPrefix(fmt.Sprint(two["decided_by"]), "carol") {
		t.Errorf("denied approval = %v", two)
	}
	if code, _ := g.raw(http.MethodGet, "/api/v1/approvals/nope", g.owner, nil); code != http.StatusNotFound {
		t.Errorf("unknown approval: %d, want 404", code)
	}
	if code, _ := g.raw(http.MethodGet, "/api/v1/approvals?status=bogus", g.owner, nil); code != http.StatusUnprocessableEntity {
		t.Errorf("unknown status filter: %d, want 422", code)
	}

	for _, a := range []string{"approvals.allow_self=true", "approvals.allow_self=false", "approval.approve:" + id1, "approval.deny:" + id2} {
		wantAdmin(g, a)
	}
}

// Revocation: a revoked token stops working on a session that is already open,
// within the 5 seconds of ADR 0003, and a new token works at once.
func TestPlanRevocationOnALiveSession(t *testing.T) {
	g := startGateway(t)
	readyEcho(t, g)
	issued := g.api(http.MethodPost, "/api/v1/tokens", g.owner, map[string]any{"name": "temp", "role": "member", "kind": "gateway"}, http.StatusCreated)
	token, _ := issued["token"].(string)
	id, _ := issued["id"].(string)
	s := g.connect(token)
	g.eventually("member sees echo", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s), "echo__echo") })
	if got := callEcho(t, s, "before"); got != "before" {
		t.Fatalf("call before revocation: %q", got)
	}

	revoked := time.Now()
	g.api(http.MethodDelete, "/api/v1/tokens/"+id, g.owner, nil, http.StatusOK)
	g.eventually("calls on the open session fail", 6*time.Second, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "echo__echo", Arguments: map[string]any{"text": "after"}})
		return err != nil
	})
	if took := time.Since(revoked); took > 5*time.Second {
		t.Errorf("revocation took %s, want within 5s (ADR 0003)", took)
	}

	fresh := g.issue("temp2", "member")
	s2 := g.connect(fresh)
	g.eventually("a new token works", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s2), "echo__echo") })
	if got := callEcho(t, s2, "new"); got != "new" {
		t.Fatalf("call with the new token: %q", got)
	}
	if code, _ := g.raw(http.MethodDelete, "/api/v1/tokens/nope", g.owner, nil); code != http.StatusNotFound {
		t.Errorf("revoking an unknown token: %d, want 404", code)
	}
	wantAdmin(g, "token.revoke:"+id)
}

// Presets and defaults: ask-before-writes allows what a server marks read-only
// and asks for the rest; the default applies to anything no rule names.
func TestPlanPresetsAndDefaultPolicy(t *testing.T) {
	g := startGateway(t)
	readyEcho(t, g)
	g.registerEcho("notes", []string{"-mode", "annotated"}, nil)
	g.acceptPins("notes")
	s := g.connect(g.agent)
	g.eventually("both servers listed", 5*time.Second, func() bool {
		names := toolNames(t, s)
		return slices.Contains(names, "echo__echo") && slices.Contains(names, "notes__read_note") && slices.Contains(names, "notes__delete_note")
	})

	if code, _ := g.raw(http.MethodPost, "/api/v1/servers/notes/policy:preset", g.owner, map[string]any{"preset": "bogus"}); code != http.StatusUnprocessableEntity {
		t.Errorf("unknown preset: %d, want 422", code)
	}
	if code, _ := g.raw(http.MethodPost, "/api/v1/servers/nope/policy:preset", g.owner, map[string]any{"preset": "read-only"}); code != http.StatusNotFound {
		t.Errorf("preset for an unknown server: %d, want 404", code)
	}
	res := g.api(http.MethodPost, "/api/v1/servers/notes/policy:preset", g.owner, map[string]any{"preset": "ask-before-writes"}, http.StatusCreated)
	actions := map[string]string{}
	for _, p := range res["policies"].([]any) {
		m := p.(map[string]any)
		actions[m["tool"].(string)], _ = m["action"].(string)
	}
	if len(actions) != 2 || actions["read_note"] != "allow" || actions["delete_note"] != "ask" {
		t.Fatalf("ask-before-writes rules = %v, want read_note allow and delete_note ask", actions)
	}
	if text, isErr := call(t, s, "notes__read_note", nil); isErr || text != "note" {
		t.Fatalf("read_note: %q %v", text, isErr)
	}
	if text, isErr := call(t, s, "notes__delete_note", nil); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("delete_note: %q", text)
	}
	wantAdmin(g, "policy.preset:ask-before-writes")

	// The default covers what no rule names (echo has none).
	for _, c := range []struct {
		def, want string
	}{{"ask", "Approval required"}, {"deny", "Denied"}} {
		g.api(http.MethodPatch, "/api/v1/settings/policy", g.owner, map[string]any{"default": c.def}, http.StatusOK)
		if got := g.api(http.MethodGet, "/api/v1/policies", g.owner, nil, http.StatusOK)["default"]; got != c.def {
			t.Fatalf("GET /policies default = %v, want %s", got, c.def)
		}
		if text, isErr := call(t, s, "echo__echo", map[string]any{"text": c.def}); !isErr || !strings.Contains(text, c.want) {
			t.Fatalf("default %s: %q", c.def, text)
		}
		wantAdmin(g, "policy.default="+c.def)
	}
	if code, _ := g.raw(http.MethodPatch, "/api/v1/settings/policy", g.owner, map[string]any{"default": "maybe"}); code != http.StatusUnprocessableEntity {
		t.Errorf("invalid default: %d, want 422", code)
	}
	g.api(http.MethodPatch, "/api/v1/settings/policy", g.owner, map[string]any{"default": "allow"}, http.StatusOK)
	if text, isErr := call(t, s, "echo__echo", map[string]any{"text": "open"}); isErr || text != "open" {
		t.Fatalf("default allow: %q %v", text, isErr)
	}
	// Rules still beat the default: delete_note stays a request under allow.
	if text, isErr := call(t, s, "notes__delete_note", nil); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("delete_note under default allow: %q", text)
	}
}

// Audit: every outcome the log can show, the fields of a row, the limit and
// kind parameters, the admin view, and who may read what.
func TestPlanAuditFieldsAndOutcomes(t *testing.T) {
	g := startGateway(t, "PRIVO_APPROVAL_HOLD=300ms")
	readyEcho(t, g)
	g.registerEcho("mix", []string{"-mode", "outcomes"}, nil)
	g.acceptPins("mix")
	s := g.connect(g.agent)
	g.eventually("servers listed", 5*time.Second, func() bool {
		names := toolNames(t, s)
		return slices.Contains(names, "echo__echo") && slices.Contains(names, "mix__fine")
	})
	g.api(http.MethodPatch, "/api/v1/settings/audit", g.owner, map[string]any{"record_arguments": true}, http.StatusOK)

	if text, isErr := call(t, s, "mix__fine", nil); isErr || text != "fine" {
		t.Fatalf("fine: %q %v", text, isErr)
	}
	if text, isErr := call(t, s, "mix__fail", nil); !isErr || text != "failed on purpose" {
		t.Fatalf("fail: %q %v", text, isErr)
	}
	if text, isErr := call(t, s, "mix__boom", nil); !isErr {
		t.Fatalf("boom: %q %v, want an error result", text, isErr)
	}
	if text, isErr := call(t, s, "echo__echo", map[string]any{"text": "hello"}); isErr || text != "hello" {
		t.Fatalf("echo: %q %v", text, isErr)
	}

	ok := wantCall(g, "mix", "fine", "tools/call", "allow", "ok")
	for _, field := range []string{"id", "time", "actor", "actor_id", "server", "item", "action", "decision", "outcome", "latency_ms"} {
		if _, present := ok[field]; !present {
			t.Errorf("a completed call row has no %q: %v", field, ok)
		}
	}
	if ok["actor"] != "owner-agent" || ok["tool"] != "fine" {
		t.Errorf("row = %v", ok)
	}
	if _, err := time.Parse(time.RFC3339Nano, fmt.Sprint(ok["time"])); err != nil {
		t.Errorf("time %v is not RFC 3339: %v", ok["time"], err)
	}
	wantCall(g, "mix", "fail", "tools/call", "allow", "tool_error")
	wantCall(g, "mix", "boom", "tools/call", "allow", "error")
	hello := wantCall(g, "echo", "echo", "tools/call", "allow", "ok")
	if args, _ := hello["arguments"].(map[string]any); args["text"] != "hello" {
		t.Errorf("arguments were switched on but the row has %v", hello["arguments"])
	}

	// A deny rule, then an ask rule that a person turns down.
	denyID := idOfPolicy(t, g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{"server": "echo", "tool": "echo", "action": "deny"}, http.StatusCreated))
	if text, isErr := call(t, s, "echo__echo", map[string]any{"text": "d"}); !isErr || !strings.Contains(text, "Denied") {
		t.Fatalf("deny: %q", text)
	}
	wantCall(g, "echo", "echo", "tools/call", "deny", "denied:")
	g.api(http.MethodDelete, "/api/v1/policies/"+denyID, g.owner, nil, http.StatusOK)

	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{"server": "echo", "tool": "echo", "action": "ask"}, http.StatusCreated)
	if text, isErr := call(t, s, "echo__echo", map[string]any{"text": "a"}); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("ask: %q", text)
	}
	wantCall(g, "echo", "echo", "tools/call", "ask", "pending_approval")
	carol := g.issueControl("carol", "admin", "carol")
	pending := g.pendingApprovals(carol)
	if len(pending) != 1 {
		t.Fatalf("pending approvals: %v", pending)
	}
	g.api(http.MethodPost, "/api/v1/approvals/"+pending[0]["id"].(string)+"/deny", carol, nil, http.StatusOK)
	if text, isErr := call(t, s, "echo__echo", map[string]any{"text": "a"}); !isErr || !strings.Contains(text, "denied") {
		t.Fatalf("retry after denial: %q", text)
	}
	wantCall(g, "echo", "echo", "tools/call", "ask", "denied_by_approver")

	// With arguments switched off again, new rows carry none.
	g.api(http.MethodPatch, "/api/v1/settings/audit", g.owner, map[string]any{"record_arguments": false}, http.StatusOK)
	g.api(http.MethodDelete, "/api/v1/policies/"+firstPolicyID(t, g), g.owner, nil, http.StatusOK)
	if text, isErr := call(t, s, "echo__echo", map[string]any{"text": "quiet"}); isErr || text != "quiet" {
		t.Fatalf("echo with the rule gone: %q %v", text, isErr)
	}
	g.eventually("the quiet call has no arguments", 5*time.Second, func() bool {
		e := findCall(g, func(e map[string]any) bool {
			return e["server"] == "echo" && e["decision"] == "allow" && e["outcome"] == "ok" && e["arguments"] == nil
		})
		return e != nil
	})

	// Parameters.
	two := g.api(http.MethodGet, "/api/v1/audit/events?limit=2", g.owner, nil, http.StatusOK)["events"].([]any)
	if len(two) != 2 {
		t.Errorf("limit=2 returned %d events", len(two))
	}
	if code, _ := g.raw(http.MethodGet, "/api/v1/audit/events?kind=bogus", g.owner, nil); code != http.StatusBadRequest {
		t.Errorf("unknown kind: %d, want 400", code)
	}
	g.api(http.MethodGet, "/api/v1/audit/events?kind=calls&limit=500", g.owner, nil, http.StatusOK)
	admin := g.api(http.MethodGet, "/api/v1/audit/events?kind=admin&limit=5", g.owner, nil, http.StatusOK)["events"].([]any)
	if len(admin) == 0 {
		t.Fatal("no admin events")
	}
	for _, field := range []string{"id", "time", "actor", "actor_id", "action", "decision", "outcome"} {
		if _, present := admin[0].(map[string]any)[field]; !present {
			t.Errorf("an admin row has no %q: %v", field, admin[0])
		}
	}

	// Who may read the log: an auditor reads, cannot change what is recorded.
	auditor := g.issueControl("audrey", "auditor", "audrey")
	g.api(http.MethodGet, "/api/v1/audit/events", auditor, nil, http.StatusOK)
	if code, _ := g.raw(http.MethodPatch, "/api/v1/settings/audit", auditor, map[string]any{"record_arguments": true}); code != http.StatusForbidden {
		t.Errorf("auditor changing audit settings: %d, want 403", code)
	}
}

// firstPolicyID returns the id of the only rule left.
func firstPolicyID(t *testing.T, g *gateway) string {
	t.Helper()
	list := g.api(http.MethodGet, "/api/v1/policies", g.owner, nil, http.StatusOK)["policies"].([]any)
	if len(list) != 1 {
		t.Fatalf("policies = %v, want exactly one", list)
	}
	id, _ := list[0].(map[string]any)["id"].(string)
	return id
}

// Prompts and resources through a real client: served, audited with their own
// action and name, refused by a rule on their name, and "ask" fails closed
// because approvals cover tool calls only.
func TestPlanPromptsAndResourcesThroughTheClient(t *testing.T) {
	g := startGateway(t)
	readyEcho(t, g)
	s := g.connect(g.agent)
	ctx := context.Background()
	g.eventually("echo listed", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s), "echo__echo") })

	getGreeting := func() (string, error) {
		res, err := s.GetPrompt(ctx, &mcp.GetPromptParams{Name: "echo__greet", Arguments: map[string]string{"name": "Ada"}})
		if err != nil {
			return "", err
		}
		text, _ := res.Messages[0].Content.(*mcp.TextContent)
		return text.Text, nil
	}
	resources, err := s.ListResources(ctx, &mcp.ListResourcesParams{})
	if err != nil || len(resources.Resources) != 1 {
		t.Fatalf("ListResources = %+v, %v", resources, err)
	}
	uri := resources.Resources[0].URI
	readNote := func() (string, error) {
		res, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			return "", err
		}
		return res.Contents[0].Text, nil
	}

	if got, err := getGreeting(); err != nil || got != "Hello Ada" {
		t.Fatalf("prompt = %q, %v", got, err)
	}
	if got, err := readNote(); err != nil || got != "note-body" {
		t.Fatalf("resource = %q, %v", got, err)
	}
	prompt := wantCall(g, "echo", "greet", "prompts/get", "allow", "ok")
	if prompt["tool"] != nil {
		t.Errorf("a prompt row calls itself a tool: %v", prompt)
	}
	wantCall(g, "echo", "echo://note", "resources/read", "allow", "ok")

	// A rule on the name refuses that one prompt or resource and nothing else.
	// A resource is named by its upstream URI.
	greet := idOfPolicy(t, g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{"server": "echo", "tool": "greet", "action": "deny"}, http.StatusCreated))
	if _, err := getGreeting(); err == nil || !strings.Contains(err.Error(), "Denied") {
		t.Fatalf("prompt under a deny rule: %v", err)
	}
	if got, err := readNote(); err != nil || got != "note-body" {
		t.Fatalf("a rule on the prompt reached the resource: %q, %v", got, err)
	}
	wantCall(g, "echo", "greet", "prompts/get", "deny", "denied")
	g.api(http.MethodDelete, "/api/v1/policies/"+greet, g.owner, nil, http.StatusOK)
	if got, err := getGreeting(); err != nil || got != "Hello Ada" {
		t.Fatalf("prompt after the rule is deleted = %q, %v", got, err)
	}

	note := idOfPolicy(t, g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{"server": "echo", "tool": "echo://note", "action": "deny"}, http.StatusCreated))
	if _, err := readNote(); err == nil || !strings.Contains(err.Error(), "Denied") {
		t.Fatalf("resource under a deny rule: %v", err)
	}
	wantCall(g, "echo", "echo://note", "resources/read", "deny", "denied")
	g.api(http.MethodDelete, "/api/v1/policies/"+note, g.owner, nil, http.StatusOK)

	// Ask has no approval flow for these: it refuses, and says so in the audit.
	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{"server": "echo", "tool": "*", "action": "ask"}, http.StatusCreated)
	if _, err := getGreeting(); err == nil {
		t.Error("prompt served under an ask rule")
	}
	if _, err := readNote(); err == nil {
		t.Error("resource served under an ask rule")
	}
	wantCall(g, "echo", "greet", "prompts/get", "ask", "denied:approval not supported for prompts/get")
	wantCall(g, "echo", "echo://note", "resources/read", "ask", "denied:approval not supported for resources/read")
	if p := g.pendingApprovals(g.owner); len(p) != 0 {
		t.Errorf("a prompt or resource created approvals: %v", p)
	}
}
