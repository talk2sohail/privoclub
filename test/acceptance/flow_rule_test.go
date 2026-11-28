//go:build acceptance

package acceptance

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ADR 0032 / F09 C through the real binary: reading from an untrusted_source
// server escalates subsequent egress_sink calls for that caller to approval,
// leaves other callers isolated, fails closed on sink resource reads, and
// never overrides a deny rule.
func TestCrossServerFlowEscalatesToApproval(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("crm", nil, nil)
	g.acceptPins("crm")
	g.registerEcho("web", nil, nil)
	g.acceptPins("web")

	g.api(http.MethodPut, "/api/v1/servers/crm/flow-tags", g.owner, map[string]any{
		"tags": []map[string]any{{"tool_name": "", "tag": "untrusted_source"}},
	}, http.StatusOK)
	g.api(http.MethodPut, "/api/v1/servers/web/flow-tags", g.owner, map[string]any{
		"tags": []map[string]any{{"tool_name": "", "tag": "egress_sink"}},
	}, http.StatusOK)

	adaTok := g.issue("ada")
	bobTok := g.issue("bob")
	approver := g.issueControl("carol", "approver", "carol")

	ada := g.connect(adaTok)
	bob := g.connect(bobTok)
	g.eventually("both servers listed", 5*time.Second, func() bool {
		names := toolNames(t, ada)
		return slices.Contains(names, "crm__echo") && slices.Contains(names, "web__echo")
	})

	// 1. Before reading an untrusted source, the sink tool runs cleanly.
	if text, isErr := call(t, ada, "web__echo", map[string]any{"text": "clean"}); isErr || text != "clean" {
		t.Fatalf("clean call: %q (error=%v)", text, isErr)
	}

	// 2. Reading from crm taints ada's caller session.
	if text, isErr := call(t, ada, "crm__echo", map[string]any{"text": "customer-record"}); isErr || text != "customer-record" {
		t.Fatalf("source read: %q (error=%v)", text, isErr)
	}

	// 3. Another caller (bob) who has not read an untrusted source is unaffected.
	if text, isErr := call(t, bob, "web__echo", map[string]any{"text": "bob-clean"}); isErr || text != "bob-clean" {
		t.Fatalf("bob call: %q (error=%v)", text, isErr)
	}

	// 4. ada's call to web__echo now escalates to approval with cross-server reason.
	text, isErr := call(t, ada, "web__echo", map[string]any{"text": "exfiltrate"})
	if !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("expected escalation to approval, got %q (error=%v)", text, isErr)
	}

	pending := g.pendingApprovals(approver)
	if len(pending) != 1 {
		t.Fatalf("pending approvals = %v, want 1", pending)
	}
	reason, _ := pending[0]["reason"].(string)
	if !strings.Contains(reason, "cross-server flow: read from crm/echo") {
		t.Fatalf("approval reason = %q, want cross-server flow explanation", reason)
	}

	// 5. Resource read on a sink fails closed as denied when tainted.
	if _, err := ada.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "privo://web/echo://note"}); err == nil {
		t.Fatal("tainted resource read on egress_sink succeeded; want denial")
	}

	// 6. Approving allows ada's exact retry through once.
	id, _ := pending[0]["id"].(string)
	g.api(http.MethodPost, "/api/v1/approvals/"+id+"/approve", approver, nil, http.StatusOK)
	if text, isErr := call(t, ada, "web__echo", map[string]any{"text": "exfiltrate"}); isErr || text != "exfiltrate" {
		t.Fatalf("approved retry: %q (error=%v)", text, isErr)
	}

	// 7. A deny rule still overrides flow escalation.
	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{
		"server": "web", "tool": "echo", "action": "deny",
	}, http.StatusCreated)
	if text, isErr := call(t, ada, "web__echo", map[string]any{"text": "blocked"}); !isErr || !strings.Contains(text, "Denied") {
		t.Fatalf("expected deny to win over flow escalation, got %q (error=%v)", text, isErr)
	}

	// 8. Admin and gateway events are audited.
	g.eventually("flow tags and approval audited", 5*time.Second, func() bool {
		sawApproved := false
		for _, ev := range auditEvents(g) {
			if d, _ := ev["decision"].(string); d == "approved:"+id && ev["outcome"] == "ok" {
				sawApproved = true
			}
		}
		if !sawApproved {
			return false
		}
		code, raw := g.raw(http.MethodGet, "/api/v1/audit/events?kind=admin", g.owner, nil)
		return code == http.StatusOK && strings.Contains(string(raw), "server.flow_tags:crm") && strings.Contains(string(raw), "server.flow_tags:web")
	})
}
