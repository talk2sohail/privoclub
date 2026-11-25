package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/argcheck"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/upstream"
)

func toolCall(t *testing.T, hash, schema, args string) call {
	t.Helper()
	var s any
	if err := json.Unmarshal([]byte(schema), &s); err != nil {
		t.Fatal(err)
	}
	return call{p: auth.Principal{OrgID: "org"}, action: "tools/call", args: json.RawMessage(args),
		item: upstream.Item{Alias: "files", Kind: upstream.KindTool, Name: "write", Hash: hash, Tool: &mcp.Tool{Name: "write", InputSchema: s}}}
}

func TestCheckArgsRefusesMismatch(t *testing.T) {
	g := &Gateway{}
	schema := `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`
	if r := g.checkArgs(toolCall(t, "h1", schema, `{"path":"a.txt"}`)); r != nil {
		t.Fatalf("matching arguments refused: %v", r)
	}
	r := g.checkArgs(toolCall(t, "h1", schema, `{"path":["a.txt"]}`))
	if r == nil || r.class != "invalid_arguments" || !strings.Contains(r.msg, "do not match") || !strings.Contains(r.msg, "/properties/path") {
		t.Fatalf("got %+v", r)
	}
	// Prompts and resources have no input schema to check.
	c := toolCall(t, "h1", schema, `{"path":5}`)
	c.action = "prompts/get"
	if r := g.checkArgs(c); r != nil {
		t.Fatalf("a prompt was checked: %v", r)
	}
}

func TestCheckArgsFailsClosedOnUnreadableSchema(t *testing.T) {
	g := &Gateway{}
	r := g.checkArgs(toolCall(t, "h2", `{"type":"object","properties":{"path":{"$ref":"https://schemas.example/path.json"}}}`, `{"path":"a"}`))
	if r == nil || r.class != "schema_unreadable" || !strings.Contains(r.msg, `"files"`) {
		t.Fatalf("got %+v", r)
	}
	if strings.Contains(r.msg, "schemas.example") {
		t.Fatalf("the refusal repeats the upstream's schema: %q", r.msg)
	}
}

func TestCheckArgsRefusesArgumentsTooCostlyToCheck(t *testing.T) {
	old := argcheck.MaxSteps
	argcheck.MaxSteps = 50
	defer func() { argcheck.MaxSteps = old }()
	g := &Gateway{}
	schema := `{"type":"object","properties":{"xs":{"type":"array","items":{"anyOf":[{"type":"string"},{"type":"number"}]}}}}`
	r := g.checkArgs(toolCall(t, "h3", schema, `{"xs":[`+strings.Repeat(`"a",`, 99)+`"a"]}`))
	if r == nil || r.class != "arguments_too_costly" {
		t.Fatalf("got %+v", r)
	}
}
