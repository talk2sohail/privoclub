//go:build acceptance

package acceptance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func freshToolsMap(t *testing.T, g *gateway) map[string]*mcp.Tool {
	t.Helper()
	s := g.connect(g.agent)
	res, err := s.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	out := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tl := range res.Tools {
		out[tl.Name] = tl
	}
	return out
}

func pinMap(t *testing.T, g *gateway, alias string) map[string]map[string]any {
	t.Helper()
	raw := g.api(http.MethodGet, "/api/v1/servers/"+alias+"/pins", g.owner, nil, http.StatusOK)
	pins, _ := raw["pins"].([]any)
	out := make(map[string]map[string]any, len(pins))
	for _, p := range pins {
		m, _ := p.(map[string]any)
		key, _ := m["name"].(string)
		if st, _ := m["status"].(string); st != "" {
			out[key+"|"+st] = m
		}
		out[key] = m
	}
	return out
}

// F09 B: admin overrides for tool annotations reach MCP clients, feed policy
// tightening and presets, keep upstream's original claims visible, audit every
// change, and stay bound to the accepted pin hash across definition changes.
func TestAnnotationOverridesEndToEnd(t *testing.T) {
	var version atomic.Int32
	version.Store(1)

	srvV1 := mcp.NewServer(&mcp.Implementation{Name: "mutable", Version: "1.0.0"}, nil)
	mcp.AddTool(srvV1, &mcp.Tool{Name: "probe", Description: "Probe v1", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "v1"}}}, nil, nil
		})
	srvV2 := mcp.NewServer(&mcp.Implementation{Name: "mutable", Version: "2.0.0"}, nil)
	mcp.AddTool(srvV2, &mcp.Tool{Name: "probe", Description: "Probe v2 (changed)", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "v2"}}}, nil, nil
		})
	upTS := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		if version.Load() == 2 {
			return srvV2
		}
		return srvV1
	}, &mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(upTS.Close)

	g := startGateway(t, "PRIVO_EGRESS_ALLOW_CIDRS=127.0.0.0/8")
	g.registerEcho("notes", []string{"-mode", "annotated"}, nil)
	g.acceptPins("notes")

	// Wait until tools are listed on the gateway.
	g.eventually("notes tools listed", 5*time.Second, func() bool {
		return len(freshToolsMap(t, g)) == 2
	})

	pins := pinMap(t, g, "notes")
	readPin := pins["read_note"]
	deletePin := pins["delete_note"]
	readHash, _ := readPin["hash"].(string)
	deleteHash, _ := deletePin["hash"].(string)

	// Upstream's original annotations are returned on GET /pins.
	if upAnn, _ := readPin["annotations"].(map[string]any); upAnn["readOnlyHint"] != true {
		t.Fatalf("read_note upstream annotations = %v, want readOnlyHint=true", upAnn)
	}
	if upOv, _ := readPin["annotation_overrides"].(map[string]any); len(upOv) != 0 {
		t.Fatalf("read_note initial overrides = %v, want empty", upOv)
	}

	// Non-admin tokens cannot modify annotation overrides.
	memberGW := g.issue("ada")
	auditorTok := g.issueControl("eve", "auditor", "eve")
	approverTok := g.issueControl("bob", "approver", "bob")
	patchBody := map[string]any{"name": "read_note", "hash": readHash, "readOnlyHint": false, "destructiveHint": true, "openWorldHint": false}
	if code, _ := g.raw(http.MethodPatch, "/api/v1/servers/notes/pins/annotations", memberGW, patchBody); code != http.StatusUnauthorized {
		t.Fatalf("gateway token PATCH status = %d, want 401", code)
	}
	for role, tok := range map[string]string{"auditor": auditorTok, "approver": approverTok} {
		if code, _ := g.raw(http.MethodPatch, "/api/v1/servers/notes/pins/annotations", tok, patchBody); code != http.StatusForbidden {
			t.Fatalf("%s control token PATCH status = %d, want 403", role, code)
		}
	}

	// Owner overrides read_note to destructive and delete_note to read-only.
	g.api(http.MethodPatch, "/api/v1/servers/notes/pins/annotations", g.owner, patchBody, http.StatusOK)
	g.api(http.MethodPatch, "/api/v1/servers/notes/pins/annotations", g.owner, map[string]any{
		"name": "delete_note", "hash": deleteHash, "readOnlyHint": true, "destructiveHint": false,
	}, http.StatusOK)

	// Clients receive the overridden annotations on tools/list.
	tools := freshToolsMap(t, g)
	rn := tools["notes__read_note"]
	dn := tools["notes__delete_note"]
	if rn == nil || rn.Annotations == nil || rn.Annotations.ReadOnlyHint ||
		rn.Annotations.DestructiveHint == nil || !*rn.Annotations.DestructiveHint ||
		rn.Annotations.OpenWorldHint == nil || *rn.Annotations.OpenWorldHint {
		t.Fatalf("client read_note annotations = %+v", rn)
	}
	if dn == nil || dn.Annotations == nil || !dn.Annotations.ReadOnlyHint ||
		dn.Annotations.DestructiveHint == nil || *dn.Annotations.DestructiveHint {
		t.Fatalf("client delete_note annotations = %+v", dn)
	}

	// Default allow tightens overridden destructive read_note -> Approval required,
	// while delete_note (now readOnlyHint=true, destructiveHint=false) runs.
	s := g.connect(g.agent)
	if text, isErr := call(t, s, "notes__read_note", nil); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("overridden read_note call = %q (err=%v), want Approval required", text, isErr)
	}
	if text, isErr := call(t, s, "notes__delete_note", nil); isErr || text != "deleted" {
		t.Fatalf("overridden delete_note call = %q (err=%v), want deleted", text, isErr)
	}

	// Presets read effective (overridden) annotations: read-only denies read_note and allows delete_note.
	g.api(http.MethodPost, "/api/v1/servers/notes/policy:preset", g.owner, map[string]any{"preset": "read-only"}, http.StatusCreated)
	if text, isErr := call(t, s, "notes__read_note", nil); !isErr || !strings.Contains(text, "Denied") {
		t.Fatalf("read-only preset on overridden read_note = %q (err=%v), want Denied", text, isErr)
	}
	if text, isErr := call(t, s, "notes__delete_note", nil); isErr || text != "deleted" {
		t.Fatalf("read-only preset on overridden delete_note = %q (err=%v), want deleted", text, isErr)
	}

	// Admin pin list STILL shows upstream's original annotations alongside active overrides.
	pins = pinMap(t, g, "notes")
	if upAnn, _ := pins["read_note"]["annotations"].(map[string]any); upAnn["readOnlyHint"] != true {
		t.Fatalf("upstream annotations mutated on GET /pins: %v", upAnn)
	}
	if ov, _ := pins["read_note"]["annotation_overrides"].(map[string]any); ov["readOnlyHint"] != false || ov["destructiveHint"] != true || ov["openWorldHint"] != false {
		t.Fatalf("GET /pins overrides = %v", ov)
	}

	// Explicit null clears readOnlyHint and destructiveHint back to upstream's values while keeping openWorldHint.
	g.api(http.MethodPatch, "/api/v1/servers/notes/pins/annotations", g.owner, map[string]any{
		"name": "read_note", "hash": readHash, "readOnlyHint": nil, "destructiveHint": nil,
	}, http.StatusOK)
	tools = freshToolsMap(t, g)
	rn = tools["notes__read_note"]
	if !rn.Annotations.ReadOnlyHint || rn.Annotations.DestructiveHint != nil || rn.Annotations.OpenWorldHint == nil || *rn.Annotations.OpenWorldHint {
		t.Fatalf("after null clear read_note annotations = %+v", rn.Annotations)
	}

	// Audit log records alias, tool, hash, and before/after overrides.
	g.eventually("pin.annotations audited", 5*time.Second, func() bool {
		evResp := g.api(http.MethodGet, "/api/v1/audit/events?kind=admin&action=pin.annotations:notes:read_note", g.owner, nil, http.StatusOK)
		evs, _ := evResp["events"].([]any)
		for _, raw := range evs {
			m, _ := raw.(map[string]any)
			out, _ := m["outcome"].(string)
			if m["server"] == "notes" && strings.Contains(out, "hash="+readHash) && strings.Contains(out, "before=") && strings.Contains(out, "after=") {
				return true
			}
		}
		return false
	})

	// Changed definition (new hash) never inherits prior overrides.
	g.api(http.MethodPost, "/api/v1/servers", g.owner, map[string]any{
		"alias": "mutable", "transport": "http", "url": upTS.URL + "/mcp",
	}, http.StatusCreated)
	g.acceptPins("mutable")
	mPins := pinMap(t, g, "mutable")
	h1, _ := mPins["probe"]["hash"].(string)
	g.api(http.MethodPatch, "/api/v1/servers/mutable/pins/annotations", g.owner, map[string]any{
		"name": "probe", "hash": h1, "readOnlyHint": false, "destructiveHint": true,
	}, http.StatusOK)
	if text, isErr := call(t, s, "mutable__probe", nil); !isErr || !strings.Contains(text, "Approval required") {
		t.Fatalf("mutable v1 overridden destructive call = %q (err=%v), want Approval required", text, isErr)
	}

	// Switch upstream to v2 and restart connection so the new definition loads.
	version.Store(2)
	g.api(http.MethodPatch, "/api/v1/servers/mutable", g.owner, map[string]any{"enabled": false}, http.StatusOK)
	g.api(http.MethodPatch, "/api/v1/servers/mutable", g.owner, map[string]any{"enabled": true}, http.StatusOK)
	var h2 string
	g.eventually("mutable v2 pending pin seen", 10*time.Second, func() bool {
		raw := g.api(http.MethodGet, "/api/v1/servers/mutable/pins", g.owner, nil, http.StatusOK)
		for _, item := range raw["pins"].([]any) {
			m, _ := item.(map[string]any)
			if m["name"] == "probe" && m["status"] == "pending" && m["hash"] != h1 {
				h2, _ = m["hash"].(string)
				return true
			}
		}
		return false
	})
	g.api(http.MethodPost, "/api/v1/servers/mutable/pins:accept", g.owner, map[string]any{
		"pins": []map[string]any{{"kind": "tool", "name": "probe", "hash": h2}},
	}, http.StatusOK)

	// Accepted v2 must have NO inherited overrides from h1.
	tools = freshToolsMap(t, g)
	pr := tools["mutable__probe"]
	if pr == nil || pr.Annotations == nil || !pr.Annotations.ReadOnlyHint || pr.Annotations.DestructiveHint != nil {
		t.Fatalf("mutable v2 inherited h1 overrides: %+v", pr)
	}
	if text, isErr := call(t, s, "mutable__probe", nil); isErr || text != "v2" {
		t.Fatalf("mutable v2 call = %q (err=%v), want v2", text, isErr)
	}
}
