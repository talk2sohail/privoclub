package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/store"
)

// Found by the independent review on 2026-10-29: a null entry in an upstream's
// prompt or resource list panicked in load, and a restart while the upstream
// answered that way took the whole gateway down. An upstream is untrusted
// input and one process serves every tenant, so a list that breaks the protocol
// has to cost that server only: it goes down, with a reason.

// hostileServer is a real MCP server over HTTP whose list of nullIn (tools,
// prompts or resources) carries a JSON null after the real entry.
func hostileServer(t *testing.T, nullIn string) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "hostile", Version: "0.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Repeat."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
	srv.AddPrompt(&mcp.Prompt{Name: "greet"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{}, nil
	})
	srv.AddResource(&mcp.Resource{URI: "echo://note", Name: "note"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{}, nil
	})
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if err != nil {
				return res, err
			}
			switch r := res.(type) {
			case *mcp.ListToolsResult:
				if nullIn == "tools" {
					r.Tools = append(r.Tools, nil)
				}
			case *mcp.ListPromptsResult:
				if nullIn == "prompts" {
					r.Prompts = append(r.Prompts, nil)
				}
			case *mcp.ListResourcesResult:
				if nullIn == "resources" {
					r.Resources = append(r.Resources, nil)
				}
			}
			return res, nil
		}
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts
}

// The SDK client drops invalid tool entries itself (filterValidTools), so a
// null in tools/list never reaches load and the server stays up with its real
// tools. Prompts and resources have no such filter: a null there must put the
// server down with a reason, not panic. If a future SDK stops filtering tools,
// the first case fails and the decision is made on purpose.
func TestLoadRefusesNullListEntries(t *testing.T) {
	for _, tc := range []struct {
		nullIn   string
		wantDown bool
	}{{"tools", false}, {"prompts", true}, {"resources", true}} {
		nullIn := tc.nullIn
		t.Run(nullIn, func(t *testing.T) {
			ts := hostileServer(t, nullIn)
			fs := &fakeStore{servers: []store.Server{{OrgID: "o", ID: "s1", Alias: "evil", Transport: "http",
				Config: store.ServerConfig{URL: ts.URL}, Enabled: true, UpdatedAt: time.Now()}}}
			m := New(Options{Store: fs, HTTPClient: loopClient(), RetryBase: time.Hour})
			defer m.Close()
			if err := m.Sync(context.Background(), "o"); err != nil {
				t.Fatal(err)
			}
			m.WaitSettled(context.Background(), "s1", 5*time.Second)

			st, ok := m.State("s1")
			if !tc.wantDown {
				if !ok || st.Status != StatusUp || len(st.Items) != 3 {
					t.Fatalf("server is %+v, want %q with its three real items", st, StatusUp)
				}
				return
			}
			if !ok || st.Status != StatusDown {
				t.Fatalf("server is %+v, want status %q", st, StatusDown)
			}
			if !strings.Contains(st.Error, nullIn+"/list") || !strings.Contains(st.Error, "null") {
				t.Errorf("error %q does not say that %s/list held a null entry", st.Error, nullIn)
			}
			if items := m.Items("o"); len(items) != 0 {
				t.Errorf("a server with a malformed list offers %d items, want none", len(items))
			}
		})
	}
}
