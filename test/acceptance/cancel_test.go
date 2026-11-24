//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Issue #47 (F02, ADR 0004, ADR 0005): client cancellation withdraws a pending
// approval while held, forwards cancellation to stdio and HTTP upstreams
// without dropping the session, and still writes audit completion & telemetry.
func TestClientCancellationEndToEnd(t *testing.T) {
	httpStarted := make(chan struct{}, 4)
	httpCancelled := make(chan struct{}, 4)
	httpSrv := mcp.NewServer(&mcp.Implementation{Name: "http-slow", Version: "0.0.1"}, nil)
	mcp.AddTool(httpSrv, &mcp.Tool{Name: "slow", Description: "Waits until cancelled."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			httpStarted <- struct{}{}
			select {
			case <-ctx.Done():
				httpCancelled <- struct{}{}
				return nil, nil, ctx.Err()
			case <-time.After(10 * time.Second):
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "finished"}}}, nil, nil
			}
		})
	mcp.AddTool(httpSrv, &mcp.Tool{Name: "ping", Description: "Immediate pong."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
		})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return httpSrv }, nil))
	t.Cleanup(ts.Close)

	g := startGateway(t, "PRIVO_EGRESS_ALLOW_CIDRS=127.0.0.1/32", "PRIVO_APPROVAL_HOLD=5s")
	flagFile := filepath.Join(t.TempDir(), "slow.flag")
	g.registerEcho("echo", []string{"-mode", "slow"}, map[string]string{"FLAG": flagFile})
	g.acceptPins("echo")
	g.registerRemote("httpup", "http", ts.URL+"/mcp")

	member := g.issue("ada")
	s := g.connect(member)
	g.eventually("both slow tools listed", 5*time.Second, func() bool {
		names := toolNames(t, s)
		return slices.Contains(names, "echo__slow") && slices.Contains(names, "httpup__slow")
	})

	// 1. Cancelling while held in Approval.Gate withdraws the pending approval,
	// purges its arguments, prevents later approval, and audits both refusal and withdrawal.
	pol := g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{
		"server": "echo", "tool": "slow", "action": "ask",
	}, http.StatusCreated)
	polID := idOfPolicy(t, pol)

	holdCtx, cancelHold := context.WithCancel(context.Background())
	holdDone := make(chan error, 1)
	go func() {
		_, err := s.CallTool(holdCtx, &mcp.CallToolParams{Name: "echo__slow", Arguments: map[string]any{"ms": 1000}})
		holdDone <- err
	}()

	var apID string
	g.eventually("pending approval appears", 5*time.Second, func() bool {
		p := g.pendingApprovals(g.owner)
		if len(p) == 1 {
			apID, _ = p[0]["id"].(string)
		}
		return apID != ""
	})
	cancelHold()

	select {
	case err := <-holdDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("held CallTool err = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("held CallTool did not return after cancel")
	}

	g.eventually("pending approval withdrawn in history", 5*time.Second, func() bool {
		if len(g.pendingApprovals(g.owner)) != 0 {
			return false
		}
		hist := g.api(http.MethodGet, "/api/v1/approvals?status=history", g.owner, nil, http.StatusOK)
		for _, a := range list(hist, "approvals") {
			if a["id"] == apID {
				return a["status"] == "withdrawn" && a["decided_by"] == "caller cancelled" && a["arguments"] == nil
			}
		}
		return false
	})
	if code, _ := g.raw(http.MethodPost, "/api/v1/approvals/"+apID+"/approve", g.owner, nil); code != http.StatusConflict {
		t.Fatalf("approving cancelled approval: status %d, want 409", code)
	}
	wantCall(g, "echo", "slow", "tools/call", "ask", "cancelled_while_pending")
	wantAdmin(g, "approval.withdraw:"+apID)
	if _, err := os.Stat(flagFile); err == nil {
		t.Fatal("stdio upstream was called despite cancellation during approval hold")
	}

	g.api(http.MethodDelete, "/api/v1/policies/"+polID, g.owner, nil, http.StatusOK)

	// 2. Cancelling an allowed upstream stdio call forwards notifications/cancelled
	// to echoserver, writes audit completion "cancelled", and keeps the session healthy.
	stdioCtx, cancelStdio := context.WithCancel(context.Background())
	stdioDone := make(chan error, 1)
	go func() {
		_, err := s.CallTool(stdioCtx, &mcp.CallToolParams{Name: "echo__slow", Arguments: map[string]any{"ms": 10000}})
		stdioDone <- err
	}()
	g.eventually("stdio upstream started slow tool", 5*time.Second, func() bool {
		b, _ := os.ReadFile(flagFile)
		return strings.Contains(string(b), "slow:started")
	})
	cancelStdio()

	select {
	case err := <-stdioDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stdio CallTool err = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stdio CallTool did not return after cancel")
	}
	g.eventually("stdio upstream received notifications/cancelled", 5*time.Second, func() bool {
		b, _ := os.ReadFile(flagFile)
		return strings.Contains(string(b), "notify:cancelled") && strings.Contains(string(b), "slow:cancelled")
	})
	wantCall(g, "echo", "slow", "tools/call", "allow", "cancelled")

	if got := callEcho(t, s, "after-stdio-cancel"); got != "after-stdio-cancel" {
		t.Fatalf("echo after stdio cancel = %q, want after-stdio-cancel", got)
	}

	// 3. Cancelling an upstream Streamable HTTP call forwards cancellation to
	// the HTTP upstream (tested for both 2026-07-28 and 2025-11-25 clients).
	for idx, proto := range []string{newProtocol, legacyProtocol} {
		t.Run("http_"+proto, func(t *testing.T) {
			connCtx, cancelConn := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelConn()
			cli := mcp.NewClient(&mcp.Implementation{Name: "cancel-e2e", Version: "0.0.0"}, nil)
			cs, err := cli.Connect(connCtx, &mcp.StreamableClientTransport{
				Endpoint:             g.base + "/mcp",
				HTTPClient:           &http.Client{Transport: bearer{member, http.DefaultTransport}},
				DisableStandaloneSSE: true,
			}, &mcp.ClientSessionOptions{ProtocolVersion: proto})
			if err != nil {
				t.Fatalf("connect %s: %v", proto, err)
			}
			defer func() { _ = cs.Close() }()

			httpCtx, cancelHTTP := context.WithCancel(context.Background())
			httpDone := make(chan error, 1)
			go func() {
				_, err := cs.CallTool(httpCtx, &mcp.CallToolParams{Name: "httpup__slow"})
				httpDone <- err
			}()
			select {
			case <-httpStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("http upstream tool did not start")
			}
			cancelHTTP()

			select {
			case err := <-httpDone:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("http CallTool err = %v, want context.Canceled", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("http CallTool did not return after cancel")
			}
			select {
			case <-httpCancelled:
			case <-time.After(5 * time.Second):
				t.Fatal("http upstream handler did not observe cancellation")
			}
			wantCount := idx + 1
			g.eventually("http cancellation audited", 5*time.Second, func() bool {
				n := 0
				for _, e := range auditEvents(g) {
					if e["server"] == "httpup" && e["item"] == "slow" && e["outcome"] == "cancelled" {
						n++
					}
				}
				return n == wantCount
			})

			if text, isErr := call(t, cs, "httpup__ping", nil); isErr || text != "pong" {
				t.Fatalf("httpup__ping after cancel = %q (err=%v), want pong", text, isErr)
			}
		})
	}

	g.eventually("telemetry records cancelled error_class", 5*time.Second, func() bool {
		calls := list(g.api(http.MethodGet, "/api/v1/telemetry/calls", g.owner, nil, http.StatusOK), "calls")
		for _, c := range calls {
			if c["error_class"] == "cancelled" {
				return true
			}
		}
		return false
	})
}
