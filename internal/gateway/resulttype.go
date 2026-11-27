package gateway

import (
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resultTypeSince is the first protocol version whose results say what kind of
// result they are. From then on every result of tools/call, prompts/get and
// resources/read must carry "resultType" (SEP-2322): "complete" for a finished
// result. Clients that validate results, such as Claude Code, drop a response
// without it.
const resultTypeSince = "2026-07-28"

// The Go SDK sets the field in its own tool, prompt and resource handlers and
// nowhere else. The gateway answers those three methods itself, from the
// receiving middleware, so a result reaches the client with whatever the
// upstream server happened to send: nothing from a server that predates
// 2026-07-28, which is most of them, and nothing at all from the results the
// gateway builds (a refusal, "not available"). The field is unexported, so
// the gateway cannot set it on the SDK's types; typedResult sets it on the wire.
//
// That is a gap in the SDK: middleware is a supported place to answer a call,
// and the result should not depend on which side answered. Drop typedResult if
// a later SDK sets the field on results that middleware returns.

// wantsResultType reports whether the client behind req speaks 2026-07-28 or
// later. It is the test the SDK applies before it sets the field itself: the
// version from the request's _meta for the new protocol, or the one in the
// Mcp-Protocol-Version header for older clients. When the version is unknown
// the answer is yes, as in the SDK: an extra field is ignored, a missing one
// is not.
func wantsResultType(req mcp.Request) bool {
	if ss, ok := req.GetSession().(*mcp.ServerSession); ok && ss != nil {
		if p := ss.InitializeParams(); p != nil {
			return p.ProtocolVersion >= resultTypeSince
		}
	}
	return true
}

// withResultType prepares the result of tools/call, prompts/get or
// resources/read for the wire. Clients on 2026-07-28 or later get
// "resultType": "complete", older clients get no such field, whatever the
// upstream sent, so a result looks the same whichever server produced it.
func withResultType(req mcp.Request, res mcp.Result, err error) (mcp.Result, error) {
	if err != nil {
		return nil, err
	}
	var empty, needsInput bool
	switch r := res.(type) {
	case *mcp.CallToolResult:
		empty, needsInput = r == nil, r != nil && r.NeedsInput()
	case *mcp.GetPromptResult:
		empty, needsInput = r == nil, r != nil && r.NeedsInput()
	case *mcp.ReadResourceResult:
		empty, needsInput = r == nil, r != nil && r.NeedsInput()
	case *mcp.CompleteResult:
		empty, needsInput = r == nil, r != nil && r.ResultType == "input_required"
	default:
		empty = res == nil
	}
	if empty {
		return nil, errors.New("gateway produced an empty result")
	}
	if needsInput {
		// Labelling this "complete" would be false, and relaying an upstream
		// server's questions to the client is not something the gateway does.
		// The SDK client already refuses them; this keeps it that way.
		return nil, errNeedsInput
	}
	return typedResult{Result: res, complete: wantsResultType(req)}, nil
}

// typedResult marshals the result it wraps with "resultType" set or removed.
// It embeds the interface, so it is a valid mcp.Result and the SDK's own
// post-processing (server info in _meta) still reaches the wrapped value.
type typedResult struct {
	mcp.Result
	complete bool
}

func (r typedResult) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(r.Result) // the SDK's own encoding, with every field it knows
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if r.complete {
		obj["resultType"] = json.RawMessage(`"complete"`)
	} else {
		delete(obj, "resultType")
	}
	return json.Marshal(obj)
}
