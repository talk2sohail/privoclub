//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// R-04 (independent review, 2026-10-29): one process holds what the upstreams
// of every tenant send, and an upstream is untrusted input. What a server can
// make the gateway hold or do is bounded: the size of a reply, the time of a
// call, the pages and bytes of a list, and the rate of refreshes. The tests set
// small limits, so none of them needs a large payload.

type blobArgs struct {
	Bytes int `json:"bytes"`
}
type sleepArgs struct {
	Ms int `json:"ms"`
}

// limitsUpstream serves tools small, blob (a reply of N bytes) and sleep (an
// answer after N ms) over streamable HTTP, in this process. A Go SDK server
// answers a call with an event stream unless jsonReplies asks for a JSON body,
// and the gateway has to bound both.
func limitsUpstream(t *testing.T, jsonReplies bool) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "limits", Version: "0.0.0"}, nil)
	text := func(s string) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}, nil, nil
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "small", Description: "Answer small."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return text("small")
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "blob", Description: "Answer with bytes bytes."},
		func(_ context.Context, _ *mcp.CallToolRequest, in blobArgs) (*mcp.CallToolResult, any, error) {
			return text(strings.Repeat("a", in.Bytes))
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "sleep", Description: "Answer after ms milliseconds."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in sleepArgs) (*mcp.CallToolResult, any, error) {
			select {
			case <-time.After(time.Duration(in.Ms) * time.Millisecond):
			case <-ctx.Done():
			}
			return text("slept")
		})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{JSONResponse: jsonReplies}))
	t.Cleanup(ts.Close)
	return ts
}

// callText calls a tool and returns what the caller gets to read, whether the
// failure came as a protocol error or as an error result, and how it ended.
func callText(s *mcp.ClientSession, name string, args map[string]any, within time.Duration) (text string, failed bool, took time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	start := time.Now()
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	took = time.Since(start)
	switch {
	case err != nil:
		return err.Error(), true, took
	case len(res.Content) == 0:
		return "", res.IsError, took
	}
	tc, _ := res.Content[0].(*mcp.TextContent)
	if tc == nil {
		return "", res.IsError, took
	}
	return tc.Text, res.IsError, took
}

// auditOutcome waits for the completion of the newest call of tool on server
// and returns its outcome.
func auditOutcome(g *gateway, server, tool string) string {
	g.t.Helper()
	var outcome string
	g.eventually("the audit row of "+server+"."+tool, 10*time.Second, func() bool {
		for _, e := range auditEvents(g) { // newest first
			if e["server"] != server || e["item"] != tool { // "item" is the tool, prompt or resource name
				continue
			}
			outcome, _ = e["outcome"].(string)
			return outcome != "" && outcome != "pending" // completions are written in batches
		}
		return false
	})
	return outcome
}

func (g *gateway) registerRemote(alias, transport, url string) {
	g.t.Helper()
	g.api(http.MethodPost, "/api/v1/servers", g.owner, map[string]any{"alias": alias, "transport": transport, "url": url}, http.StatusCreated)
	g.acceptPins(alias)
}

func TestOversizedUpstreamReplyIsRefused(t *testing.T) {
	const secret = "LEAK-ME-5b1d7e20"
	for _, mode := range []string{"json", "event-stream"} {
		t.Run(mode, func(t *testing.T) {
			ts := limitsUpstream(t, mode == "json")
			g := startGateway(t, "PRIVO_EGRESS_ALLOW_CIDRS=127.0.0.1/32", "PRIVO_UPSTREAM_MAX_RESPONSE_BYTES=65536")
			g.registerEcho("echo", nil, nil)
			g.acceptPins("echo")
			g.registerRemote("big", "http", ts.URL+"/mcp?key="+secret)
			s := g.connect(g.agent)

			if text, failed, _ := callText(s, "big__small", map[string]any{}, 20*time.Second); failed || text != "small" {
				t.Fatalf("a small reply: %q, failed %v", text, failed)
			}
			if text, failed, _ := callText(s, "big__blob", map[string]any{"bytes": 20000}, 20*time.Second); failed || len(text) != 20000 {
				t.Fatalf("a reply below the limit: %d bytes, failed %v", len(text), failed)
			}

			text, failed, _ := callText(s, "big__blob", map[string]any{"bytes": 200000}, 20*time.Second)
			if !failed {
				t.Fatalf("a reply of 200000 bytes went through a limit of 65536: %d bytes", len(text))
			}
			if len(text) > 1000 || strings.Contains(text, "aaaa") {
				t.Errorf("part of the oversized reply reached the caller: %d bytes of text", len(text))
			}
			for _, bad := range []string{secret, "key=", "127.0.0.1"} {
				if strings.Contains(text, bad) {
					t.Errorf("the caller was told %q, which contains %q", text, bad)
				}
			}
			if !strings.Contains(text, "big") {
				t.Errorf("error %q does not name the server", text)
			}
			if got := auditOutcome(g, "big", "blob"); got != "error" {
				t.Errorf("audit outcome of the oversized call is %q, want error", got)
			}

			// The gateway and its other servers are untouched, and the server
			// that sent too much is connected again and answers small replies.
			assertServing(t, g)
			g.eventually("the server to serve again after the reconnect", 15*time.Second, func() bool {
				text, failed, _ := callText(s, "big__small", map[string]any{}, 5*time.Second)
				return !failed && text == "small"
			})
			if strings.Contains(g.logs.String(), secret) {
				t.Errorf("the gateway log contains the secret:\n%s", g.logs.String())
			}
		})

		// Control: the same call with the limit out of the way, so that the
		// limit is what stopped the call above and not the test server.
		t.Run(mode+" without the limit", func(t *testing.T) {
			ts := limitsUpstream(t, mode == "json")
			g := startGateway(t, "PRIVO_EGRESS_ALLOW_CIDRS=127.0.0.1/32", "PRIVO_UPSTREAM_MAX_RESPONSE_BYTES=1073741824")
			g.registerRemote("big", "http", ts.URL+"/mcp")
			s := g.connect(g.agent)
			if text, failed, _ := callText(s, "big__blob", map[string]any{"bytes": 200000}, 20*time.Second); failed || len(text) != 200000 {
				t.Fatalf("200000 bytes without the limit: %d bytes, failed %v", len(text), failed)
			}
		})
	}
}

