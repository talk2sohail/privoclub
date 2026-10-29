package upstream

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/store"
)

// R-04 (independent review, 2026-10-29): an upstream is untrusted input and
// one process holds what every tenant's upstreams send. These tests use small
// limits, so none of them needs a large payload.

// toolsCtl steers the tools/list of a test server.
type toolsCtl struct {
	calls   atomic.Int64  // tools/list requests so far
	hang    atomic.Bool   // answer none until released
	release chan struct{} // closed at the end of the test
}

func mkTool(name string, descLen int) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: strings.Repeat("x", descLen), InputSchema: map[string]any{"type": "object"}}
}

// toolsServer is a real MCP server over HTTP that answers every tools/list
// itself: answer gets the cursor of the request and the number of the call.
func toolsServer(t *testing.T, answer func(cursor string, call int) *mcp.ListToolsResult) (*httptest.Server, *toolsCtl) {
	t.Helper()
	c := &toolsCtl{release: make(chan struct{})}
	srv := mcp.NewServer(&mcp.Implementation{Name: "paged", Version: "0.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "real", Description: "Only here so that the server advertises tools."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/list" {
				return next(ctx, method, req)
			}
			n := int(c.calls.Add(1))
			if c.hang.Load() {
				select {
				case <-ctx.Done():
				case <-c.release:
				}
				return nil, errors.New("released")
			}
			cursor := ""
			if p, ok := req.GetParams().(*mcp.ListToolsParams); ok && p != nil {
				cursor = p.Cursor
			}
			return answer(cursor, n), nil
		}
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(c.release) }) // runs before ts.Close: it waits for hung requests
	return ts, c
}

// start registers the server with a manager and waits for the first connection
// attempt to settle.
func start(t *testing.T, ts *httptest.Server, fs *fakeStore, o Options) *Manager {
	t.Helper()
	fs.servers = []store.Server{{OrgID: "o", ID: "s1", Alias: "up", Transport: "http",
		Config: store.ServerConfig{URL: ts.URL}, Enabled: true, UpdatedAt: time.Now()}}
	o.Store, o.HTTPClient, o.RetryBase = fs, loopClient(), time.Hour
	m := New(o)
	t.Cleanup(m.Close)
	if err := m.Sync(context.Background(), "o"); err != nil {
		t.Fatal(err)
	}
	m.WaitSettled(context.Background(), "s1", 10*time.Second)
	return m
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("after %s: %s", d, what)
}

// A list whose pages never end used to keep the load going until the connect
// timeout (the SDK follows cursors without a limit), or, with a new item on
// each page, until 2000 items had been stored.
func TestEndlessPaginationBringsServerDown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(cursor string, call int) *mcp.ListToolsResult
		want   string
	}{
		{"same cursor again and again", func(_ string, call int) *mcp.ListToolsResult {
			return &mcp.ListToolsResult{Tools: []*mcp.Tool{mkTool(fmt.Sprintf("t%d", call), 0)}, NextCursor: "again"}
		}, "cursor"},
		{"new cursor on every empty page", func(_ string, call int) *mcp.ListToolsResult {
			return &mcp.ListToolsResult{NextCursor: fmt.Sprintf("c%d", call)}
		}, "pages"},
		{"new cursor and a new tool on every page", func(_ string, call int) *mcp.ListToolsResult {
			return &mcp.ListToolsResult{Tools: []*mcp.Tool{mkTool(fmt.Sprintf("t%d", call), 0)}, NextCursor: fmt.Sprintf("c%d", call)}
		}, "pages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, ctl := toolsServer(t, tc.answer)
			m := start(t, ts, &fakeStore{}, Options{MaxPages: 5, ConnectTimeout: 3 * time.Second})
			st, _ := m.State("s1")
			if st.Status != StatusDown {
				t.Fatalf("server is %q (%q), want %q", st.Status, st.Error, StatusDown)
			}
			if !strings.Contains(st.Error, "tools/list") || !strings.Contains(st.Error, tc.want) {
				t.Errorf("error %q should name tools/list and say %q", st.Error, tc.want)
			}
			if n := ctl.calls.Load(); n > 6 {
				t.Errorf("the gateway asked for %d pages, the limit is 5", n)
			}
			if items := m.Items("o"); len(items) != 0 {
				t.Errorf("a server with an endless list offers %d items, want none", len(items))
			}
		})
	}
}

