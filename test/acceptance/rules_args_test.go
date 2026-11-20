//go:build acceptance

package acceptance

import (
	"net/http"
	"strings"
	"testing"
)

// ADR 0027 through the real binary: a rule on the call's arguments, and a
// grant that does not lift it.
func TestRulesOnArguments(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	boss := ownerOf(t, g, gh)
	addReviewed(t, g, boss, map[string]any{"alias": "shared", "transport": "http", "url": plainMCPServer(t).URL + "/mcp"})
	_, adaToken := join(t, g, gh, boss, "ada", 2)
	client := g.connect(adaToken)

	// Deny echo when text is "secret"; everything else on the server asks.
	for _, rule := range []map[string]any{
		{"server": "shared", "tool": "echo", "action": "deny", "conditions": []map[string]any{{"arg": "text", "op": "equals", "value": "secret"}}},
		{"server": "shared", "tool": "*", "action": "ask"},
	} {
		if code, out := boss.app(http.MethodPost, "/v1/policies", rule, nil); code != http.StatusCreated {
			t.Fatalf("rule: %d %v", code, out)
		}
	}
	if text, isErr := call(t, client, "shared__echo", map[string]any{"text": "secret"}); !isErr || !strings.Contains(text, "Denied") {
		t.Fatalf("the conditioned call: %q", text)
	}
	if text, isErr := call(t, client, "shared__echo", map[string]any{"text": "hello"}); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("another call: %q", text)
	}
	// Approve "hello" and allow echo for ada for an hour.
	_, out := boss.app(http.MethodGet, "/v1/approvals?status=pending", nil, nil)
	pending := list(out, "approvals")
	if len(pending) != 1 {
		t.Fatalf("pending %v", out)
	}
	if code, out := boss.app(http.MethodPost, "/v1/approvals/"+pending[0]["id"].(string)+"/approve", map[string]any{"grant": "1h"}, nil); code != http.StatusOK {
		t.Fatalf("approve with a grant: %d %v", code, out)
	}
	if text, isErr := call(t, client, "shared__echo", map[string]any{"text": "anything"}); isErr || text != "anything" {
		t.Fatalf("under the grant: %q %v", text, isErr)
	}
	// The grant never lifts the deny.
	if text, isErr := call(t, client, "shared__echo", map[string]any{"text": "secret"}); !isErr || !strings.Contains(text, "Denied") {
		t.Fatalf("the denied call under a grant: %q", text)
	}
	// The rules round-trip through YAML.
	_, exp := boss.app(http.MethodGet, "/v1/policies/export", nil, nil)
	y, _ := exp["yaml"].(string)
	if !strings.Contains(y, "value: secret") {
		t.Fatalf("export:\n%s", y)
	}
	if code, out := boss.app(http.MethodPost, "/v1/policies/import", map[string]any{"yaml": y, "mode": "replace"}, nil); code != http.StatusOK {
		t.Fatalf("import: %d %v", code, out)
	}
	if text, isErr := call(t, client, "shared__echo", map[string]any{"text": "secret"}); !isErr || !strings.Contains(text, "Denied") {
		t.Fatalf("after the round trip: %q", text)
	}
}