func TestUpstreamCallTimeout(t *testing.T) {
	ts := limitsUpstream(t, false)
	g := startGateway(t, "PRIVO_EGRESS_ALLOW_CIDRS=127.0.0.1/32", "PRIVO_UPSTREAM_CALL_TIMEOUT=700ms")
	g.registerEcho("echo", nil, nil)
	g.acceptPins("echo")
	g.registerRemote("slow", "http", ts.URL+"/mcp")
	s := g.connect(g.agent)

	if text, failed, _ := callText(s, "slow__sleep", map[string]any{"ms": 100}, 20*time.Second); failed || text != "slept" {
		t.Fatalf("a call that answers within the limit: %q, failed %v", text, failed)
	}

	text, failed, took := callText(s, "slow__sleep", map[string]any{"ms": 60000}, 30*time.Second)
	if !failed {
		t.Fatalf("a call that sleeps for a minute was answered: %q", text)
	}
	if took > 10*time.Second {
		t.Errorf("the call took %s for a limit of 700ms", took)
	}
	if !strings.Contains(text, "slow") || !strings.Contains(text, "700ms") {
		t.Errorf("error %q should name the server and the limit", text)
	}
	if got := auditOutcome(g, "slow", "sleep"); got != "error" {
		t.Errorf("audit outcome of the call that timed out is %q, want error", got)
	}
	// The connection is fine: the next call goes through at once.
	if text, failed, _ := callText(s, "slow__small", map[string]any{}, 5*time.Second); failed || text != "small" {
		t.Errorf("the call after the timeout: %q, failed %v", text, failed)
	}
	assertServing(t, g)
}

// A server that announces a change of its tools all the time (here: 60 times
// in 1.5 s, each time with a new description) gets one refresh per gap, and the
// definitions it no longer offers do not pile up as pending pins.
func TestListChangedFloodIsRateLimited(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "flood", Version: "0.0.0"}, nil)
	var lists atomic.Int64
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				lists.Add(1)
			}
			return next(ctx, method, req)
		}
	})
	setTool := func(version int) {
		srv.AddTool(&mcp.Tool{Name: "flood", Description: fmt.Sprintf("version %d", version), InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "flood"}}}, nil
			})
	}
	setTool(0)
	// The streamable client of the gateway does not listen for server
	// notifications, so this server speaks the older SSE transport, which does.
	ts := httptest.NewServer(mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)

	g := startGateway(t, "PRIVO_EGRESS_ALLOW_CIDRS=127.0.0.1/32", "PRIVO_UPSTREAM_REFRESH_GAP=400ms")
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("gateway log:\n%s", g.logs.String())
		}
	})
	g.registerRemote("flood", "sse", ts.URL)
	g.eventually("the flood server up", 10*time.Second, func() bool { return g.serverStatus("flood") == "up" })

	lists.Store(0)
	for v, end := 1, time.Now().Add(1500*time.Millisecond); time.Now().Before(end); v++ {
		setTool(v)
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(1200 * time.Millisecond) // the last refresh runs one gap after the one before

	n := lists.Load()
	if n < 3 {
		t.Fatalf("the gateway asked for the tools %d times during the flood: the notifications do not arrive, so this test proves nothing", n)
	}
	if n > 9 {
		t.Errorf("the gateway asked for the tools %d times in 2.7 s with a gap of 400ms; 3 to 9 is right", n)
	}

	listed := g.api(http.MethodGet, "/api/v1/servers/flood/pins", g.owner, nil, http.StatusOK)
	pins, _ := listed["pins"].([]any)
	if len(pins) > 2 {
		t.Errorf("%d pins are on record for a server that offers one tool; the ones it stopped offering must not pile up", len(pins))
	}
	assertNoPanic(t, g)
}

// assertNoPanic fails if the gateway logged a recovered panic.
func assertNoPanic(t *testing.T, g *gateway) {
	t.Helper()
	if strings.Contains(g.logs.String(), "panic") {
		t.Errorf("the gateway logged a panic:\n%s", g.logs.String())
	}
}
