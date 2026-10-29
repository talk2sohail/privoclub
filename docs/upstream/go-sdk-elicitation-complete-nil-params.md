# Draft issue for modelcontextprotocol/go-sdk: a `notifications/elicitation/complete` without `params` crashes the client, and nothing in the SDK recovers a handler panic

Status: **draft, not filed.** Written 2026-10-29; the owner files it later.
Before filing: search the tracker for an existing report (try "elicitation complete nil", "callElicitationCompleteHandler", "recover"). This draft was written from a session that could not reach the issue tracker, so a duplicate is possible.
Everything between the two rules below is the issue text. The program and its output were run against v1.8.0, the newest release at the time.

---

## Title

`Client: notifications/elicitation/complete without "params" panics (nil dereference in callElicitationCompleteHandler) and takes down the whole process`

## Body

### Summary

A server that sends the notification `notifications/elicitation/complete` with no `params` member makes the client panic with a nil pointer dereference. The panic happens in a goroutine the SDK starts for each incoming message, and no `recover()` exists anywhere in `mcp/` or `internal/jsonrpc2/`, so the panic ends the whole process. For a program that connects to servers it does not control (a gateway, a proxy, an IDE host) one misbehaving or compromised server stops every other session in the process.

The specification requires `params` with an `elicitationId` for this notification, so the message is invalid. The report is not that the SDK should accept it. It is that an invalid message from the peer should produce an error (or be dropped), not a crash.

### Version

- `github.com/modelcontextprotocol/go-sdk` v1.8.0 (latest release)
- Go 1.27.3, linux/arm64

### Where

`mcp/client.go:1526`, in `(*Client).callElicitationCompleteHandler`:

```go
func (c *Client) callElicitationCompleteHandler(ctx context.Context, req *ElicitationCompleteNotificationRequest) (Result, error) {
	// Check if there's a pending elicitation waiting for this notification.
	if cs, ok := req.GetSession().(*ClientSession); ok {
		cs.pendingElicitationsMu.Lock()
		if ch, exists := cs.pendingElicitations[req.Params.ElicitationID]; exists {   // <- req.Params is nil
```

`ElicitationCompleteNotificationRequest` is `ClientRequest[*ElicitationCompleteParams]`, and `Params` is a nil pointer when the notification has no `params` (or `"params": null`).

The goroutine that runs it is started in `internal/jsonrpc2/conn.go` `(*Connection).handleAsync` (`go func() { ... c.handler.Handle(ctx, req.Request) ... }()`), with no `defer recover()`.

### Reproduction

A standalone program with no other dependencies. It plays a minimal raw server over two in-memory pipes: it answers the client's `server/discover` with "method not found" (so the client falls back to `initialize`), answers `initialize`, and after `notifications/initialized` sends the hostile notification.

```go
// Reproduction: a server notification "notifications/elicitation/complete"
// without "params" panics the go-sdk client (nil pointer dereference in
// (*Client).callElicitationCompleteHandler), and nothing recovers it.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	// A raw "server": two pipes, speaking newline-delimited JSON-RPC.
	toServerR, toServerW := io.Pipe()
	toClientR, toClientW := io.Pipe()

	go func() {
		sc := bufio.NewScanner(toServerR)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case contains(line, `"method":"server/discover"`):
				// Older server: no discovery, the client falls back to initialize.
				fmt.Fprintf(toClientW, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"method not found"}}`+"\n", idOf(line))
			case contains(line, `"method":"initialize"`):
				id := idOf(line)
				fmt.Fprintf(toClientW, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"raw","version":"0"}}}`+"\n", id)
			case contains(line, `"method":"notifications/initialized"`):
				// The hostile message: a notification with no "params".
				fmt.Fprint(toClientW, `{"jsonrpc":"2.0","method":"notifications/elicitation/complete"}`+"\n")
			}
		}
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "repro", Version: "0"}, nil)
	_, err := client.Connect(context.Background(), &mcp.IOTransport{Reader: toClientR, Writer: toServerW}, nil)
	fmt.Println("connected, err =", err)
	time.Sleep(time.Second)
	fmt.Println("still alive") // not reached
	os.Exit(0)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// idOf returns the raw JSON id of a request line like {"jsonrpc":"2.0","id":1,...}.
func idOf(line string) string {
	const k = `"id":`
	for i := 0; i+len(k) <= len(line); i++ {
		if line[i:i+len(k)] == k {
			j := i + len(k)
			e := j
			for e < len(line) && line[e] != ',' && line[e] != '}' {
				e++
			}
			return line[j:e]
		}
	}
	return "0"
}
```

