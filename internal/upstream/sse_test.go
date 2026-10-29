package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/store"
)

// Found while testing R-04: the legacy SSE transport ties its event stream to
// the context given to Connect, and connect gave it the context of the
// handshake, which is cancelled when connect returns. An SSE upstream went up,
// was dropped at once ("connection closed") and was reconnected, over and over.
func TestSSEUpstreamStaysConnected(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "legacy", Version: "0.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Repeat."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
		})
	ts := httptest.NewServer(mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)

	fs := &fakeStore{servers: []store.Server{{OrgID: "o", ID: "s1", Alias: "legacy", Transport: "sse",
		Config: store.ServerConfig{URL: ts.URL}, Enabled: true, UpdatedAt: time.Now()}}}
	m := New(Options{Store: fs, HTTPClient: loopClient(), RetryBase: time.Hour})
	t.Cleanup(m.Close)
	if err := m.Sync(context.Background(), "o"); err != nil {
		t.Fatal(err)
	}
	m.WaitSettled(context.Background(), "s1", 5*time.Second)
	time.Sleep(400 * time.Millisecond) // the stream used to end as soon as connect returned

	st, _ := m.State("s1")
	if st.Status != StatusUp || len(st.Items) != 1 {
		t.Fatalf("SSE server is %q (%q) with %d items, want %q with 1", st.Status, st.Error, len(st.Items), StatusUp)
	}
	session, err := m.Session("o", "s1")
	if err != nil {
		t.Fatal(err)
	}
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("a call over the SSE connection: %v, %+v", err, res)
	}
}
