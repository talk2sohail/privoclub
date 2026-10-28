package gateway

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// An upstream's resources/read reply is untrusted. A null among the contents
// used to be dereferenced in readResource and ended the process for every
// tenant (independent review, 2026-10-29).
func TestRewriteRead(t *testing.T) {
	t.Run("gives each content its public URI", func(t *testing.T) {
		res := &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "echo://note"}, {URI: "echo://other"}}}
		if err := rewriteRead("echo", res); err != nil {
			t.Fatal(err)
		}
		for i, want := range []string{ResourceURI("echo", "echo://note"), ResourceURI("echo", "echo://other")} {
			if res.Contents[i].URI != want {
				t.Errorf("contents[%d].URI = %q, want %q", i, res.Contents[i].URI, want)
			}
		}
	})
	t.Run("no result is malformed", func(t *testing.T) {
		if err := rewriteRead("echo", nil); err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("err = %v, want a malformed-reply error", err)
		}
	})
	for name, contents := range map[string][]*mcp.ResourceContents{
		"only a null":         {nil},
		"a null after a real": {{URI: "echo://note"}, nil},
		"a null before one":   {nil, {URI: "echo://note"}},
	} {
		t.Run(name+" is malformed and changes nothing", func(t *testing.T) {
			err := rewriteRead("echo", &mcp.ReadResourceResult{Contents: contents})
			if err == nil || !strings.Contains(err.Error(), "malformed") || !strings.Contains(err.Error(), "null") {
				t.Fatalf("err = %v, want a malformed-reply error naming the null", err)
			}
			for _, c := range contents {
				if c != nil && c.URI != "echo://note" {
					t.Errorf("a refused reply was still rewritten: %q", c.URI)
				}
			}
		})
	}
}
