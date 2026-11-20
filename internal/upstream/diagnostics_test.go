package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
)

type diagSink struct {
	mu sync.Mutex
	ds []store.Diagnostic
}

func (d *diagSink) InsertCallStats(context.Context, store.OrgID, ...store.CallStat) error { return nil }
func (d *diagSink) InsertDiagnostics(_ context.Context, _ store.OrgID, rows ...store.Diagnostic) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ds = append(d.ds, rows...)
	return nil
}

// ADR 0024: a failed connection and its recovery are in the diagnostic log,
// with the server's credentials removed, and the metrics see the state.
func TestUpstreamDiagnostics(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "flaky", Version: "0.0.0"}, nil)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	var healthy atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			http.Error(w, "down for maintenance", http.StatusBadGateway)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	sink := &diagSink{}
	rec := telemetry.New(sink, telemetry.Options{FlushInterval: 10 * time.Millisecond})
	fs := &fakeStore{servers: []store.Server{{OrgID: "o", ID: "s1", Alias: "flaky", Transport: "http",
		Config: store.ServerConfig{URL: ts.URL + "/mcp?api_key=s3cret-value"}, Enabled: true, UpdatedAt: time.Now()}}}
	m := New(Options{Store: fs, HTTPClient: loopClient(), RetryBase: time.Hour, Diagnostics: rec})
	t.Cleanup(m.Close)
	if err := m.Sync(context.Background(), "o"); err != nil {
		t.Fatal(err)
	}
	m.WaitSettled(context.Background(), "s1", 10*time.Second)
	if st := m.UpstreamStates(); len(st) != 1 || st[0].Server != "flaky" || st[0].Up {
		t.Fatalf("states while down: %+v", st)
	}

	healthy.Store(true)
	m.Kick("s1")
	waitFor(t, 10*time.Second, "the server comes up", func() bool { s, _ := m.State("s1"); return s.Status == StatusUp })
	if st := m.UpstreamStates(); len(st) != 1 || !st[0].Up {
		t.Fatalf("states while up: %+v", st)
	}
	rec.Close()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.ds) != 2 {
		t.Fatalf("diagnostics: %+v", sink.ds)
	}
	failed, back := sink.ds[0], sink.ds[1]
	if failed.Level != store.LevelWarn || failed.Category != store.CategoryUpstream || failed.Server != "flaky" ||
		!strings.HasPrefix(failed.Message, "connection failed: ") {
		t.Fatalf("failure: %+v", failed)
	}
	if strings.Contains(failed.Message, "s3cret-value") {
		t.Fatalf("the server's key is in the log: %q", failed.Message)
	}
	if back.Level != store.LevelInfo || back.Message != "connected again" {
		t.Fatalf("recovery: %+v", back)
	}
}

// Review of 2026-11-15, T-07: a connection error holds the server's URL with
// its key; the diagnostic does not. (The test above cannot fail on this: a
// 502 puts no URL in the error.)
func TestUpstreamDiagnosticsRedactTheURL(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	addr := closed.URL
	closed.Close() // a closed port: the error names the URL
	sink := &diagSink{}
	rec := telemetry.New(sink, telemetry.Options{FlushInterval: 10 * time.Millisecond})
	fs := &fakeStore{servers: []store.Server{{OrgID: "o", ID: "s1", Alias: "gone", Transport: "http",
		Config: store.ServerConfig{URL: addr + "/mcp?api_key=s3cret-value"}, Enabled: true, UpdatedAt: time.Now()}}}
	m := New(Options{Store: fs, HTTPClient: loopClient(), RetryBase: time.Hour, Diagnostics: rec})
	t.Cleanup(m.Close)
	if err := m.Sync(context.Background(), "o"); err != nil {
		t.Fatal(err)
	}
	m.WaitSettled(context.Background(), "s1", 10*time.Second)
	rec.Close()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.ds) == 0 {
		t.Fatal("no diagnostic")
	}
	for _, d := range sink.ds {
		if strings.Contains(d.Message, "s3cret-value") || strings.Contains(d.Message, "api_key") {
			t.Fatalf("the key is in the log: %q", d.Message)
		}
	}
	if st, _ := m.State("s1"); strings.Contains(st.Error, "s3cret-value") {
		t.Fatalf("the key is in the status: %q", st.Error)
	}
}
