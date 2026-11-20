//go:build acceptance

package acceptance

import (
	"net/http"
	"slices"
	"testing"
	"time"
)

// ADR 0029 through the real binary: a server moved to another address
// offers nothing until its tools are reviewed again, and then serves from
// the new address.
func TestEditServerEndToEnd(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	boss := ownerOf(t, g, gh)
	addReviewed(t, g, boss, map[string]any{"alias": "kit", "transport": "http", "url": twoToolServer(t).URL + "/mcp"})
	_, adaToken := join(t, g, gh, boss, "ada", 2)
	ada := g.connect(adaToken)
	if got := toolList(t, ada); !slices.Equal(got, []string{"kit__echo", "kit__shout"}) {
		t.Fatalf("before the edit ada sees %v", got)
	}

	moved := twoToolServer(t)
	if code, out := boss.app(http.MethodPatch, "/v1/servers/kit", map[string]any{"url": moved.URL + "/mcp"}, nil); code != http.StatusOK {
		t.Fatalf("edit: %d %v", code, out)
	}
	// Nothing from the old address is trusted at the new one.
	g.eventually("kit offers nothing until reviewed again", 6*time.Second, func() bool { return len(toolList(t, ada)) == 0 })

	// The review shows the same tools waiting; accepting them serves them again.
	var pending []map[string]any
	g.eventually("the new address's tools wait for review", 6*time.Second, func() bool {
		_, out := boss.app(http.MethodGet, "/v1/servers/kit/pins", nil, nil)
		pending = nil
		for _, p := range list(out, "pins") {
			if p["status"] == "pending" {
				pending = append(pending, map[string]any{"kind": p["kind"], "name": p["name"], "hash": p["hash"]})
			}
		}
		return len(pending) == 2
	})
	if code, out := boss.app(http.MethodPost, "/v1/servers/kit/pins:accept", map[string]any{"pins": pending}, nil); code != http.StatusOK {
		t.Fatalf("accept: %d %v", code, out)
	}
	g.eventually("ada sees kit again", 6*time.Second, func() bool { return slices.Equal(toolList(t, ada), []string{"kit__echo", "kit__shout"}) })
	if text, isErr := call(t, ada, "kit__echo", map[string]any{"text": "moved"}); isErr || text != "moved" {
		t.Fatalf("a call after the move: %q %v", text, isErr)
	}
	// The edit is in the audit, with no values.
	_, evs := boss.app(http.MethodGet, "/v1/audit/events?kind=admin&action=server.", nil, nil)
	var acts []string
	for _, e := range list(evs, "events") {
		acts = append(acts, e["action"].(string))
	}
	for _, want := range []string{"server.url", "server.review_again:2", "server.connections_ended:0"} {
		if !slices.Contains(acts, want) {
			t.Fatalf("audit %q misses %s", acts, want)
		}
	}
}