// The limits must not get in the way of a list that ends: three pages, and
// exactly as many pages as the limit allows.
func TestPaginationWithinLimitsStaysUp(t *testing.T) {
	for _, pages := range []int{1, 3, 5} {
		t.Run(fmt.Sprintf("%d pages", pages), func(t *testing.T) {
			ts, _ := toolsServer(t, func(_ string, call int) *mcp.ListToolsResult {
				res := &mcp.ListToolsResult{Tools: []*mcp.Tool{mkTool(fmt.Sprintf("a%d", call), 0), mkTool(fmt.Sprintf("b%d", call), 0)}}
				if call < pages {
					res.NextCursor = fmt.Sprintf("c%d", call)
				}
				return res
			})
			m := start(t, ts, &fakeStore{}, Options{MaxPages: 5})
			st, _ := m.State("s1")
			if st.Status != StatusUp || len(st.Items) != 2*pages {
				t.Fatalf("server is %q (%q) with %d items, want %q with %d", st.Status, st.Error, len(st.Items), StatusUp, 2*pages)
			}
		})
	}
}

// Every definition may be 64 KiB and the server may offer 2000 of them. The
// definitions of one load are held and written to the store, so together they
// have a budget.
func TestLoadStopsAtDefinitionBudget(t *testing.T) {
	ten := func(string, int) *mcp.ListToolsResult {
		res := &mcp.ListToolsResult{}
		for i := 0; i < 10; i++ {
			res.Tools = append(res.Tools, mkTool(fmt.Sprintf("t%d", i), 10_000))
		}
		return res
	}
	t.Run("over the budget", func(t *testing.T) {
		ts, _ := toolsServer(t, ten)
		m := start(t, ts, &fakeStore{}, Options{MaxLoadBytes: 50_000})
		st, _ := m.State("s1")
		if st.Status != StatusDown {
			t.Fatalf("server is %q with %d items, want %q", st.Status, len(st.Items), StatusDown)
		}
		if !strings.Contains(st.Error, "definitions") || !strings.Contains(st.Error, "50000") {
			t.Errorf("error %q should say that the definitions pass 50000 bytes", st.Error)
		}
	})
	t.Run("within the budget", func(t *testing.T) {
		ts, _ := toolsServer(t, ten)
		m := start(t, ts, &fakeStore{}, Options{MaxLoadBytes: 200_000})
		if st, _ := m.State("s1"); st.Status != StatusUp || len(st.Items) != 10 {
			t.Fatalf("server is %q (%q) with %d items, want %q with 10", st.Status, st.Error, len(st.Items), StatusUp)
		}
	})
}

