package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// R-05 to R-07 (independent review, 2026-10-29): a reply of an upstream is
// checked inside the call, so a reply the gateway cannot relay is audited as an
// error and not as "ok", and what the gateway relays is the gateway's to say.

func TestCheckPrompt(t *testing.T) {
	good := &mcp.PromptMessage{Role: "user", Content: &mcp.TextContent{Text: "hi"}}
	for name, tc := range map[string]struct {
		res  *mcp.GetPromptResult
		want string // part of the error; empty: the reply is fine
	}{
		"a prompt":                      {&mcp.GetPromptResult{Messages: []*mcp.PromptMessage{good}}, ""},
		"a prompt without messages":     {&mcp.GetPromptResult{Messages: []*mcp.PromptMessage{}}, ""},
		"no result":                     {nil, "no result"},
		"messages absent or null":       {&mcp.GetPromptResult{}, "messages"},
		"a null message":                {&mcp.GetPromptResult{Messages: []*mcp.PromptMessage{good, nil}}, "messages[1]"},
		"a message without content":     {&mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user"}}}, "messages[0].content"},
		"a null message among the real": {&mcp.GetPromptResult{Messages: []*mcp.PromptMessage{nil, good}}, "messages[0]"},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkPrompt(tc.res)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("a good reply was refused: %v", err)
				}
				return
			}
			if !errors.Is(err, errMalformedReply) || !strings.Contains(err.Error(), "malformed") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a malformed-reply error naming %q", err, tc.want)
			}
		})
	}
}

// The SDK client turns a null, an empty object and a missing "content" of a
// tools/call reply into the same empty result, so they cannot be told apart
// from {"content": []}, and what the gateway relays is valid either way. The
// test pins that, so a later SDK that stops doing it is noticed.
func TestCheckToolResult(t *testing.T) {
	if err := checkTool(&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "x"}}}); err != nil {
		t.Fatalf("a result was refused: %v", err)
	}
	if err := checkTool(&mcp.CallToolResult{Content: []mcp.Content{}}); err != nil {
		t.Fatalf("an empty result is valid: %v", err)
	}
	if err := checkTool(nil); !errors.Is(err, errMalformedReply) {
		t.Errorf("no result: err = %v, want a malformed-reply error", err)
	}
	if err := checkTool(&mcp.CallToolResult{Content: []mcp.Content{nil}}); !errors.Is(err, errMalformedReply) || !strings.Contains(err.Error(), "content[0]") {
		t.Errorf("a null content block: err = %v", err)
	}
	var asks mcp.CallToolResult
	if err := json.Unmarshal([]byte(`{"resultType":"input_required","content":[]}`), &asks); err != nil {
		t.Fatal(err)
	}
	if err := checkTool(&asks); !errors.Is(err, errNeedsInput) {
		t.Errorf("a result that asks for input: err = %v, want errNeedsInput", err)
	}
	var wire mcp.GetPromptResult
	if err := json.Unmarshal([]byte(`{"resultType":"input_required","messages":[]}`), &wire); err != nil {
		t.Fatal(err)
	}
	if err := checkPrompt(&wire); !errors.Is(err, errNeedsInput) {
		t.Errorf("a prompt that asks for input: err = %v, want errNeedsInput", err)
	}
}

func TestRewriteReadRequiresContents(t *testing.T) {
	if err := rewriteRead("echo", &mcp.ReadResourceResult{}); !errors.Is(err, errMalformedRead) || !strings.Contains(err.Error(), "contents") {
		t.Errorf("contents absent or null: err = %v, want a malformed-reply error naming contents", err)
	}
	if err := rewriteRead("echo", &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{}}); err != nil {
		t.Errorf("an empty list of contents is valid: %v", err)
	}
	if !errors.Is(fmt.Errorf("%w: x", errMalformedRead), errMalformedReply) {
		t.Error("a malformed read is a malformed reply")
	}
}

func TestReservedMetaKey(t *testing.T) {
	for key, want := range map[string]bool{
		"io.modelcontextprotocol/serverInfo":   true,
		"io.modelcontextprotocol/related-task": true,
		"dev.mcp/anything":                     true,
		"org.modelcontextprotocol.api/x":       true,
		"com.example.mcp/x":                    true,
		"com.example/keep":                     false,
		"example.com/mcp":                      false, // only the prefix counts
		"progressToken":                        false,
		"":                                     false,
	} {
		if got := reservedMetaKey(key); got != want {
			t.Errorf("reservedMetaKey(%q) = %v, want %v", key, got, want)
		}
	}
}

