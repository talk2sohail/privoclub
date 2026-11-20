//go:build acceptance

package acceptance

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// F10 second slice through the real binary: a scoped grant from an approval,
// a person withdrawing their own request, and the history.
func TestApprovalGrantAndWithdraw(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	boss := ownerOf(t, g, gh)
	addReviewed(t, g, boss, map[string]any{"alias": "shared", "transport": "http", "url": plainMCPServer(t).URL + "/mcp"})
	code, out := boss.app(http.MethodPost, "/v1/policies", map[string]any{"server": "shared", "tool": "*", "subject": "*", "action": "ask", "priority": 3}, nil)
	if code != http.StatusCreated {
		t.Fatalf("rule: %d %v", code, out)
	}
	ada, adaToken := join(t, g, gh, boss, "ada", 2)
	client := g.connect(adaToken)

	pendingID := func(text string) string {
		t.Helper()
		_, out := boss.app(http.MethodGet, "/v1/approvals?status=pending", nil, nil)
		for _, a := range list(out, "approvals") {
			if strings.Contains(stringOf(a["arguments"]), text) {
				id, _ := a["id"].(string)
				return id
			}
		}
		t.Fatalf("no pending approval for %q in %v", text, out)
		return ""
	}

	// 1. The first call asks; the owner approves it and allows the tool for
	// Ada for an hour.
	if text, isErr := call(t, client, "shared__echo", map[string]any{"text": "one"}); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("first call: %q", text)
	}
	code, out = boss.app(http.MethodPost, "/v1/approvals/"+pendingID("one")+"/approve", map[string]any{"grant": "1h"}, nil)
	grant, _ := out["grant"].(map[string]any)
	if code != http.StatusOK || grant["action"] != "allow" || grant["expires_at"] == nil || !strings.HasPrefix(grant["subject"].(string), "user:") {
		t.Fatalf("approve with a grant: %d %v", code, out)
	}
	if text, isErr := call(t, client, "shared__echo", map[string]any{"text": "one"}); isErr || text != "one" {
		t.Fatalf("the approved call: %q %v", text, isErr)
	}
	// 2. Other arguments run without asking while the grant lasts.
	if text, isErr := call(t, client, "shared__echo", map[string]any{"text": "two"}); isErr || text != "two" {
		t.Fatalf("a call under the grant: %q %v", text, isErr)
	}
	_, rules := boss.app(http.MethodGet, "/v1/policies", nil, nil)
	var named bool
	for _, r := range list(rules, "policies") {
		named = named || r["subject_person"] == "ada"
	}
	if !named {
		t.Fatalf("rules do not name ada: %v", rules)
	}

	// 3. Without the grant, Ada's next call asks again, and she withdraws it.
	if code, _ := boss.app(http.MethodDelete, "/v1/policies/"+grant["id"].(string), nil, nil); code != http.StatusOK {
		t.Fatalf("delete the grant: %d", code)
	}
	if text, isErr := call(t, client, "shared__echo", map[string]any{"text": "three"}); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("after the grant was removed: %q", text)
	}
	_, mine := ada.app(http.MethodGet, "/v1/me/approvals", nil, nil)
	waiting := list(mine, "approvals")
	if len(waiting) != 1 {
		t.Fatalf("ada's waiting requests: %v", mine)
	}
	id, _ := waiting[0]["id"].(string)
	if code, out := ada.app(http.MethodPost, "/v1/me/approvals/"+id+"/withdraw", nil, nil); code != http.StatusOK {
		t.Fatalf("withdraw: %d %v", code, out)
	}
	if code, _ := boss.app(http.MethodPost, "/v1/approvals/"+id+"/approve", nil, nil); code != http.StatusConflict {
		t.Fatalf("approving a withdrawn request: %d, want 409", code)
	}
	// The same call asks again, as a new request.
	if text, isErr := call(t, client, "shared__echo", map[string]any{"text": "three"}); !isErr || !strings.Contains(text, "Approval required") || strings.Contains(text, id) {
		t.Fatalf("retry after withdrawing: %q", text)
	}

	// 4. History: the approved request and the withdrawn one, without arguments.
	g.eventually("history lists both", 5*time.Second, func() bool {
		_, out := boss.app(http.MethodGet, "/v1/approvals?status=history", nil, nil)
		seen := map[string]bool{}
		for _, a := range list(out, "approvals") {
			if a["arguments"] != nil {
				t.Fatalf("history shows arguments: %v", a)
			}
			seen[a["status"].(string)] = true
		}
		// The grant let the retried call through, so the approval itself was
		// not needed and stays approved, unused, until it expires.
		return (seen["approved"] || seen["consumed"]) && seen["withdrawn"]
	})
}

func stringOf(v any) string {
	if m, ok := v.(map[string]any); ok {
		var b strings.Builder
		for k, x := range m {
			b.WriteString(k + "=" + stringOf(x) + ";")
		}
		return b.String()
	}
	s, _ := v.(string)
	return s
}
