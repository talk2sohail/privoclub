//go:build acceptance

package acceptance

import (
	"net/url"
	"strings"
	"testing"
)

// R-05 to R-07 (independent review, 2026-10-29). An upstream's reply is
// untrusted, and the audit log has to say what happened to it.

// resourceURIOf returns the public URI of the resource of alias, as listed.
func resourceURIOf(t *testing.T, g *gateway, alias string) string {
	t.Helper()
	listed, _ := g.rpcAt(newProtocol, g.agent, "resources/list", map[string]any{})["resources"].([]any)
	for _, r := range listed {
		if uri, _ := r.(map[string]any)["uri"].(string); strings.HasPrefix(uri, "privo://"+alias+"/") {
			return uri
		}
	}
	t.Fatalf("no resource of %q listed: %v", alias, listed)
	return ""
}

func rpcMessage(rpcErr map[string]any) string {
	m, _ := rpcErr["message"].(string)
	return m
}

// R-05: a prompts/get reply without messages, and a resources/read reply
// without contents, were relayed as null and audited "ok". They are refused,
// with an error the caller can read and the audit log calls "error".
func TestMalformedPromptAndReadRepliesAreRefusedAndAudited(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("echo", nil, nil)
	g.registerEcho("noprompt", []string{"-mode", "hostile", "-null", "messages"}, nil)
	g.registerEcho("noread", []string{"-mode", "hostile", "-null", "contents"}, nil)
	for _, alias := range []string{"echo", "noprompt", "noread"} {
		g.acceptPins(alias)
	}
	readURI := resourceURIOf(t, g, "noread")

	for _, version := range []string{legacyProtocol, newProtocol} {
		t.Run(version+" prompts/get", func(t *testing.T) {
			result, rpcErr := g.rpcTry(version, g.agent, "prompts/get", map[string]any{"name": "noprompt__greet", "arguments": map[string]any{"name": "Ada"}})
			if rpcErr == nil {
				t.Fatalf("a prompt without messages was relayed: %v", result)
			}
			if code, _ := rpcErr["code"].(float64); code != -32603 || !strings.Contains(rpcMessage(rpcErr), "malformed") || !strings.Contains(rpcMessage(rpcErr), "messages") {
				t.Errorf("error = %v, want code -32603 naming the malformed messages", rpcErr)
			}
		})
		t.Run(version+" resources/read", func(t *testing.T) {
			result, rpcErr := g.rpcTry(version, g.agent, "resources/read", map[string]any{"uri": readURI})
			if rpcErr == nil {
				t.Fatalf("a read without contents was relayed: %v", result)
			}
			if code, _ := rpcErr["code"].(float64); code != -32603 || !strings.Contains(rpcMessage(rpcErr), "malformed") || !strings.Contains(rpcMessage(rpcErr), "contents") {
				t.Errorf("error = %v, want code -32603 naming the malformed contents", rpcErr)
			}
		})
	}

	if got := auditOutcome(g, "noprompt", "greet"); got != "error" {
		t.Errorf("audit outcome of the malformed prompt = %q, want \"error\"", got)
	}
	if got := auditOutcome(g, "noread", "echo://note"); got != "error" {
		t.Errorf("audit outcome of the malformed read = %q, want \"error\"", got)
	}
	assertServing(t, g)
	assertNoPanic(t, g)
}

// R-06: requestState and the reserved _meta keys of a result are not the
// upstream's to set. The gateway relays a result as complete, so a state
// handle from the upstream would have been a lie, and an upstream's
// serverInfo took the place of the gateway's.
func TestUpstreamRequestStateAndReservedMetaAreNotRelayed(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("echo", nil, nil)
	g.registerEcho("evil", []string{"-mode", "hostile", "-null", "poison"}, nil)
	g.acceptPins("echo")
	g.acceptPins("evil")
	readURI := resourceURIOf(t, g, "evil")

	probes := map[string]map[string]any{
		"tools/call":     {"name": "evil__echo", "arguments": map[string]any{"text": "hi"}},
		"prompts/get":    {"name": "evil__greet", "arguments": map[string]any{"name": "Ada"}},
		"resources/read": {"uri": readURI},
	}
	for _, version := range []string{legacyProtocol, newProtocol} {
		for method, params := range probes {
			t.Run(version+" "+method, func(t *testing.T) {
				result := g.rpcAt(version, g.agent, method, params)
				if state, present := result["requestState"]; present {
					t.Errorf("requestState %v was relayed", state)
				}
				meta, _ := result["_meta"].(map[string]any)
				if meta["com.example/keep"] != "yes" {
					t.Errorf("_meta = %v, the upstream's own key was dropped", meta)
				}
				if _, present := meta["io.modelcontextprotocol/related-task"]; present {
					t.Errorf("_meta = %v, a reserved key was relayed", meta)
				}
				if info, present := meta["io.modelcontextprotocol/serverInfo"]; present {
					name, _ := info.(map[string]any)["name"].(string)
					if name != "privo" {
						t.Errorf("serverInfo = %v, want the gateway's, not the upstream's", info)
					}
				} else if version == newProtocol {
					t.Errorf("_meta = %v, want the gateway's serverInfo", meta)
				}
			})
		}
	}
	assertServing(t, g)
}

// R-07: an error that leaves the gateway carries a valid JSON-RPC code. The
// SDK sends a plain error as code 0.
func TestGatewayErrorsCarryJSONRPCCodes(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("echo", nil, nil)
	g.acceptPins("echo")

	for _, version := range []string{legacyProtocol, newProtocol} {
		for name, tc := range map[string]struct {
			method string
			params map[string]any
			code   float64
		}{
			"an unknown prompt":          {"prompts/get", map[string]any{"name": "echo__nope"}, -32602},
			"a URI that is not Privo's":  {"resources/read", map[string]any{"uri": "file:///etc/passwd"}, -32602},
			"a resource that is missing": {"resources/read", map[string]any{"uri": "privo://echo/" + url.PathEscape("echo://nope")}, -32602},
		} {
			t.Run(version+" "+name, func(t *testing.T) {
				result, rpcErr := g.rpcTry(version, g.agent, tc.method, tc.params)
				if rpcErr == nil {
					t.Fatalf("answered with a result: %v", result)
				}
				if code, _ := rpcErr["code"].(float64); code != tc.code {
					t.Errorf("code = %v, want %v (error: %v)", rpcErr["code"], tc.code, rpcErr)
				}
			})
		}
	}
}