// A refresh that hung kept the refresh slot for good: every later change of the
// server's list was folded into it and none ever ran. A refresh has the connect
// timeout as its deadline.
func TestRefreshHasADeadline(t *testing.T) {
	var extra atomic.Bool
	ts, ctl := toolsServer(t, func(string, int) *mcp.ListToolsResult {
		res := &mcp.ListToolsResult{Tools: []*mcp.Tool{mkTool("t1", 0)}}
		if extra.Load() {
			res.Tools = append(res.Tools, mkTool("t2", 0))
		}
		return res
	})
	m := start(t, ts, &fakeStore{}, Options{ConnectTimeout: 300 * time.Millisecond, RefreshGap: time.Millisecond})
	if st, _ := m.State("s1"); st.Status != StatusUp || len(st.Items) != 1 {
		t.Fatalf("server is %q (%q) with %d items, want %q with 1", st.Status, st.Error, len(st.Items), StatusUp)
	}
	m.mu.Lock()
	e := m.entries["s1"]
	m.mu.Unlock()
	session, err := m.Session("o", "s1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctl.hang.Store(true)
	m.scheduleLoad(ctx, e, session)
	waitFor(t, 3*time.Second, "a refresh that the server never answers still holds the refresh slot", func() bool { return !e.loading.Load() })

	ctl.hang.Store(false)
	extra.Store(true)
	m.scheduleLoad(ctx, e, session)
	waitFor(t, 3*time.Second, "the next refresh did not publish the changed list", func() bool {
		st, _ := m.State("s1")
		return len(st.Items) == 2
	})
}

// A server that announces a change as often as it likes gets one refresh per
// RefreshGap, not one per announcement: every refresh lists everything and
// writes to the store.
func TestRefreshGap(t *testing.T) {
	ts, ctl := toolsServer(t, func(string, int) *mcp.ListToolsResult {
		return &mcp.ListToolsResult{Tools: []*mcp.Tool{mkTool("t1", 0)}}
	})
	m := start(t, ts, &fakeStore{}, Options{RefreshGap: 300 * time.Millisecond})
	m.mu.Lock()
	e := m.entries["s1"]
	m.mu.Unlock()
	session, err := m.Session("o", "s1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for end := time.Now().Add(700 * time.Millisecond); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		m.scheduleLoad(ctx, e, session)
	}
	time.Sleep(100 * time.Millisecond)
	// The first load, then a refresh about every 300 ms.
	if n := ctl.calls.Load(); n < 2 || n > 4 {
		t.Errorf("the server was asked for its tools %d times in 0.8 s, want 2 to 4 (the first load and a refresh per 300 ms)", n)
	}
}

// After a load the pending pins that the server no longer offers are deleted
// (store.PrunePending), but only after a load that listed everything.
func TestLoadPrunesPendingPins(t *testing.T) {
	t.Run("a complete load", func(t *testing.T) {
		ts, _ := toolsServer(t, func(string, int) *mcp.ListToolsResult {
			return &mcp.ListToolsResult{Tools: []*mcp.Tool{mkTool("t1", 0), mkTool("t2", 0)}}
		})
		fs := &fakeStore{}
		m := start(t, ts, fs, Options{})
		st, _ := m.State("s1")
		if st.Status != StatusUp || len(st.Items) != 2 {
			t.Fatalf("server is %q (%q) with %d items", st.Status, st.Error, len(st.Items))
		}
		got := fs.pruned()
		if len(got) != 1 || got[0].org != "o" || got[0].serverID != "s1" {
			t.Fatalf("PrunePending calls: %+v, want one for org o, server s1", got)
		}
		want := map[store.PinKey]bool{}
		for _, it := range st.Items {
			want[store.PinKey{Kind: it.Kind, Name: it.Name, Hash: it.Hash}] = true
		}
		if len(got[0].keep) != len(want) {
			t.Fatalf("kept %v, want %v", got[0].keep, want)
		}
		for _, k := range got[0].keep {
			if !want[k] {
				t.Errorf("kept %v, which the server did not offer", k)
			}
		}
	})
	t.Run("a load that failed", func(t *testing.T) {
		ts := hostileServer(t, "prompts")
		fs := &fakeStore{}
		m := start(t, ts, fs, Options{})
		if st, _ := m.State("s1"); st.Status != StatusDown {
			t.Fatalf("server is %q, want %q", st.Status, StatusDown)
		}
		if got := fs.pruned(); len(got) != 0 {
			t.Errorf("pins were pruned after a load that did not finish: %+v", got)
		}
	})
}

func TestLimitBodies(t *testing.T) {
	const limit = 1024
	mux := http.NewServeMux()
	body := func(n int) []byte { return []byte(strings.Repeat("a", n)) }
	mux.HandleFunc("/exact", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body(limit)) })
	mux.HandleFunc("/over-by-one", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body(limit + 1)) })
	mux.HandleFunc("/chunked", func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i < 8; i++ { // no Content-Length: the server flushes between writes
			_, _ = w.Write(body(512))
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/gzip", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = zw.Write(body(100 * limit)) // a few hundred bytes on the wire
		_ = zw.Close()
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 8; i++ { // 8 KiB in all, each event just below the limit
			_, _ = fmt.Fprintf(w, "data: %s\n\n", body(1000))
			w.(http.Flusher).Flush()
		}
	})
	events := func(eol string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 0; i < 8; i++ { // 8 KiB in all, each event just below the limit
				_, _ = fmt.Fprintf(w, "data: %s%s%s", body(1000), eol, eol)
				w.(http.Flusher).Flush()
			}
		}
	}
	mux.HandleFunc("/events-crlf", events("\r\n"))
	mux.HandleFunc("/events-cr", events("\r"))
	mux.HandleFunc("/event-too-big", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: ok\n\ndata: %s\n\n", body(2*limit))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	c := limitBodies(&http.Client{Transport: &http.Transport{}}, limit)

	read := func(path string) ([]byte, error) {
		resp, err := c.Get(ts.URL + path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		return io.ReadAll(resp.Body)
	}
	if b, err := read("/exact"); err != nil || len(b) != limit {
		t.Errorf("a reply of exactly the limit: %d bytes, %v", len(b), err)
	}
	for _, path := range []string{"/events", "/events-crlf", "/events-cr"} {
		if b, err := read(path); err != nil || len(b) < 8000 {
			t.Errorf("%s: an event stream is capped per event, not as a whole: %d bytes, %v", path, len(b), err)
		}
	}
	for _, path := range []string{"/over-by-one", "/chunked", "/gzip", "/event-too-big"} {
		b, err := read(path)
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Errorf("%s: got %d bytes and error %v, want ErrResponseTooLarge", path, len(b), err)
		}
		allowed := limit
		if path == "/event-too-big" {
			allowed += len("data: ok\n\n") // the small event before it was complete
		}
		if len(b) > allowed {
			t.Errorf("%s: %d bytes were handed on, at most %d may be", path, len(b), allowed)
		}
	}
	// A reply that announces its size is refused before its body is read.
	mux.HandleFunc("/declared", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(10*limit))
		_, _ = w.Write(body(10 * limit))
	})
	if _, err := c.Get(ts.URL + "/declared"); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("a reply that announces %d bytes: error %v, want ErrResponseTooLarge", 10*limit, err)
	}
}

