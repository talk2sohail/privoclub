//go:build acceptance

package acceptance

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// F28, ADR 0024 on the real binary: calls leave statistics with their phases,
// the Activity endpoints read them, and Prometheus metrics answer on their own
// address only.
func TestActivityTelemetry(t *testing.T) {
	metricsPort := freePort(t)
	g := startGateway(t, fmt.Sprintf("PRIVO_METRICS_ADDR=127.0.0.1:%d", metricsPort))
	g.registerEcho("echo", nil, nil)
	g.acceptPins("echo")
	s := g.connect(g.agent)
	g.eventually("echo listed", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s), "echo__echo") })
	callEcho(t, s, "first")
	callEcho(t, s, "second")

	var calls []map[string]any
	g.eventually("two call statistics", 10*time.Second, func() bool {
		calls = list(g.api(http.MethodGet, "/api/v1/telemetry/calls", g.owner, nil, http.StatusOK), "calls")
		return len(calls) == 2
	})
	for _, c := range calls {
		if c["server"] != "echo" || c["item"] != "echo" || c["method"] != "tools/call" || c["outcome"] != "ok" {
			t.Fatalf("statistic: %v", c)
		}
		if c["total_ms"].(float64) <= 0 || c["upstream_ms"].(float64) <= 0 || c["result_bytes"].(float64) <= 0 || c["args_bytes"].(float64) <= 0 {
			t.Fatalf("phases and sizes: %v", c)
		}
		if _, ok := c["arguments"]; ok {
			t.Fatalf("a statistic holds the arguments: %v", c)
		}
	}
	// The statistic and the audit share the call id.
	audited := callEvents(g)
	if len(audited) != 2 || (audited[0]["id"] != calls[0]["id"] && audited[0]["id"] != calls[1]["id"]) {
		t.Fatalf("audit %v and statistics %v do not share call ids", audited, calls)
	}

	sum := g.api(http.MethodGet, "/api/v1/telemetry/summary?window=1h&by=tool", g.owner, nil, http.StatusOK)
	if total := sum["total"].(map[string]any); total["calls"].(float64) != 2 || total["errors"].(float64) != 0 {
		t.Fatalf("summary: %v", sum)
	}
	// Minutes from the one holding now-1h to the one holding now: 60, or 61
	// when now is not on a minute (T-09: the counts are the window's own).
	if series := g.api(http.MethodGet, "/api/v1/telemetry/timeseries?window=1h", g.owner, nil, http.StatusOK); len(list(series, "buckets")) < 60 || len(list(series, "buckets")) > 61 {
		t.Fatalf("a one-hour series in minutes: %d buckets", len(list(series, "buckets")))
	} else {
		var n float64
		for _, b := range list(series, "buckets") {
			n += b["calls"].(float64)
		}
		if n != 2 {
			t.Fatalf("the chart counts %v calls, the summary 2", n)
		}
	}
	logs := list(g.api(http.MethodGet, "/api/v1/telemetry/logs?category=lifecycle", g.owner, nil, http.StatusOK), "logs")
	if len(logs) == 0 || !strings.Contains(logs[0]["message"].(string), "started") {
		t.Fatalf("lifecycle log: %v", logs)
	}
	if st := g.api(http.MethodGet, "/api/v1/telemetry/storage", g.owner, nil, http.StatusOK); st["recording"] != true || st["retention_days"].(float64) != 30 {
		t.Fatalf("storage: %v", st)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", metricsPort))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	for _, want := range []string{
		`privo_mcp_calls_total{server="echo",method="tools/call",outcome="ok"} 2`,
		`privo_mcp_call_duration_seconds_count{server="echo",method="tools/call"} 2`,
		`privo_upstream_up{server="echo"} 1`,
		"privo_audit_events_lost_total 0",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics lack %q:\n%s", want, body)
		}
	}
	// Never on the public port.
	if code, raw := g.raw(http.MethodGet, "/metrics", "", nil); code == http.StatusOK && strings.Contains(string(raw), "privo_mcp_calls_total") {
		t.Fatal("metrics are served on the public port")
	}
}

// Retention 0: nothing is recorded, the audit is untouched, and no metrics
// listener is opened unless asked for.
func TestTelemetryOff(t *testing.T) {
	g := startGateway(t, "PRIVO_TELEMETRY_RETENTION_DAYS=0")
	g.registerEcho("echo", nil, nil)
	g.acceptPins("echo")
	s := g.connect(g.agent)
	g.eventually("echo listed", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s), "echo__echo") })
	callEcho(t, s, "x")
	g.eventually("the call audited", 5*time.Second, func() bool { return len(callEvents(g)) == 1 })
	time.Sleep(1500 * time.Millisecond) // longer than the statistics flush
	if calls := list(g.api(http.MethodGet, "/api/v1/telemetry/calls", g.owner, nil, http.StatusOK), "calls"); len(calls) != 0 {
		t.Fatalf("statistics recorded while off: %v", calls)
	}
	if st := g.api(http.MethodGet, "/api/v1/telemetry/storage", g.owner, nil, http.StatusOK); st["recording"] != false || st["logs"].(float64) != 0 {
		t.Fatalf("storage while off: %v", st)
	}
	if strings.Contains(g.logs.String(), "metrics on") {
		t.Fatal("a metrics listener opened without PRIVO_METRICS_ADDR")
	}
}

// Review of 2026-11-15, T-04: switching statistics off deletes what was kept,
// so the API does not keep serving it.
func TestSwitchingStatisticsOffDeletesThem(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("echo", nil, nil)
	g.acceptPins("echo")
	s := g.connect(g.agent)
	g.eventually("echo listed", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s), "echo__echo") })
	callEcho(t, s, "x")
	g.eventually("the statistic kept", 10*time.Second, func() bool {
		return len(list(g.api(http.MethodGet, "/api/v1/telemetry/calls", g.owner, nil, http.StatusOK), "calls")) == 1
	})
	_ = s.Close()
	g.stop()
	g.env = append(g.env, "PRIVO_TELEMETRY_RETENTION_DAYS=0")
	g.start()
	g.eventually("the statistics gone", 10*time.Second, func() bool {
		st := g.api(http.MethodGet, "/api/v1/telemetry/storage", g.owner, nil, http.StatusOK)
		return st["recording"] == false && st["calls"].(float64) == 0 && st["logs"].(float64) == 0
	})
	if n := len(callEvents(g)); n != 1 {
		t.Fatalf("the audit changed: %d call events", n)
	}
}
