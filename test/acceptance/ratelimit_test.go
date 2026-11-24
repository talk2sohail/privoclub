//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func postMCPRequest(t *testing.T, url, bearer, body string) (int, http.Header, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func TestRateLimitAndBulkheadUnderLoad(t *testing.T) {
	t.Run("rate limit burst triggers 429 with Retry-After and recovers", func(t *testing.T) {
		g := startGateway(t,
			"PRIVO_RATE_LIMIT_TOKEN_RPM=60",
			"PRIVO_RATE_LIMIT_USER_RPM=1000",
			"PRIVO_RATE_LIMIT_ORG_RPM=1000",
		)
		const ping = `{"jsonrpc":"2.0","id":1,"method":"ping"}`

		got429 := false
		for i := range 80 {
			code, hdr, body := postMCPRequest(t, g.base+"/mcp", g.agent, ping)
			if code == http.StatusOK {
				continue
			}
			if code != http.StatusTooManyRequests {
				t.Fatalf("req %d: unexpected status %d: %s", i, code, body)
			}
			ra := hdr.Get("Retry-After")
			secs, err := strconv.Atoi(ra)
			if err != nil || secs < 1 {
				t.Fatalf("429 Retry-After = %q (err=%v), want integer >= 1", ra, err)
			}
			if strings.Contains(body, g.agent) {
				t.Fatalf("429 body leaked token: %q", body)
			}
			got429 = true
			break
		}
		if !got429 {
			t.Fatal("burst did not trigger 429")
		}

		// After waiting one Retry-After interval (1s for 60 RPM), the next request succeeds.
		time.Sleep(1100 * time.Millisecond)
		code, _, body := postMCPRequest(t, g.base+"/mcp", g.agent, ping)
		if code != http.StatusOK {
			t.Fatalf("after Retry-After wait: status %d, want 200: %s", code, body)
		}

		g.eventually("rate_limited refusal stat recorded", 10*time.Second, func() bool {
			for _, c := range list(g.api(http.MethodGet, "/api/v1/telemetry/calls", g.owner, nil, http.StatusOK), "calls") {
				if c["outcome"] == "refused" && c["error_class"] == "rate_limited" {
					return true
				}
			}
			return false
		})
	})

	t.Run("bulkhead isolates slow upstream from healthy upstream", func(t *testing.T) {
		block := make(chan struct{})
		var closeBlock sync.Once
		t.Cleanup(func() { closeBlock.Do(func() { close(block) }) })

		slowEntered := make(chan struct{}, 10)
		slowSrv := mcp.NewServer(&mcp.Implementation{Name: "slow", Version: "0.0.1"}, nil)
		mcp.AddTool(slowSrv, &mcp.Tool{Name: "work", Description: "blocks"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			slowEntered <- struct{}{}
			select {
			case <-block:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "slow-done"}}}, nil, nil
		})
		slowTS := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return slowSrv }, nil))
		t.Cleanup(slowTS.Close)

		fastSrv := mcp.NewServer(&mcp.Implementation{Name: "fast", Version: "0.0.1"}, nil)
		mcp.AddTool(fastSrv, &mcp.Tool{Name: "ping", Description: "fast"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fast-ok"}}}, nil, nil
		})
		fastTS := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fastSrv }, nil))
		t.Cleanup(fastTS.Close)

		g := startGateway(t,
			"PRIVO_EGRESS_ALLOW_CIDRS=127.0.0.1/32",
			"PRIVO_UPSTREAM_MAX_INFLIGHT=2",
			"PRIVO_RATE_LIMIT_TOKEN_RPM=1000",
			"PRIVO_RATE_LIMIT_USER_RPM=1000",
			"PRIVO_RATE_LIMIT_ORG_RPM=1000",
		)
		g.registerRemote("slow", "http", slowTS.URL+"/mcp")
		g.registerRemote("fast", "http", fastTS.URL+"/mcp")
		s := g.connect(g.agent)

		// Saturate slow's 2-slot bulkhead.
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				text, failed, _ := callText(s, "slow__work", map[string]any{}, 15*time.Second)
				if failed || text != "slow-done" {
					t.Errorf("in-flight slow call: text=%q failed=%v", text, failed)
				}
			}()
		}
		for range 2 {
			select {
			case <-slowEntered:
			case <-time.After(10 * time.Second):
				t.Fatal("timed out waiting for slow calls to enter upstream")
			}
		}

		// 3rd call to slow is shed immediately by the bulkhead.
		text, failed, took := callText(s, "slow__work", map[string]any{}, 5*time.Second)
		if !failed || took > 2*time.Second {
			t.Fatalf("3rd slow call should fail fast on bulkhead: text=%q failed=%v took=%s", text, failed, took)
		}

		// Fast server stays completely responsive while slow's bulkhead is saturated.
		for range 3 {
			ft, ff, ftTook := callText(s, "fast__ping", map[string]any{}, 5*time.Second)
			if ff || ft != "fast-ok" || ftTook > 2*time.Second {
				t.Fatalf("healthy server degraded: text=%q failed=%v took=%s", ft, ff, ftTook)
			}
		}

		closeBlock.Do(func() { close(block) })
		wg.Wait()

		g.eventually("bulkhead in_flight_limit stat recorded", 10*time.Second, func() bool {
			for _, c := range list(g.api(http.MethodGet, "/api/v1/telemetry/calls?server=slow", g.owner, nil, http.StatusOK), "calls") {
				if c["outcome"] == "refused" && c["error_class"] == "in_flight_limit" {
					return true
				}
			}
			return false
		})
	})

	t.Run("global and org in-flight limits shed with 503", func(t *testing.T) {
		block := make(chan struct{})
		var closeBlock sync.Once
		t.Cleanup(func() { closeBlock.Do(func() { close(block) }) })
		entered := make(chan struct{}, 2)

		slowSrv := mcp.NewServer(&mcp.Implementation{Name: "slow", Version: "0.0.1"}, nil)
		mcp.AddTool(slowSrv, &mcp.Tool{Name: "work", Description: "blocks"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			entered <- struct{}{}
			select {
			case <-block:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
		})
		slowTS := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return slowSrv }, nil))
		t.Cleanup(slowTS.Close)

		g := startGateway(t,
			"PRIVO_EGRESS_ALLOW_CIDRS=127.0.0.1/32",
			"PRIVO_MAX_INFLIGHT=2",
			"PRIVO_ORG_MAX_INFLIGHT=1",
			"PRIVO_RATE_LIMIT_TOKEN_RPM=1000",
			"PRIVO_RATE_LIMIT_USER_RPM=1000",
			"PRIVO_RATE_LIMIT_ORG_RPM=1000",
		)
		g.registerRemote("slow", "http", slowTS.URL+"/mcp")
		s := g.connect(g.agent)

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = callText(s, "slow__work", map[string]any{}, 10*time.Second)
		}()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for slow call")
		}

		// Second concurrent request exceeds PRIVO_ORG_MAX_INFLIGHT=1 -> 503 with Retry-After: 1.
		code, hdr, body := postMCPRequest(t, g.base+"/mcp", g.agent, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
		if code != http.StatusServiceUnavailable || hdr.Get("Retry-After") != "1" {
			t.Fatalf("in-flight shed: status=%d Retry-After=%q body=%s, want 503 Retry-After=1", code, hdr.Get("Retry-After"), body)
		}

		closeBlock.Do(func() { close(block) })
		wg.Wait()

		// Once in-flight drains, requests succeed again.
		code, _, body = postMCPRequest(t, g.base+"/mcp", g.agent, `{"jsonrpc":"2.0","id":2,"method":"ping"}`)
		if code != http.StatusOK {
			t.Fatalf("after drain: status=%d body=%s, want 200", code, body)
		}
	})

	t.Run("failing upstream trips circuit breaker and recovers after cooldown", func(t *testing.T) {
		var failing atomic.Bool
		var hits atomic.Int64

		flakySrv := mcp.NewServer(&mcp.Implementation{Name: "flaky", Version: "0.0.1"}, nil)
		mcp.AddTool(flakySrv, &mcp.Tool{Name: "ping", Description: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			hits.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
		})
		base := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return flakySrv }, nil)
		flakyTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if failing.Load() {
				hits.Add(1)
				http.Error(w, "upstream down", http.StatusServiceUnavailable)
				return
			}
			base.ServeHTTP(w, r)
		}))
		t.Cleanup(flakyTS.Close)

		metricsPort := freePort(t)
		g := startGateway(t,
			"PRIVO_EGRESS_ALLOW_CIDRS=127.0.0.1/32",
			"PRIVO_CIRCUIT_BREAKER_THRESHOLD=3",
			"PRIVO_CIRCUIT_BREAKER_COOLDOWN=1500ms",
			"PRIVO_RATE_LIMIT_TOKEN_RPM=1000",
			"PRIVO_RATE_LIMIT_USER_RPM=1000",
			"PRIVO_RATE_LIMIT_ORG_RPM=1000",
			fmt.Sprintf("PRIVO_METRICS_ADDR=127.0.0.1:%d", metricsPort),
		)
		g.registerRemote("flaky", "http", flakyTS.URL+"/mcp")
		s := g.connect(g.agent)

		if text, failed, _ := callText(s, "flaky__ping", map[string]any{}, 5*time.Second); failed || text != "pong" {
			t.Fatalf("initial healthy call: text=%q failed=%v", text, failed)
		}

		// 3 consecutive 503s trip the breaker Open.
		failing.Store(true)
		for i := range 3 {
			if text, failed, _ := callText(s, "flaky__ping", map[string]any{}, 5*time.Second); !failed {
				t.Fatalf("failure call %d unexpectedly succeeded: %q", i, text)
			}
		}

		// Check Prometheus metric shows breaker state Open (1) with no org label.
		scrape := func() string {
			resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", metricsPort))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			b, _ := io.ReadAll(resp.Body)
			return string(b)
		}
		if m := scrape(); !strings.Contains(m, `privo_upstream_breaker_state{server="flaky"} 1`) {
			t.Fatalf("metrics missing open breaker state:\n%s", m)
		} else if strings.Contains(m, `org="`) || strings.Contains(m, `org_id="`) {
			t.Fatalf("metrics contain banned org label:\n%s", m)
		}

		// Even once upstream recovers, calls during cooldown are refused locally without hitting upstream.
		failing.Store(false)
		hitsBefore := hits.Load()
		text, failed, _ := callText(s, "flaky__ping", map[string]any{}, 5*time.Second)
		if !failed {
			t.Fatalf("call during open cooldown succeeded: %q", text)
		}
		if hits.Load() != hitsBefore {
			t.Fatalf("upstream was hit while circuit breaker was open (%d -> %d)", hitsBefore, hits.Load())
		}

		// Wait past cooldown -> HalfOpen probe succeeds -> breaker Closes.
		time.Sleep(1600 * time.Millisecond)
		if text, failed, _ := callText(s, "flaky__ping", map[string]any{}, 5*time.Second); failed || text != "pong" {
			t.Fatalf("half-open recovery call failed: text=%q failed=%v", text, failed)
		}
		if m := scrape(); !strings.Contains(m, `privo_upstream_breaker_state{server="flaky"} 0`) {
			t.Fatalf("metrics missing closed breaker state after recovery:\n%s", m)
		}

		g.eventually("circuit_open refusal stat recorded", 10*time.Second, func() bool {
			for _, c := range list(g.api(http.MethodGet, "/api/v1/telemetry/calls?server=flaky", g.owner, nil, http.StatusOK), "calls") {
				if c["outcome"] == "refused" && c["error_class"] == "circuit_open" {
					return true
				}
			}
			return false
		})
		g.eventually("breaker diagnostic logs recorded", 10*time.Second, func() bool {
			var opened, half, closed bool
			for _, l := range list(g.api(http.MethodGet, "/api/v1/telemetry/logs?category=upstream", g.owner, nil, http.StatusOK), "logs") {
				msg, _ := l["message"].(string)
				if strings.Contains(msg, "opened") {
					opened = true
				}
				if strings.Contains(msg, "half-open") {
					half = true
				}
				if strings.Contains(msg, "closed") {
					closed = true
				}
			}
			return opened && half && closed
		})
	})
}
