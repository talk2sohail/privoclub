//go:build acceptance

package acceptance

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// F02: a call's arguments are checked against the accepted input schema
// before the decision, so arguments the tool does not take never reach it and
// never become an approval request.
func TestArgumentsCheckedAgainstTheSchema(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	boss := ownerOf(t, g, gh)
	addReviewed(t, g, boss, map[string]any{"alias": "shared", "transport": "http", "url": plainMCPServer(t).URL + "/mcp"})
	_, adaToken := join(t, g, gh, boss, "ada", 2)
	client := g.connect(adaToken)
	if code, out := boss.app(http.MethodPost, "/v1/policies", map[string]any{"server": "shared", "tool": "*", "action": "ask"}, nil); code != http.StatusCreated {
		t.Fatalf("rule: %d %v", code, out)
	}
	pending := func() int {
		_, out := boss.app(http.MethodGet, "/v1/approvals?status=pending", nil, nil)
		return len(list(out, "approvals"))
	}

	// echo takes {"text": string} and nothing else (the Go SDK's schema).
	for _, args := range []map[string]any{{"text": 5}, {"text": "a", "unexpected": "tell-nobody"}, {}} {
		text, isErr := call(t, client, "shared__echo", args)
		if !isErr || !strings.Contains(text, "do not match this tool's input schema") {
			t.Fatalf("%v: %q", args, text)
		}
	}
	if n := pending(); n != 0 {
		t.Fatalf("%d approval requests for arguments the tool does not take", n)
	}
	// Arguments that match go on to the decision, which asks.
	if text, isErr := call(t, client, "shared__echo", map[string]any{"text": "hello"}); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("a matching call: %q", text)
	}
	if n := pending(); n != 1 {
		t.Fatalf("%d approval requests, want 1", n)
	}

	// The audit names the refusals and holds neither the reason nor the values.
	g.eventually("the refusals are in the audit", 6*time.Second, func() bool {
		_, out := boss.app(http.MethodGet, "/v1/audit/events?limit=200&decision=refused:invalid_arguments", nil, nil)
		return len(list(out, "events")) >= 3
	})
	_, out := boss.app(http.MethodGet, "/v1/audit/events?limit=200", nil, nil)
	raw, _ := json.Marshal(out)
	for _, leak := range []string{"tell-nobody", "do not match"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("the audit holds %q", leak)
		}
	}
}