// R-06: requestState and the reserved _meta keys of a result are the
// protocol's to say. An upstream's requestState reached the client on a result
// labelled complete, and an upstream's serverInfo was relayed in place of the
// gateway's, because the SDK adds its own only when the key is missing.
func TestCleanForRelay(t *testing.T) {
	meta := func() mcp.Meta {
		return mcp.Meta{
			"io.modelcontextprotocol/serverInfo":   map[string]any{"name": "evil"},
			"io.modelcontextprotocol/related-task": map[string]any{"taskId": "t1"},
			"dev.mcp/anything":                     1,
			"com.example/keep":                     "yes",
			"plain":                                true,
		}
	}
	state := func(res mcp.Result) string {
		switch r := res.(type) {
		case *mcp.CallToolResult:
			return r.RequestState
		case *mcp.GetPromptResult:
			return r.RequestState
		case *mcp.ReadResourceResult:
			return r.RequestState
		}
		return "?"
	}
	for name, res := range map[string]mcp.Result{
		"tools/call":     &mcp.CallToolResult{Meta: meta(), RequestState: "S3CRET-STATE"},
		"prompts/get":    &mcp.GetPromptResult{Meta: meta(), RequestState: "S3CRET-STATE"},
		"resources/read": &mcp.ReadResourceResult{Meta: meta(), RequestState: "S3CRET-STATE"},
	} {
		t.Run(name, func(t *testing.T) {
			cleanForRelay(res)
			if s := state(res); s != "" {
				t.Errorf("requestState %q was kept", s)
			}
			got := res.GetMeta()
			if len(got) != 2 || got["com.example/keep"] != "yes" || got["plain"] != true {
				t.Errorf("_meta = %v, want only the two keys that are not reserved", got)
			}
		})
	}
	t.Run("no _meta stays none", func(t *testing.T) {
		res := &mcp.CallToolResult{}
		cleanForRelay(res)
		if res.GetMeta() != nil {
			t.Errorf("_meta = %v", res.GetMeta())
		}
	})
	t.Run("only reserved keys leave no empty map", func(t *testing.T) {
		res := &mcp.CallToolResult{Meta: mcp.Meta{"io.modelcontextprotocol/serverInfo": "x"}}
		cleanForRelay(res)
		if len(res.GetMeta()) != 0 {
			t.Errorf("_meta = %v", res.GetMeta())
		}
	})
}

// R-07: a plain error leaving the gateway reaches the client as JSON-RPC code
// 0, which is not a valid code.
func TestRPCErrorCodes(t *testing.T) {
	g := &Gateway{}
	for name, tc := range map[string]struct {
		err  error
		code int64
		msg  string
	}{
		"a plain error":  {errors.New("boom"), -32603, "boom"},
		"a refusal":      {&refusal{msg: "Denied by policy"}, -32600, "Denied by policy"},
		"bad parameters": {invalidParams("prompt %q is not available", "x"), -32602, `prompt "x" is not available`},
		// SEP-2164 moved "resource not found" to -32602; the SDK keeps the value.
		"a resource that is missing": {resourceNotFound("resource %q is not available", "u"), int64(mcp.CodeResourceNotFound), `resource "u" is not available`},
		"the upstream's own error":   {fmt.Errorf("wrapped: %w", &jsonrpc.Error{Code: -32042, Message: "its words"}), -32042, "its words"},
		"a malformed reply":          {fmt.Errorf("%w: x", errMalformedReply), -32603, "malformed"},
	} {
		t.Run(name, func(t *testing.T) {
			got := g.rpcError(tc.err)
			var rpc *jsonrpc.Error
			if !errors.As(got, &rpc) || rpc.Code != tc.code || !strings.Contains(rpc.Message, tc.msg) {
				t.Fatalf("got %#v, want code %d with %q", got, tc.code, tc.msg)
			}
		})
	}
	if g.rpcError(nil) != nil {
		t.Error("nil became an error")
	}
}
