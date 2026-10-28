//go:build acceptance

package acceptance

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Found with Claude Code on 2026-10-29, calling DeepWiki through the gateway:
// from 2026-07-28 every result must say "resultType": "complete" (or
// "input_required"), and Claude Code dropped tools/call results without it.
// The Go SDK only sets the field in its own tool, prompt and resource handlers,
// and the gateway answers those three methods itself, so a result carried
// whatever the upstream sent: nothing, from a server that predates 2026-07-28,
// and nothing from the gateway's own refusals. The Go client does not check the
// field and the older tests speak the legacy protocol, so only a raw-JSON test
// at 2026-07-28 against both kinds of upstream sees it. Legacy clients get no
// field, as the SDK does it, because they predate it.
func TestEveryResultCarriesResultTypeOnNewProtocol(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("echo", nil, nil)                        // speaks 2026-07-28 itself
	g.registerEcho("old", []string{"-mode", "legacy"}, nil) // like DeepWiki and most servers today
	g.acceptPins("echo")
	g.acceptPins("old")
	g.eventually("both servers listed", 5*time.Second, func() bool {
		tools, _ := g.rpcAt(newProtocol, g.agent, "tools/list", map[string]any{})["tools"].([]any)
		return len(tools) == 2
	})
	uris := map[string]string{}
	listed, _ := g.rpcAt(newProtocol, g.agent, "resources/list", map[string]any{})["resources"].([]any)
	for _, r := range listed {
		uri, _ := r.(map[string]any)["uri"].(string)
		for _, alias := range []string{"echo", "old"} {
			if strings.HasPrefix(uri, "privo://"+alias+"/") {
				uris[alias] = uri
			}
		}
	}
	if len(uris) != 2 {
		t.Fatalf("resources %v", listed)
	}

	type probe struct {
		name, method string
		params       map[string]any
		check        func(t *testing.T, result map[string]any)
	}
	probes := []probe{
		{name: "tools/list", method: "tools/list", params: map[string]any{}},
		{name: "prompts/list", method: "prompts/list", params: map[string]any{}},
		{name: "resources/list", method: "resources/list", params: map[string]any{}},
		{name: "resources/templates/list", method: "resources/templates/list", params: map[string]any{}},
		{name: "tools/call of an unknown tool", method: "tools/call", params: map[string]any{"name": "echo__nope"}, check: func(t *testing.T, r map[string]any) {
			if r["isError"] != true {
				t.Errorf("unknown tool: isError = %v, want true", r["isError"])
			}
		}},
	}
	for _, alias := range []string{"echo", "old"} {
		probes = append(probes,
			probe{name: alias + " tools/call", method: "tools/call", params: map[string]any{"name": alias + "__echo", "arguments": map[string]any{"text": "hi"}}, check: func(t *testing.T, r map[string]any) {
				if text := firstText(r["content"]); text != "hi" || r["isError"] == true {
					t.Errorf("tools/call content %v, isError %v", r["content"], r["isError"])
				}
			}},
			probe{name: alias + " prompts/get", method: "prompts/get", params: map[string]any{"name": alias + "__greet", "arguments": map[string]any{"name": "Ada"}}, check: func(t *testing.T, r map[string]any) {
				msgs, _ := r["messages"].([]any)
				if len(msgs) == 0 {
					t.Fatalf("prompts/get: no messages in %v", r)
				}
				msg, _ := msgs[0].(map[string]any)
				if text := firstText([]any{msg["content"]}); text != "Hello Ada" {
					t.Errorf("prompts/get text %q", text)
				}
			}},
			probe{name: alias + " resources/read", method: "resources/read", params: map[string]any{"uri": uris[alias]}, check: func(t *testing.T, r map[string]any) {
				// The cache hints from the previous fix must survive.
				if r["cacheScope"] != "private" || r["ttlMs"] != float64(0) {
					t.Errorf("resources/read cache hints: scope %v, ttlMs %v", r["cacheScope"], r["ttlMs"])
				}
				contents, _ := r["contents"].([]any)
				if len(contents) == 0 {
					t.Fatalf("resources/read: no contents in %v", r)
				}
				if got, _ := contents[0].(map[string]any)["uri"].(string); got != uris[alias] {
					t.Errorf("resources/read uri %q, want %q", got, uris[alias])
				}
			}},
		)
	}

	for _, version := range []string{newProtocol, legacyProtocol} {
		for _, p := range probes {
			t.Run(version+" "+p.name, func(t *testing.T) {
				result := g.rpcAt(version, g.agent, p.method, p.params)
				got, present := result["resultType"]
				switch {
				case version == newProtocol && got != "complete":
					t.Errorf("resultType = %#v, want \"complete\"; result: %v", got, result)
				case version == legacyProtocol && present:
					t.Errorf("legacy client got resultType %#v; the SDK leaves it out before 2026-07-28", got)
				}
				if p.check != nil {
					p.check(t, result)
				}
			})
		}
	}

	// A call the policy refuses is an ordinary result as well, and the model
	// reads it, so it needs the field like any other.
	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{"server": "old", "tool": "echo", "action": "deny"}, http.StatusCreated)
	t.Run(newProtocol+" tools/call refused by policy", func(t *testing.T) {
		r := g.rpcAt(newProtocol, g.agent, "tools/call", map[string]any{"name": "old__echo", "arguments": map[string]any{"text": "hi"}})
		if r["isError"] != true || !strings.Contains(firstText(r["content"]), "Denied") {
			t.Errorf("refusal: isError %v, content %v", r["isError"], r["content"])
		}
		if r["resultType"] != "complete" {
			t.Errorf("resultType = %#v, want \"complete\"", r["resultType"])
		}
	})
}

// firstText returns the text of the first content block of a raw result.
func firstText(content any) string {
	blocks, _ := content.([]any)
	if len(blocks) == 0 {
		return ""
	}
	block, _ := blocks[0].(map[string]any)
	text, _ := block["text"].(string)
	return text
}
