//go:build acceptance

package acceptance

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// F12 through the real binary: calls are sealed into a signed chain; the API
// and `privo audit verify` say it is intact; a row changed in the database is
// found by both.
func TestAuditChainEndToEnd(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	boss := ownerOf(t, g, gh)
	addReviewed(t, g, boss, map[string]any{"alias": "shared", "transport": "http", "url": plainMCPServer(t).URL + "/mcp"})
	_, token := join(t, g, gh, boss, "ada", 2)
	client := g.connect(token)
	for _, text := range []string{"one", "two", "three"} {
		if got, isErr := call(t, client, "shared__echo", map[string]any{"text": text}); isErr || got != text {
			t.Fatalf("call %s: %q", text, got)
		}
	}
	var report map[string]any
	g.eventually("everything is sealed and intact", 15*time.Second, func() bool {
		code, out := boss.app(http.MethodGet, "/v1/audit/integrity", nil, nil)
		report, _ = out["report"].(map[string]any)
		return code == http.StatusOK && report["ok"] == true && report["unsealed"] == float64(0) && report["events"].(float64) >= 6
	})
	if out, err := g.command("audit", "verify").CombinedOutput(); err != nil || !strings.Contains(string(out), `"ok": true`) {
		t.Fatalf("privo audit verify: %v\n%s", err, out)
	}
	// Search and export see the calls.
	_, found := boss.app(http.MethodGet, "/v1/audit/events?server=shared", nil, nil)
	if n := len(list(found, "events")); n != 3 {
		t.Fatalf("search found %d calls", n)
	}
	_, exp := boss.app(http.MethodGet, "/v1/audit/export?kind=calls&server=shared", nil, nil)
	if exp["rows"] != float64(3) {
		t.Fatalf("export %v", exp)
	}

	// Someone with the database rewrites a call.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(g.dataDir, "privo.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE audit_events SET decision = 'deny' WHERE action = 'tools/call' AND chain_seq IS NOT NULL AND phase = 'intent'
		AND id = (SELECT id FROM audit_events WHERE action = 'tools/call' AND phase = 'intent' ORDER BY chain_seq LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	_, out := boss.app(http.MethodGet, "/v1/audit/integrity", nil, nil)
	report, _ = out["report"].(map[string]any)
	if report["ok"] != false || !strings.Contains(report["problem"].(string), "changed") {
		t.Fatalf("the change was not found: %v", out)
	}
	if out, err := g.command("audit", "verify").CombinedOutput(); err == nil || !strings.Contains(string(out), "changed") {
		t.Fatalf("privo audit verify after the change: %v\n%s", err, out)
	}
}