Output (v1.8.0, trimmed):

```text
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x8 pc=0x29fc98]

goroutine 25 [running]:
github.com/modelcontextprotocol/go-sdk/mcp.(*Client).callElicitationCompleteHandler(...)
	.../go-sdk@v1.8.0/mcp/client.go:1526 +0xa8
github.com/modelcontextprotocol/go-sdk/mcp.clientMethod[...].func1(...)
	.../go-sdk@v1.8.0/mcp/shared.go:386 +0x44
github.com/modelcontextprotocol/go-sdk/mcp.newClientMethodInfo[...].func2(...)
	.../go-sdk@v1.8.0/mcp/shared.go:308 +0x44
github.com/modelcontextprotocol/go-sdk/mcp.defaultReceivingMethodHandler[...](...)
	.../go-sdk@v1.8.0/mcp/shared.go:203 +0xa8
github.com/modelcontextprotocol/go-sdk/mcp.handleReceive[...](...)
	.../go-sdk@v1.8.0/mcp/shared.go:220 +0x1c8
github.com/modelcontextprotocol/go-sdk/mcp.(*ClientSession).handle(...)
	.../go-sdk@v1.8.0/mcp/client.go:1192 +0x68
github.com/modelcontextprotocol/go-sdk/internal/jsonrpc2.HandlerFunc.Handle(...)
	.../go-sdk@v1.8.0/internal/jsonrpc2/jsonrpc2.go:60 +0x38
github.com/modelcontextprotocol/go-sdk/internal/jsonrpc2.(*Connection).handleAsync.func2()
	.../go-sdk@v1.8.0/internal/jsonrpc2/conn.go:686 +0x64
created by ... (*Connection).handleAsync
exit status 2
```

The process exits with status 2. `"params": null` behaves the same.

In a separate harness (a stdio server driving the same client) we also sent `params` absent for `notifications/progress`, `notifications/message`, `notifications/cancelled`, `notifications/resources/updated` and `notifications/roots/list_changed`. None of those crashed, so this looks like a single missing nil check rather than a general problem in the decoder. (Server-to-client requests for sampling, roots and elicitation with odd `params` did not crash either.)

### Why this matters beyond one nil check

- A receiving middleware can work around it, but only if it wraps `next` in its own `recover`, because middleware runs on the same goroutine as the handler (we do exactly that, and it works). Nothing else can catch the panic: not `net/http`, and not the caller, because the goroutine belongs to the SDK.
- Any future handler bug of this kind, in any method, has the same effect: a peer-controlled message ends the process.

### Suggested fixes (either would have prevented this; both are cheap)

1. Nil-check in the handler:

   ```go
   if req.Params != nil { ... lookup and signal ... }
   ```

   and, more generally, reject a notification or request whose required `params` is missing with an invalid-params error before it reaches the handler.

2. In `internal/jsonrpc2/conn.go` `handleAsync`, `defer` a `recover()` around `c.handler.Handle(...)` that turns the panic into an internal-error response (for requests) or a logged drop (for notifications), so a bug in one handler costs one message and not the process.

We can send a pull request for either if that helps.

---

## Notes for the owner (not part of the issue)

- Privo's own defence is `internal/safe.Recover`, installed as the outermost receiving middleware on the upstream client (`internal/upstream` `connect`) and on the client-facing server. Acceptance tests: `TestHostileUpstreamNotification`, `TestMalformedClientNotificationsDoNotCrash`. Keep them when the SDK is fixed; they test our contract, not the SDK's.
- The reproduction was run from a scratch module outside the repository, against the v1.8.0 sources already in the module cache (`GOFLAGS=-mod=mod GOPROXY=off go run .`).
- Found by the independent review of 2026-10-29 (finding R-01, `docs/reviews/2026-10-29-independent-review-f24-cache-hints-result-type.md`). The review did not run the `http` and `sse` transports (argued from reading only); this draft uses an in-memory pipe, which feeds the same client handler.
- When filed, tick the item in `docs/todo.md` and record the issue number there.