// The state machine that finds the end of an event must give the same answer
// however the bytes are cut into reads, and for every line ending.
func TestEventCapAcrossReads(t *testing.T) {
	const limit = 100
	event := func(n int, eol string) string { return "data: " + strings.Repeat("a", n) + eol + eol }
	for _, eol := range []string{"\n", "\r\n", "\r"} {
		ok := strings.Repeat(event(80, eol), 20) // 20 events, each below the limit, 2 KiB together
		bad := event(80, eol) + event(limit, eol) + event(5, eol)
		for name, wrap := range map[string]func(io.Reader) io.Reader{
			"whole reads":   func(r io.Reader) io.Reader { return r },
			"one byte each": iotestOneByte,
		} {
			read := func(s string) ([]byte, error) {
				rc := &eventCapBody{rc: io.NopCloser(wrap(strings.NewReader(s))), max: limit, atLineStart: true}
				return io.ReadAll(rc)
			}
			if b, err := read(ok); err != nil || string(b) != ok {
				t.Errorf("eol %q, %s: %d of %d bytes, error %v", eol, name, len(b), len(ok), err)
			}
			b, err := read(bad)
			if !errors.Is(err, ErrResponseTooLarge) {
				t.Errorf("eol %q, %s: an event of %d bytes was let through: %v", eol, name, 6+limit+len(eol)*2, err)
			}
			if len(b) > len(event(80, eol))+limit {
				t.Errorf("eol %q, %s: %d bytes were handed on", eol, name, len(b))
			}
		}
	}
}

// iotestOneByte hands out one byte per Read.
func iotestOneByte(r io.Reader) io.Reader { return oneByte{r} }

type oneByte struct{ r io.Reader }

func (o oneByte) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return o.r.Read(p[:1])
}
