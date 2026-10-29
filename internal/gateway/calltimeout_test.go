package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/upstream"
)

// R-04: a call to an upstream has a deadline. Without one an upstream that
// accepts a call and never answers holds the caller, the audit row ("intent"
// without a completion) and a goroutine for as long as the client waits.
func TestCallUpstreamTimeout(t *testing.T) {
	c := call{item: upstream.Item{Alias: "slow"}, action: "tools/call"}
	waitForCancel := func(ctx context.Context, _ *mcp.ClientSession) (string, error) {
		<-ctx.Done()
		return "error", ctx.Err()
	}

	t.Run("an upstream that does not answer in time", func(t *testing.T) {
		g := &Gateway{CallTimeout: 50 * time.Millisecond}
		start := time.Now()
		outcome, err := g.callUpstream(context.Background(), c, nil, waitForCancel)
		if !errors.Is(err, errCallTimeout) {
			t.Fatalf("error %v, want errCallTimeout", err)
		}
		if outcome != "error" {
			t.Errorf("outcome %q, want %q", outcome, "error")
		}
		if !strings.Contains(err.Error(), `"slow"`) || !strings.Contains(err.Error(), "50ms") {
			t.Errorf("error %q should name the server and the limit", err)
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Errorf("took %s for a limit of 50ms", took)
		}
	})
	t.Run("a client that went away is not a timeout", func(t *testing.T) {
		g := &Gateway{CallTimeout: time.Minute}
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(20*time.Millisecond, cancel)
		_, err := g.callUpstream(ctx, c, nil, waitForCancel)
		if err == nil || errors.Is(err, errCallTimeout) || !errors.Is(err, context.Canceled) {
			t.Fatalf("error %v, want context.Canceled and not a timeout", err)
		}
	})
	t.Run("a call that answers in time is left alone", func(t *testing.T) {
		g := &Gateway{CallTimeout: time.Minute}
		outcome, err := g.callUpstream(context.Background(), c, nil, func(context.Context, *mcp.ClientSession) (string, error) { return "ok", nil })
		if outcome != "ok" || err != nil {
			t.Fatalf("got %q, %v", outcome, err)
		}
		own := errors.New("upstream said no")
		_, err = g.callUpstream(context.Background(), c, nil, func(context.Context, *mcp.ClientSession) (string, error) { return "error", own })
		if !errors.Is(err, own) || errors.Is(err, errCallTimeout) {
			t.Fatalf("an error before the deadline became %v", err)
		}
	})
	t.Run("an answer that arrives after the deadline is still an answer", func(t *testing.T) {
		g := &Gateway{CallTimeout: 10 * time.Millisecond}
		outcome, err := g.callUpstream(context.Background(), c, nil, func(context.Context, *mcp.ClientSession) (string, error) {
			time.Sleep(40 * time.Millisecond) // does not look at ctx
			return "ok", nil
		})
		if outcome != "ok" || err != nil {
			t.Fatalf("got %q, %v", outcome, err)
		}
	})
	t.Run("no limit configured: no deadline", func(t *testing.T) {
		g := &Gateway{}
		_, _ = g.callUpstream(context.Background(), c, nil, func(ctx context.Context, _ *mcp.ClientSession) (string, error) {
			if _, ok := ctx.Deadline(); ok {
				t.Error("a deadline was set although none is configured")
			}
			return "ok", nil
		})
	})
}
