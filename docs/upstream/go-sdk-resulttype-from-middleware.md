# Draft issue for modelcontextprotocol/go-sdk: `resultType` missing on results returned from receiving middleware

Status: **draft, not filed.** Written 2026-10-29 after Claude Code rejected Privo's `tools/call` results.
Before filing: search the tracker for an existing report. This draft was written from a session that could not reach the issue tracker, and a web search turned up nothing related, so a duplicate is possible.
Everything between the two rules below is the issue text. The program and its output were run against v1.8.0, the newest release at the time.

---

## Title

`2026-07-28: results returned from receiving middleware for tools/call, prompts/get and resources/read have no "resultType"`

## Body

### Summary

With a client on protocol 2026-07-28, a `tools/call`, `prompts/get` or `resources/read` that is answered from a receiving middleware (instead of a registered tool, prompt or resource) is sent without `resultType`. The same methods answered by the SDK's own handlers carry `"resultType": "complete"`. A middleware result that sets `InputRequests` also goes out without `"resultType": "input_required"`.

The 2026-07-28 specification says the `result` of a response must include `resultType` ([Basic protocol, Result Responses](https://modelcontextprotocol.io/specification/2026-07-28/basic)), so these responses are non-compliant, and a client that validates results rejects them. We hit this with Claude Code, which dropped the response and failed the tool call.

### Version

- `github.com/modelcontextprotocol/go-sdk` v1.8.0 (latest release)
- Go 1.27.3, linux/arm64

### Reproduction

A standalone program with no other dependencies. It registers one tool, prompt and resource the normal way, answers the same three methods from a receiving middleware, and then sends raw 2026-07-28 requests (protocol version in `_meta`, plus the `Mcp-Method` and `Mcp-Name` headers) and prints each result's `resultType`.

```go
// Reproduction: results returned from receiving middleware for tools/call,
// prompts/get and resources/read lack "resultType" on protocol 2026-07-28,
// while the same methods handled by the SDK's own handlers have it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "repro", Version: "0.0.0"}, nil)

	// Registered the normal way: handled by the SDK's own handlers.
	mcp.AddTool(server, &mcp.Tool{Name: "sdk_tool"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hi"}}}, nil, nil
		})
	server.AddPrompt(&mcp.Prompt{Name: "sdk_prompt"},
		func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "hi"}}}}, nil
		})
	server.AddResource(&mcp.Resource{URI: "sdk://res", Name: "res"},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "sdk://res", Text: "hi"}}}, nil
		})

	// The same three methods answered from receiving middleware, which is the
	// supported place to implement a proxy or gateway, or to short-circuit a call.
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/call":
				if p, _ := req.GetParams().(*mcp.CallToolParamsRaw); p != nil && p.Name == "mw_tool" {
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hi"}}}, nil
				}
				if p, _ := req.GetParams().(*mcp.CallToolParamsRaw); p != nil && p.Name == "mw_tool_needs_input" {
					return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{"q": &mcp.ElicitParams{Message: "Which one?"}}}, nil
				}
			case "prompts/get":
				if p, _ := req.GetParams().(*mcp.GetPromptParams); p != nil && p.Name == "mw_prompt" {
					return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "hi"}}}}, nil
				}
			case "resources/read":
				if p, _ := req.GetParams().(*mcp.ReadResourceParams); p != nil && p.URI == "mw://res" {
					return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "mw://res", Text: "hi"}}}, nil
				}
			case "tools/list":
				return &mcp.ListToolsResult{Tools: []*mcp.Tool{}}, nil // for contrast
			}
			return next(ctx, method, req)
		}
	})

	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true}))
	defer ts.Close()

	for _, c := range []struct {
		label, method, nameOrURI string
		params                   map[string]any
	}{
		{"tools/call      handled by the SDK ", "tools/call", "sdk_tool", map[string]any{"name": "sdk_tool"}},
		{"tools/call      from middleware    ", "tools/call", "mw_tool", map[string]any{"name": "mw_tool"}},
		{"tools/call      needs input (mw)   ", "tools/call", "mw_tool_needs_input", map[string]any{"name": "mw_tool_needs_input"}},
		{"prompts/get     handled by the SDK ", "prompts/get", "sdk_prompt", map[string]any{"name": "sdk_prompt"}},
		{"prompts/get     from middleware    ", "prompts/get", "mw_prompt", map[string]any{"name": "mw_prompt"}},
		{"resources/read  handled by the SDK ", "resources/read", "sdk://res", map[string]any{"uri": "sdk://res"}},
		{"resources/read  from middleware    ", "resources/read", "mw://res", map[string]any{"uri": "mw://res"}},
		{"tools/list      from middleware    ", "tools/list", "", map[string]any{}},
	} {
		fmt.Printf("%s resultType = %s\n", c.label, resultType(ts.URL, c.method, c.nameOrURI, c.params))
	}
}

// resultType sends one request as a 2026-07-28 client (protocol version in
// _meta, plus the Mcp-Method and Mcp-Name headers) and returns the result's
// "resultType", or "<missing>".
func resultType(url, method, name string, params map[string]any) string {
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	payload := string(raw)
	if i := strings.Index(payload, "data:"); i >= 0 { // event-stream
		payload = strings.TrimSpace(strings.SplitN(payload[i+5:], "\n", 2)[0])
	}
	var msg struct {
		Result map[string]any `json:"result"`
		Error  any            `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &msg); err != nil || msg.Result == nil {
		return fmt.Sprintf("<no result: %s>", payload)
	}
	if v, ok := msg.Result["resultType"]; ok {
		return fmt.Sprintf("%q", v)
	}
	return "<missing>"
}
```

Run with `go mod init repro && go get github.com/modelcontextprotocol/go-sdk@v1.8.0 && go run .`

Output:

```
tools/call      handled by the SDK  resultType = "complete"
tools/call      from middleware     resultType = <missing>
tools/call      needs input (mw)    resultType = <missing>
prompts/get     handled by the SDK  resultType = "complete"
prompts/get     from middleware     resultType = <missing>
resources/read  handled by the SDK  resultType = "complete"
resources/read  from middleware     resultType = <missing>
tools/list      from middleware     resultType = "complete"
```

### Expected

Every row says `"complete"` (or `"input_required"` for the row that sets `InputRequests`), whichever side produced the result.

### Cause (from reading v1.8.0)

- `Server.callTool`, `Server.getPrompt` and `Server.readResource` call `handleMultiRoundTripResult` (`mcp/server.go` lines 913, 1015 and 1083), which sets `resultType` for clients on 2026-07-28 or later. That happens only inside the SDK's handlers.
- After the method handler returns, `ServerSession.handle` calls `setCompleteResultType(res)` for requests on the new protocol (`mcp/server.go` line 2043). That function (`mcp/protocol.go` line 43) only acts on results that embed `completeResultWithType`. `ListToolsResult` and the other list results do, which is why the last row of the output is correct. `CallToolResult`, `GetPromptResult` and `ReadResourceResult` do not: they keep `resultType` in an unexported field that their custom `MarshalJSON` writes (`mcp/protocol.go` lines 324, 984 and 1608), so the catch-all skips them.
- `setResultType` is unexported, so code outside the package cannot set the field on these types. The only workaround we found is wrapping the result in our own type that re-encodes the SDK's JSON and adds the key.

### Why it matters

Receiving middleware is the supported place to implement a gateway or proxy, or to short-circuit a call. A gateway that serves tools from many upstream servers per tenant cannot register them with the SDK ahead of time, so it answers `tools/call` itself. Such servers get no `resultType` at all, and strict clients reject every call.

A related effect, seen in our gateway but not in the program above: a result obtained from a `ClientSession` and returned from server middleware keeps the `resultType` it was decoded with, because `UnmarshalJSON` stores it in the same unexported field. A client on an older protocol version then receives `"resultType": "complete"` when the upstream spoke the newer one, although the SDK leaves the field out for older clients.

### Possible fixes

1. Make the catch-all in `ServerSession.handle` cover the three `multiRoundTripResponse` types: when the request uses the new protocol and the result has no `resultType`, set `"input_required"` if `InputRequests` is non-nil and `"complete"` otherwise. Then the result no longer depends on which side produced it.
2. Export a way to set the field (for example an exported `ResultType` field or a constructor), so middleware can do it itself.
3. If answering these methods from middleware is meant to be unsupported, say so in the documentation of `AddReceivingMiddleware`, and provide a supported hook for the gateway case, such as a fallback handler for names that are not registered.

Fix 1 looks smallest. We would be glad to send a patch if the maintainers prefer one of these.

---

## Notes for the Privo repository

- Privo works around this in `internal/gateway/resulttype.go` (`typedResult`). The comment there says to drop it when an SDK release sets the field for middleware results; `TestEveryResultCarriesResultTypeOnNewProtocol` will show if the two ever disagree.
- The tracking item is in `docs/todo.md` under F21.
- The reproduction lives only in this file. To run it, copy the program into an empty directory.
