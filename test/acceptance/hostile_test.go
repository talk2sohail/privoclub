//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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

// The class behind R-01 (independent review, 2026-10-29): the go-sdk client
// dereferences "params" in the handler of notifications/elicitation/complete
// and nowhere in the SDK is a panic recovered, so one notification without
// params from any upstream ended the process for every tenant, and again after
// each restart. Every notification the client handles is tried with params
// absent and null, so the next instance of the class is caught too.
func TestHostileUpstreamNotification(t *testing.T) {
	for _, method := range []string{
		"notifications/elicitation/complete",
		"notifications/progress",
		"notifications/message",
		"notifications/cancelled",
		"notifications/resources/updated",
		"notifications/tools/list_changed",
		"notifications/prompts/list_changed",
		"notifications/resources/list_changed",
	} {
		for _, params := range []string{"absent", "null"} {
			t.Run(strings.TrimPrefix(method, "notifications/")+" params "+params, func(t *testing.T) {
				g := startGateway(t)
				g.registerEcho("echo", nil, nil)
				g.acceptPins("echo")
				g.registerEcho("evil", []string{"-mode", "raw-notify", "-method", method, "-params", params}, nil)
				time.Sleep(300 * time.Millisecond) // the notification follows the handshake at once
				assertServing(t, g)
			})
		}
	}
}

// The other side of the gateway: notifications from an AI client with params
// absent, null or of the wrong type must not end the process either. The SDK
// server reads as safe today; this keeps it so across upgrades.
func TestMalformedClientNotificationsDoNotCrash(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("echo", nil, nil)
	g.acceptPins("echo")
	for _, method := range []string{"notifications/initialized", "notifications/cancelled", "notifications/progress", "notifications/roots/list_changed"} {
		for _, params := range []string{``, `,"params":null`, `,"params":[]`, `,"params":"x"`} {
			body := []byte(`{"jsonrpc":"2.0","method":"` + method + `"` + params + `}`)
			req, err := http.NewRequest(http.MethodPost, g.base+"/mcp", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+g.agent)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("MCP-Protocol-Version", legacyProtocol)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s%s: the gateway did not answer: %v\nlogs:\n%s", method, params, err, g.logs.String())
			}
			_ = resp.Body.Close()
		}
	}
	assertServing(t, g)
}

// remoteUpstream serves a small MCP server over HTTP, in this process.
func remoteUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "remote", Version: "0.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Repeat."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
		})
	srv.AddPrompt(&mcp.Prompt{Name: "greet"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{}, nil
	})
	srv.AddResource(&mcp.Resource{URI: "remote://note", Name: "note"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "remote://note", Text: "body"}}}, nil
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts
}

// R-02 (independent review, 2026-10-29): the text of a transport error holds
// the upstream's URL, and URLs often carry the API key in the query. The error
// went back to the AI client as "call failed: ...". Connection errors are for
// administrators (the server list hides them from members), so the caller gets
// a fixed message, and the log gets the detail with the query removed.
func TestUpstreamTransportErrorsDoNotReachTheClient(t *testing.T) {
	const secret = "LEAK-ME-7f3a9c41"
	for _, method := range []string{"tools/call", "prompts/get", "resources/read"} {
		t.Run(method, func(t *testing.T) {
			ts := remoteUpstream(t)
			g := startGateway(t, "PRIVO_EGRESS_ALLOW_CIDRS=127.0.0.1/32")
			g.api(http.MethodPost, "/api/v1/servers", g.owner, map[string]any{
				"alias": "remote", "transport": "http", "url": ts.URL + "/mcp?key=" + secret,
			}, http.StatusCreated)
			g.acceptPins("remote")
			s := g.connect(g.agent)

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var uri string
			listed, err := s.ListResources(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range listed.Resources {
				if strings.HasPrefix(r.URI, "privo://remote/") {
					uri = r.URI
				}
			}
			if uri == "" {
				t.Fatalf("resource of the remote server not listed: %+v", listed.Resources)
			}

			ts.CloseClientConnections()
			ts.Close() // the upstream goes away after registration

			var text string
			switch method {
			case "tools/call":
				res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "remote__echo", Arguments: map[string]any{}})
				if err != nil {
					text = err.Error()
				} else if len(res.Content) > 0 {
					text = res.Content[0].(*mcp.TextContent).Text
					if !res.IsError {
						t.Fatalf("a call to a dead upstream succeeded: %q", text)
					}
				}
			case "prompts/get":
				_, err := s.GetPrompt(ctx, &mcp.GetPromptParams{Name: "remote__greet"})
				if err == nil {
					t.Fatal("a prompt from a dead upstream was served")
				}
				text = err.Error()
			case "resources/read":
				_, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
				if err == nil {
					t.Fatal("a resource from a dead upstream was served")
				}
				text = err.Error()
			}
			if text == "" {
				t.Fatal("the client got no error text at all")
			}
			for _, bad := range []string{secret, "key=", "127.0.0.1"} {
				if strings.Contains(text, bad) {
					t.Errorf("the client was told %q, which contains %q", text, bad)
				}
			}
			if !strings.Contains(text, "remote") {
				t.Errorf("error %q does not name the server", text)
			}
			if strings.Contains(g.logs.String(), secret) {
				t.Errorf("the gateway log contains the secret:\n%s", g.logs.String())
			}
		})
	}
}
