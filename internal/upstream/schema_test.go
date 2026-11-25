package upstream

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
)

// remoteRef is an input schema Privo cannot read: its $ref names a URL.
var remoteRef = map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"$ref": "https://e.x/s.json"}}}

// F02: a tool whose input schema Privo cannot read is refused on every call.
// Its server's load puts a warning on Health, so a tool accepted before
// arguments were checked shows up when the server connects, not on its first
// refused call. A reload of the same list does not repeat the warning; a
// changed definition (accepted again) is warned about; past the cap the rest
// are counted.
func TestUnreadableSchemaWarnedAtLoad(t *testing.T) {
	remote := remoteRef
	var version atomic.Int32
	var many atomic.Bool
	ts, _ := toolsServer(t, func(string, int) *mcp.ListToolsResult {
		bad := &mcp.Tool{Name: "bad", Description: fmt.Sprint("v", version.Load()), InputSchema: remote}
		tools := []*mcp.Tool{mkTool("good", 0), bad}
		if many.Load() {
			for i := range maxSchemaWarnings + 3 {
				tools = append(tools, &mcp.Tool{Name: fmt.Sprint("bad", i), InputSchema: remote})
			}
		}
		return &mcp.ListToolsResult{Tools: tools}
	})
	sink := &diagSink{}
	// The recorder folds the same message within its repeat window; a
	// nanosecond leaves the manager's own once-per-definition rule to test.
	rec := telemetry.New(sink, telemetry.Options{FlushInterval: 10 * time.Millisecond, RepeatWindow: time.Nanosecond})
	m := start(t, ts, &fakeStore{acceptSeen: true}, Options{Diagnostics: rec, RefreshGap: time.Millisecond})
	m.mu.Lock()
	e := m.entries["s1"]
	m.mu.Unlock()
	session, err := m.Session("o", "s1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := func() {
		t.Helper()
		before := e.lastLoad.Load()
		m.scheduleLoad(ctx, e, session)
		waitFor(t, 3*time.Second, "the reload ends", func() bool { return e.lastLoad.Load() != before && !e.loading.Load() })
	}
	warnings := func() []string {
		t.Helper()
		time.Sleep(50 * time.Millisecond) // the recorder flushes every 10 ms
		sink.mu.Lock()
		defer sink.mu.Unlock()
		var out []string
		for _, d := range sink.ds {
			if strings.Contains(d.Message, "input schema") {
				if d.Level != store.LevelWarn || d.Category != store.CategoryUpstream || d.Server != "up" {
					t.Errorf("warning: %+v", d)
				}
				out = append(out, d.Message)
			}
		}
		return out
	}

	if w := warnings(); len(w) != 1 || !strings.Contains(w[0], `tool "bad"`) {
		t.Fatalf("after the first load: %q", w)
	}
	reload()
	if w := warnings(); len(w) != 1 {
		t.Fatalf("a reload of the same list warned again: %q", w)
	}
	version.Store(1) // a new definition of the same tool
	reload()
	if w := warnings(); len(w) != 2 {
		t.Fatalf("a changed definition was not warned about: %q", w)
	}
	many.Store(true)
	reload()
	w := warnings()
	if len(w) != 2+maxSchemaWarnings+1 || !strings.Contains(w[len(w)-1], "3 more tools") {
		t.Fatalf("one load wrote %d warnings, want %d and a count: %q", len(w)-2, maxSchemaWarnings+1, w[2:])
	}
}

// A pending definition is not read at load: the server could change its
// definitions on every refresh and make Privo compile each one. The review
// shows the problem before anyone accepts it.
func TestPendingUnreadableSchemaIsNotReadAtLoad(t *testing.T) {
	ts, _ := toolsServer(t, func(string, int) *mcp.ListToolsResult {
		return &mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "bad", InputSchema: remoteRef}}}
	})
	sink := &diagSink{}
	rec := telemetry.New(sink, telemetry.Options{FlushInterval: 10 * time.Millisecond})
	m := start(t, ts, &fakeStore{}, Options{Diagnostics: rec})
	if st, _ := m.State("s1"); st.Status != StatusUp {
		t.Fatalf("server is %q (%q)", st.Status, st.Error)
	}
	m.mu.Lock()
	e := m.entries["s1"]
	m.mu.Unlock()
	e.mu.Lock()
	read := len(e.schemas)
	e.mu.Unlock()
	rec.Close()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, d := range sink.ds {
		if strings.Contains(d.Message, "input schema") {
			t.Fatalf("a pending definition was warned about: %+v", d)
		}
	}
	if read != 0 {
		t.Fatalf("%d pending definitions were read", read)
	}
}
