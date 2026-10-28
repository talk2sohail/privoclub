package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fromWire builds a result the way a client does when it reads an upstream
// server's reply: through the SDK's decoder, which is the only way to give a
// result a resultType from outside the SDK.
func fromWire(t *testing.T, res any, wire string) {
	t.Helper()
	if err := json.Unmarshal([]byte(wire), res); err != nil {
		t.Fatalf("decoding %s: %v", wire, err)
	}
}

func marshal(t *testing.T, res mcp.Result, complete bool) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(typedResult{Result: res, complete: complete})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return out
}

// A result carries exactly what the SDK would have written, plus or minus
// resultType; nothing else moves. Each field listed here is one the gateway or
// a client relies on.
func TestTypedResultKeepsEveryField(t *testing.T) {
	tool := &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: "hi"}},
		StructuredContent: map[string]any{"n": 1},
		Meta:              mcp.Meta{"k": "v"},
	}
	prompt := &mcp.GetPromptResult{
		Description: "d",
		Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "hello"}}},
		Meta:        mcp.Meta{"k": "v"},
	}
	read := &mcp.ReadResourceResult{
		Contents:  []*mcp.ResourceContents{{URI: "privo://s/x", Text: "body"}},
		Cacheable: readCache(),
		Meta:      mcp.Meta{"k": "v"},
	}
	for _, tc := range []struct {
		name string
		res  mcp.Result
		keys []string
	}{
		{"tools/call", tool, []string{"isError", "content", "structuredContent", "_meta"}},
		{"prompts/get", prompt, []string{"description", "messages", "_meta"}},
		{"resources/read", read, []string{"contents", "cacheScope", "ttlMs", "_meta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plain, err := json.Marshal(tc.res)
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]json.RawMessage
			if err := json.Unmarshal(plain, &want); err != nil {
				t.Fatal(err)
			}
			for _, complete := range []bool{true, false} {
				got := marshal(t, tc.res, complete)
				for _, k := range tc.keys {
					if string(got[k]) != string(want[k]) || want[k] == nil {
						t.Errorf("complete=%v: %s = %s, want %s", complete, k, got[k], want[k])
					}
				}
				if complete != (string(got["resultType"]) == `"complete"`) {
					t.Errorf("complete=%v: resultType = %s", complete, got["resultType"])
				}
				if len(got) != len(want)+map[bool]int{true: 1, false: 0}[complete] {
					t.Errorf("complete=%v: fields %v, want those of %v plus resultType", complete, keysOf(got), keysOf(want))
				}
			}
		})
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A result relayed from a server on 2026-07-28 already has the field; an older
// client must still not see it, and a newer one must not see it twice.
func TestTypedResultOverridesWhatTheUpstreamSent(t *testing.T) {
	var relayed mcp.CallToolResult
	fromWire(t, &relayed, `{"resultType":"complete","content":[{"type":"text","text":"hi"}]}`)
	if got := marshal(t, &relayed, false); got["resultType"] != nil {
		t.Errorf("older client got resultType %s", got["resultType"])
	}
	raw, err := json.Marshal(typedResult{Result: &relayed, complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), `"resultType"`); n != 1 {
		t.Errorf("%d resultType fields in %s", n, raw)
	}
}

// Without a session the version is unknown, which the SDK treats as the
// newest: an extra field is ignored by a client that does not know it, a
// missing one is not.
func TestWithResultTypeDefaultsToCompleteWhenVersionUnknown(t *testing.T) {
	res, err := withResultType(&mcp.CallToolRequest{}, &mcp.CallToolResult{Content: []mcp.Content{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"resultType":"complete"`) {
		t.Errorf("got %s", b)
	}
}

func TestWithResultTypeFailsClosed(t *testing.T) {
	req := &mcp.CallToolRequest{}
	var input mcp.CallToolResult
	fromWire(t, &input, `{"resultType":"input_required"}`)
	var inputPrompt mcp.GetPromptResult
	fromWire(t, &inputPrompt, `{"resultType":"input_required"}`)
	var inputRead mcp.ReadResourceResult
	fromWire(t, &inputRead, `{"resultType":"input_required"}`)

	for name, res := range map[string]mcp.Result{
		"nil tool result":     (*mcp.CallToolResult)(nil),
		"nil prompt result":   (*mcp.GetPromptResult)(nil),
		"nil resource result": (*mcp.ReadResourceResult)(nil),
		"nil result":          nil,
		"tool asks for input": &input,
		"prompt asks input":   &inputPrompt,
		"resource asks input": &inputRead,
	} {
		if got, err := withResultType(req, res, nil); err == nil || got != nil {
			t.Errorf("%s: got %v, %v; want an error and no result", name, got, err)
		}
	}
}

func TestWithResultTypePassesErrorsThrough(t *testing.T) {
	want := errBoom{}
	got, err := withResultType(&mcp.CallToolRequest{}, &mcp.CallToolResult{}, want)
	if err != want || got != nil {
		t.Errorf("got %v, %v", got, err)
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
