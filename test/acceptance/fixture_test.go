//go:build acceptance

package acceptance

import (
	"context"
	"os/exec"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestEchoServerFixture checks the test server on its own, so a failure in the
// gateway tests is never caused by a broken fixture.
func TestEchoServerFixture(t *testing.T) {
	ctx := context.Background()
	c := mcp.NewClient(&mcp.Implementation{Name: "fixture-check", Version: "0.0.0"}, nil)
	s, err := c.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(echoBin)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "hi"}})
	if err != nil || res.Content[0].(*mcp.TextContent).Text != "hi" {
		t.Fatalf("echo: %v %+v", err, res)
	}
	p, err := s.GetPrompt(ctx, &mcp.GetPromptParams{Name: "greet", Arguments: map[string]string{"name": "Ada"}})
	if err != nil || p.Messages[0].Content.(*mcp.TextContent).Text != "Hello Ada" {
		t.Fatalf("greet: %v %+v", err, p)
	}
	r, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: "echo://note"})
	if err != nil || r.Contents[0].Text != "note-body" {
		t.Fatalf("note: %v %+v", err, r)
	}
}
