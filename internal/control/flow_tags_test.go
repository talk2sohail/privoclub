package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/token"
)

func TestServerAndToolFlowTagsCRUDAndAudit(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, owner := e.user(t, "boss", store.RoleOwner)
	_, admin := e.user(t, "carol", store.RoleAdmin)
	_, member := e.user(t, "ada", store.RoleMember)
	_, auditor := e.user(t, "eve", store.RoleAuditor)
	_, approver := e.user(t, "bob", store.RoleApprover)

	e.seedServer(t, "crm", nil)
	e.call(t, admin, http.MethodPost, "/api/v1/teams", map[string]any{"slug": "sales", "name": "Sales"}, http.StatusCreated)
	e.seedServer(t, "restricted", []string{"sales"})

	// Empty initially.
	initial := e.call(t, owner, http.MethodGet, "/api/v1/servers/crm/flow-tags", nil, http.StatusOK)
	if initial["server"] != "crm" || len(initial["tags"].([]any)) != 0 {
		t.Fatalf("initial GET = %v", initial)
	}

	// Non-managers cannot mutate flow tags.
	putBody := map[string]any{
		"tags": []map[string]any{
			{"tool_name": "", "tag": "untrusted_source"},
			{"tool_name": "export_csv", "tag": "egress_sink"},
		},
	}
	for _, p := range []auth.Principal{member, auditor, approver} {
		e.call(t, p, http.MethodPut, "/api/v1/servers/crm/flow-tags", putBody, http.StatusForbidden)
	}

	// Admin and owner can update flow tags.
	res := e.call(t, admin, http.MethodPut, "/api/v1/servers/crm/flow-tags", putBody, http.StatusOK)
	if len(res["tags"].([]any)) != 2 {
		t.Fatalf("PUT result = %v", res)
	}

	// Readers with server reachability see tags; hidden server returns 404 to non-managers.
	e.call(t, member, http.MethodGet, "/api/v1/servers/crm/flow-tags", nil, http.StatusOK)
	e.call(t, auditor, http.MethodGet, "/api/v1/servers/crm/flow-tags", nil, http.StatusOK)
	e.call(t, approver, http.MethodGet, "/api/v1/servers/crm/flow-tags", nil, http.StatusOK)
	e.call(t, member, http.MethodGet, "/api/v1/servers/restricted/flow-tags", nil, http.StatusNotFound)

	// Narrowed toolset hides non-visible tool tags from members.
	e.call(t, admin, http.MethodPatch, "/api/v1/servers/crm/teams/everyone", map[string]any{"tools": []string{"read_lead"}}, http.StatusOK)
	memberTags := e.call(t, member, http.MethodGet, "/api/v1/servers/crm/flow-tags", nil, http.StatusOK)["tags"].([]any)
	if len(memberTags) != 1 || memberTags[0].(map[string]any)["tool_name"] != "" {
		t.Fatalf("member saw hidden tool tag: %v", memberTags)
	}

	// Validation errors.
	for _, bad := range []any{
		map[string]any{},
		map[string]any{"tags": []map[string]any{{"tool_name": "", "tag": "trusted"}}},
		map[string]any{"tags": []map[string]any{{"tool_name": "", "tag": ""}}},
		map[string]any{"tags": []map[string]any{{"tool_name": "bad\ntool", "tag": "egress_sink"}}},
		map[string]any{"tags": []map[string]any{{"tool_name": "   ", "tag": "egress_sink"}}},
	} {
		e.call(t, owner, http.MethodPut, "/api/v1/servers/crm/flow-tags", bad, http.StatusUnprocessableEntity)
	}
	e.call(t, owner, http.MethodPut, "/api/v1/servers/unknown/flow-tags", putBody, http.StatusNotFound)
	e.call(t, owner, http.MethodGet, "/api/v1/servers/unknown/flow-tags", nil, http.StatusNotFound)

	// Clearing tags with empty array works.
	cleared := e.call(t, owner, http.MethodPut, "/api/v1/servers/crm/flow-tags", map[string]any{"tags": []any{}}, http.StatusOK)
	if len(cleared["tags"].([]any)) != 0 {
		t.Fatalf("expected cleared tags, got %v", cleared)
	}

	// Token-based HTTP auth path enforces KindControl and permissions.
	mux := http.NewServeMux()
	e.api.Routes(mux)
	gwTok, _ := token.New()
	ctlTok, _ := token.New()
	now := time.Now()
	if err := e.st.CreateToken(ctx, store.APIToken{
		OrgID: e.org, ID: gwTok.ID, Name: "gw", Role: store.RoleOwner, Kind: store.KindGateway,
		SecretHash: gwTok.SecretHash, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateToken(ctx, store.APIToken{
		OrgID: e.org, ID: ctlTok.ID, Name: "ctl", Role: store.RoleMember, Kind: store.KindControl,
		SecretHash: ctlTok.SecretHash, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	doHTTP := func(method, path, tok string, body any) int {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := doHTTP(http.MethodPut, "/api/v1/servers/crm/flow-tags", gwTok.Plain, putBody); code != http.StatusUnauthorized {
		t.Fatalf("gateway token PUT: %d, want 401", code)
	}
	if code := doHTTP(http.MethodPut, "/api/v1/servers/crm/flow-tags", ctlTok.Plain, putBody); code != http.StatusForbidden {
		t.Fatalf("member control token PUT: %d, want 403", code)
	}

	// Policy flow_window setting validation & readback.
	polList := e.call(t, owner, http.MethodGet, "/api/v1/policies", nil, http.StatusOK)
	if polList["flow_window"] != "10m0s" {
		t.Fatalf("default flow_window = %v, want 10m0s", polList["flow_window"])
	}
	winRes := e.call(t, owner, http.MethodPatch, "/api/v1/settings/policy", map[string]any{"flow_window": "5m"}, http.StatusOK)
	if winRes["flow_window"] != "5m0s" {
		t.Fatalf("updated flow_window = %v, want 5m0s", winRes)
	}
	for _, badWin := range []string{"", "9s", "25h", "-1m", "bogus"} {
		e.call(t, owner, http.MethodPatch, "/api/v1/settings/policy", map[string]any{"flow_window": badWin}, http.StatusUnprocessableEntity)
	}

	// Audit trail includes server.flow_tags:crm and policy.flow_window=5m0s.
	e.aw.Close()
	evs, _ := e.st.RecentAdminEvents(ctx, e.org, 50)
	var actions []string
	for _, ev := range evs {
		actions = append(actions, ev.Action+"|"+ev.Server)
	}
	for _, want := range []string{"server.flow_tags:crm|crm", "policy.flow_window=5m0s|"} {
		if !slices.Contains(actions, want) {
			t.Errorf("audit missing %q in %q", want, actions)
		}
	}
}

func TestFlowTagsOrgIsolationInAPI(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, owner := e.user(t, "boss", store.RoleOwner)
	e.seedServer(t, "crm", nil)
	e.call(t, owner, http.MethodPut, "/api/v1/servers/crm/flow-tags", map[string]any{
		"tags": []map[string]any{{"tool_name": "", "tag": "untrusted_source"}},
	}, http.StatusOK)

	other, err := e.st.EnsureOrg(ctx, "other-org", "Other Org")
	if err != nil {
		t.Fatal(err)
	}
	otherPrincipal := auth.Principal{OrgID: other.ID, Name: "other-boss", Roles: []string{store.RoleOwner}, Kind: KindSession, Person: "other", UserID: ids.New()}
	e.call(t, otherPrincipal, http.MethodGet, "/api/v1/servers/crm/flow-tags", nil, http.StatusNotFound)
	e.call(t, otherPrincipal, http.MethodPut, "/api/v1/servers/crm/flow-tags", map[string]any{"tags": []any{}}, http.StatusNotFound)
}

func TestFlowApprovalRefusesGrant(t *testing.T) {
	e := newApprovalsEnv(t)
	e.accept(t, "post_message")
	out, err := e.gate.Gate(context.Background(), approval.Request{
		Org: e.org, RequesterID: e.adaTok, RequesterName: "ada-laptop", RequesterPerson: "ada",
		ServerID: "srv1", ServerAlias: "github", Tool: "post_message", ArgsDigest: "d-flow",
		Args: json.RawMessage(`{"text":"hi"}`), Reason: "cross-server flow: read from crm/read 2s ago", PinHash: "h-post_message",
	})
	if err != nil || out.Result != approval.Pending {
		t.Fatalf("Gate = %+v, %v", out, err)
	}
	res := e.call(t, e.appr, http.MethodPost, "/api/v1/approvals/"+out.ID+"/approve", map[string]any{"grant": "1h"}, http.StatusConflict)
	if msg, _ := res["error"].(string); !strings.Contains(msg, "cross-server flow") {
		t.Fatalf("expected cross-server flow explanation, got %v", res)
	}
	// Plain one-shot approval still succeeds.
	e.call(t, e.appr, http.MethodPost, "/api/v1/approvals/"+out.ID+"/approve", nil, http.StatusOK)
}
