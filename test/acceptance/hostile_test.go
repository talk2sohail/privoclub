//go:build acceptance

package acceptance

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Found by the independent review on 2026-10-29 (two High findings): one process
// serves every tenant and an upstream is untrusted input, so a reply that breaks
// the protocol may cost that server and nothing else. A null entry in a prompt
// or resource list killed the gateway on the next start, and a null entry in the
// contents of a resources/read reply killed it on the call.

// assertServing fails unless the gateway is up and the healthy "echo" server
// still works through it.
func assertServing(t *testing.T, g *gateway) {
	t.Helper()
	if code, _ := g.raw(http.MethodGet, "/readyz", "", nil); code != http.StatusOK {
		t.Fatalf("gateway not ready after a hostile reply: /readyz = %d\nlogs:\n%s", code, g.logs.String())
	}
	s := g.connect(g.agent)
	if got := callEcho(t, s, "still here"); got != "still here" {
		t.Fatalf("healthy server answered %q", got)
	}
}

// The SDK client drops a null tool entry itself, so that server stays up with
// its real tool; a null prompt or resource entry puts the server down.
func TestHostileUpstreamNullListEntry(t *testing.T) {
	for _, tc := range []struct {
		where    string
		wantDown bool
	}{{"tools", false}, {"prompts", true}, {"resources", true}} {
		where := tc.where
		t.Run(where, func(t *testing.T) {
			g := startGateway(t)
			g.registerEcho("echo", nil, nil)
			g.acceptPins("echo")
			g.registerEcho("evil", []string{"-mode", "hostile", "-null", where}, nil)
			if !tc.wantDown {
				g.acceptPins("evil")
				time.Sleep(300 * time.Millisecond)
				assertServing(t, g)
				if !slices.Contains(toolNames(t, g.connect(g.agent)), "evil__echo") {
					t.Errorf("the real tool of a server whose list held a null is not offered")
				}
				return
			}
			g.eventually("evil is down", 5*time.Second, func() bool { return g.serverStatus("evil") == "down" })

			list := g.api(http.MethodGet, "/api/v1/servers", g.owner, nil, http.StatusOK)
			servers, _ := list["servers"].([]any)
			for _, s := range servers {
				if m, _ := s.(map[string]any); m["alias"] == "evil" {
					if msg, _ := m["error"].(string); !strings.Contains(msg, "null") {
						t.Errorf("error %q does not say that a list held a null entry", msg)
					}
				}
			}

			// The supervisor retries every 80 to 160 ms; let several attempts run.
			time.Sleep(700 * time.Millisecond)
			assertServing(t, g)
			for _, name := range toolNames(t, g.connect(g.agent)) {
				if strings.HasPrefix(name, "evil__") {
					t.Errorf("tool %q of a server with a malformed list is offered", name)
				}
			}
		})
	}
}

func TestHostileUpstreamNullReadContent(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("echo", nil, nil)
	g.registerEcho("evil", []string{"-mode", "hostile", "-null", "read"}, nil)
	g.acceptPins("echo")
	g.acceptPins("evil")

	s := g.connect(g.agent)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	listed, err := s.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var uri string
	for _, r := range listed.Resources {
		if strings.HasPrefix(r.URI, "privo://evil/") {
			uri = r.URI
		}
	}
	if uri == "" {
		t.Fatalf("no resource of the hostile server listed: %+v", listed.Resources)
	}

	_, err = s.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("read of a reply with a null content entry: err = %v, want an error that calls it malformed", err)
	}
	assertServing(t, g)
	if got := callEcho(t, s, "same session"); got != "same session" {
		t.Fatalf("the session that saw the error got %q on its next call", got)
	}
}
