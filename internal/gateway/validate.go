package gateway

import (
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// What an upstream server sends back is untrusted (R-05 to R-07, independent
// review 2026-10-29). A reply is checked inside the call, before the audit log
// says how the call ended, so a reply the gateway cannot relay is recorded as
// an error and not as "ok". What the gateway relays is then cleaned, because
// requestState and the reserved _meta keys belong to the protocol and to the
// gateway, not to a server the gateway talks to.

// errMalformedReply marks a reply that breaks the protocol. The text names the
// field and its position and never repeats what the upstream sent, so it is
// safe to show to the caller.
var errMalformedReply = errors.New("upstream sent a malformed reply")

// errMalformedRead is the same for resources/read.
var errMalformedRead = fmt.Errorf("%w to resources/read", errMalformedReply)

// errNeedsInput marks a result that asks for input. The gateway does not relay
// an upstream server's questions to the client, and labelling such a result
// "complete" would be false.
var errNeedsInput = errors.New("the upstream server asked for input, which the gateway does not relay")

func malformed(method, format string, args ...any) error {
	return fmt.Errorf("%w to %s: %s", errMalformedReply, method, fmt.Sprintf(format, args...))
}

// checkTool refuses a tools/call reply the gateway cannot relay. The SDK
// client already turns a null result, an empty object and a missing "content"
// into an empty result, which is valid.
func checkTool(res *mcp.CallToolResult) error {
	if res == nil {
		return malformed("tools/call", "no result")
	}
	if res.NeedsInput() {
		return errNeedsInput
	}
	for i, c := range res.Content {
		if c == nil {
			return malformed("tools/call", "content[%d] is null", i)
		}
	}
	return nil
}

// checkPrompt refuses a prompts/get reply the gateway cannot relay. Messages
// that are absent or null decode to a nil slice, which the SDK would send on
// as null; an empty list is valid.
func checkPrompt(res *mcp.GetPromptResult) error {
	if res == nil {
		return malformed("prompts/get", "no result")
	}
	if res.NeedsInput() {
		return errNeedsInput
	}
	if res.Messages == nil {
		return malformed("prompts/get", "messages is missing or null")
	}
	for i, m := range res.Messages {
		switch {
		case m == nil:
			return malformed("prompts/get", "messages[%d] is null", i)
		case m.Content == nil:
			return malformed("prompts/get", "messages[%d].content is missing or null", i)
		}
	}
	return nil
}

// checkRead refuses a resources/read reply the gateway cannot relay.
func checkRead(res *mcp.ReadResourceResult) error {
	if res == nil {
		return fmt.Errorf("%w: no result", errMalformedRead)
	}
	if res.NeedsInput() {
		return errNeedsInput
	}
	if res.Contents == nil {
		return fmt.Errorf("%w: contents is missing or null", errMalformedRead)
	}
	for i, c := range res.Contents {
		if c == nil {
			return fmt.Errorf("%w: contents[%d] is null", errMalformedRead, i)
		}
	}
	return nil
}

// reservedMetaKey reports whether key is one of the _meta keys the protocol
// keeps for itself: the part before the slash is a series of dot-separated
// labels, and one of them is "modelcontextprotocol" or "mcp" (io.modelcontextprotocol/serverInfo,
// dev.mcp/x, com.example.mcp/x). A key without a prefix is not reserved.
func reservedMetaKey(key string) bool {
	prefix, _, found := strings.Cut(key, "/")
	if !found {
		return false
	}
	for _, label := range strings.Split(prefix, ".") {
		if strings.EqualFold(label, "modelcontextprotocol") || strings.EqualFold(label, "mcp") {
			return true
		}
	}
	return false
}

// cleanForRelay removes what an upstream's result must not carry to the
// client: its requestState (a handle the gateway never issued, on a result it
// labels complete) and every reserved _meta key. The SDK adds the gateway's
// own serverInfo to _meta only when the key is missing, so one an upstream
// sent would have been relayed in its place. res must not be nil.
func cleanForRelay(res mcp.Result) {
	switch r := res.(type) {
	case *mcp.CallToolResult:
		r.RequestState = ""
	case *mcp.GetPromptResult:
		r.RequestState = ""
	case *mcp.ReadResourceResult:
		r.RequestState = ""
	}
	meta := res.GetMeta()
	if meta == nil {
		return
	}
	kept := make(map[string]any, len(meta))
	for k, v := range meta {
		if !reservedMetaKey(k) {
			kept[k] = v
		}
	}
	if len(kept) == 0 {
		kept = nil
	}
	res.SetMeta(kept)
}

// invalidParams is a JSON-RPC "invalid params" error (-32602).
func invalidParams(format string, args ...any) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

// resourceNotFound is the MCP "resource not found" error (-32002).
func resourceNotFound(format string, args ...any) error {
	return &jsonrpc.Error{Code: mcp.CodeResourceNotFound, Message: fmt.Sprintf(format, args...)}
}

// rpcError gives an error that leaves the gateway as the answer to a request a
// JSON-RPC code. The SDK sends a plain error as code 0, which is not a valid
// code, and clients that look at the code cannot tell the cases apart. An error
// that already has a code, such as an upstream's own answer, keeps it.
func (g *Gateway) rpcError(err error) error {
	if err == nil {
		return nil
	}
	var rpc *jsonrpc.Error
	if errors.As(err, &rpc) {
		return rpc
	}
	var r *refusal
	if errors.As(err, &r) {
		return &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: r.msg}
	}
	return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
}
