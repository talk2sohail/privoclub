//go:build acceptance

package acceptance

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// ADR 0021 through the real binary: a person holds several roles. A member
// who also approves uses the gateway and decides other people's calls in the
// browser, never their own and never with their AI client's token.
func TestMemberWhoApproves(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	boss := ownerOf(t, g, gh)
	addReviewed(t, g, boss, map[string]any{"alias": "shared", "transport": "http", "url": plainMCPServer(t).URL + "/mcp"})
	for _, rule := range []map[string]any{
		{"server": "shared", "tool": "*", "subject": "*", "action": "ask"},
		{"server": "shared", "tool": "*", "subject": "role:auditor", "action": "deny"},
	} {
		if code, out := boss.app(http.MethodPost, "/v1/policies", rule, nil); code != http.StatusCreated {
			t.Fatalf("rule %v: %d %v", rule, code, out)
		}
	}
	dan, danToken := join(t, g, gh, boss, "dan", 2)
	_, adaToken := join(t, g, gh, boss, "ada", 3)
	setRoles := func(roles ...string) {
		t.Helper()
		if code, out := boss.app(http.MethodPatch, "/v1/members/dan", map[string]any{"roles": roles}, nil); code != http.StatusOK {
			t.Fatalf("dan's roles %q: %d %v", roles, code, out)
		}
	}
	setRoles("member", "approver")
	danClient, adaClient := g.connect(danToken), g.connect(adaToken)

	// Dan's AI client calls; the call waits for a person.
	if text, isErr := call(t, danClient, "shared__echo", map[string]any{"text": "dan-call"}); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("dan's call: %q", text)
	}
	// His gateway token cannot reach the admin API, so it cannot approve.
	if code, _ := g.raw(http.MethodGet, "/api/v1/approvals?status=pending", danToken, nil); code != http.StatusUnauthorized {
		t.Fatalf("dan's gateway token on the admin API: %d, want 401", code)
	}
	// In the browser he sees his own request marked, and cannot decide it.
	own := dan.pendingID("dan-call")
	_, out := dan.app(http.MethodGet, "/v1/approvals?status=pending", nil, nil)
	if a := list(out, "approvals"); len(a) != 1 || a[0]["own"] != true || a[0]["decide_blocked"] == nil {
		t.Fatalf("dan's own request: %v", out)
	}
	if code, _ := dan.app(http.MethodPost, "/v1/approvals/"+own+"/approve", nil, nil); code != http.StatusForbidden {
		t.Fatalf("dan approves his own call: %d, want 403", code)
	}
	// He decides Ada's.
	if text, isErr := call(t, adaClient, "shared__echo", map[string]any{"text": "ada-call"}); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("ada's call: %q", text)
	}
	var adas string
	_, out = dan.app(http.MethodGet, "/v1/approvals?status=pending", nil, nil)
	for _, a := range list(out, "approvals") {
		if a["requester_person"] == "ada" {
			adas, _ = a["id"].(string)
		}
	}
	if code, out := dan.app(http.MethodPost, "/v1/approvals/"+adas+"/approve", nil, nil); code != http.StatusOK {
		t.Fatalf("dan approves ada's call: %d %v", code, out)
	}
	if text, isErr := call(t, adaClient, "shared__echo", map[string]any{"text": "ada-call"}); isErr || text != "ada-call" {
		t.Fatalf("ada's approved call: %q %v", text, isErr)
	}

	// Roles change on the next request. Without approver, Dan decides nothing.
	setRoles("member")
	if code, _ := dan.app(http.MethodGet, "/v1/approvals?status=pending", nil, nil); code != http.StatusForbidden {
		t.Fatalf("dan without approver lists approvals: %d, want 403", code)
	}
	// A rule for a role follows the person's current roles, not the token's.
	setRoles("member", "auditor")
	g.eventually("the auditor rule applies to dan", 5*time.Second, func() bool {
		text, isErr := call(t, danClient, "shared__echo", map[string]any{"text": "again"})
		return isErr && strings.Contains(text, "Denied by your organisation's Privo policy")
	})
	// Only an approver now: his AI client's token reaches nothing.
	setRoles("approver")
	g.eventually("dan's token reaches nothing", 5*time.Second, func() bool { return len(toolList(t, danClient)) == 0 })
	if text, isErr := call(t, danClient, "shared__echo", map[string]any{"text": "x"}); !isErr || !strings.Contains(text, "is not available") {
		t.Fatalf("an approver-only person's token calls: %q", text)
	}
	// And he gets no new one.
	if code, _ := dan.app(http.MethodPost, "/v1/me/tokens", map[string]any{"name": "another"}, nil); code != http.StatusForbidden {
		t.Fatalf("an approver-only person makes a gateway token: %d, want 403", code)
	}

	// Each change is audited with the owner as actor and the sets before and after.
	g.eventually("role changes audited", 5*time.Second, func() bool {
		_, out := boss.app(http.MethodGet, "/v1/audit/events?kind=admin&limit=200", nil, nil)
		var seen []string
		for _, e := range list(out, "events") {
			if a, _ := e["action"].(string); strings.HasPrefix(a, "member.roles:dan:") && e["actor"] == "boss" {
				seen = append(seen, a)
			}
		}
		want := []string{"member.roles:dan:member->approver+member", "member.roles:dan:approver+member->member",
			"member.roles:dan:member->auditor+member", "member.roles:dan:auditor+member->approver"}
		for _, w := range want {
			if !contains(seen, w) {
				return false
			}
		}
		return true
	})
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
