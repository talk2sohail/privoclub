package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/gateway"
	"privo.club/privo/internal/store"
)

func TestResultScanSettingPermissionsAndAudit(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, owner := e.user(t, "boss", store.RoleOwner)
	_, admin := e.user(t, "alice", store.RoleAdmin)
	_, member := e.user(t, "ada", store.RoleMember)
	_, approver := e.user(t, "carol", store.RoleApprover)
	_, auditor := e.user(t, "otto", store.RoleAuditor)

	// Default is "flag" on both GET /policies and GET /settings/policy.
	pol := e.call(t, owner, http.MethodGet, "/api/v1/policies", nil, http.StatusOK)
	if pol["result_scan"] != gateway.ResultScanFlag {
		t.Fatalf("GET /policies result_scan = %v, want %q", pol["result_scan"], gateway.ResultScanFlag)
	}
	settings := e.call(t, owner, http.MethodGet, "/api/v1/settings/policy", nil, http.StatusOK)
	if settings["result_scan"] != gateway.ResultScanFlag || settings["default"] != store.ActionAllow {
		t.Fatalf("GET /settings/policy = %v", settings)
	}

	// Non-policy roles cannot read or change the setting.
	for name, p := range map[string]auth.Principal{
		"member":   member,
		"approver": approver,
		"auditor":  auditor,
	} {
		t.Run("forbidden/"+name, func(t *testing.T) {
			e.call(t, p, http.MethodGet, "/api/v1/settings/policy", nil, http.StatusForbidden)
			e.call(t, p, http.MethodPatch, "/api/v1/settings/policy", map[string]any{"result_scan": "block"}, http.StatusForbidden)
		})
	}

	// Gateway tokens are rejected at the HTTP boundary (401).
	gwTok := e.call(t, owner, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "agent", "role": "owner", "kind": "gateway"}, http.StatusCreated)["token"].(string)
	mux := http.NewServeMux()
	e.api.Routes(mux)
	reqBody, _ := json.Marshal(map[string]any{"result_scan": "block"})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/policy", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+gwTok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("gateway token PATCH /settings/policy = %d, want 401", rec.Code)
	}

	// Closed enum validation: invalid values or empty patch fail with 422.
	for _, bad := range []map[string]any{
		{},
		{"result_scan": ""},
		{"result_scan": "off"},
		{"result_scan": "allow"},
		{"result_scan": "warn"},
		{"default": "allow", "result_scan": "nope"},
	} {
		e.call(t, owner, http.MethodPatch, "/api/v1/settings/policy", bad, http.StatusUnprocessableEntity)
	}
	if cur, err := e.st.Setting(ctx, e.org, gateway.SettingResultScan, gateway.ResultScanFlag); err != nil || cur != gateway.ResultScanFlag {
		t.Fatalf("invalid PATCH mutated setting to %q (err=%v)", cur, err)
	}

	// Valid updates by owner and admin succeed, preserve default, and audit.
	res := e.call(t, owner, http.MethodPatch, "/api/v1/settings/policy", map[string]any{"result_scan": "block"}, http.StatusOK)
	if res["result_scan"] != gateway.ResultScanBlock || res["default"] != store.ActionAllow {
		t.Fatalf("PATCH block response = %v", res)
	}
	if pol2 := e.call(t, admin, http.MethodGet, "/api/v1/policies", nil, http.StatusOK); pol2["result_scan"] != gateway.ResultScanBlock {
		t.Fatalf("GET /policies after block = %v", pol2)
	}
	res2 := e.call(t, admin, http.MethodPatch, "/api/v1/settings/policy", map[string]any{"default": "ask", "result_scan": "flag"}, http.StatusOK)
	if res2["result_scan"] != gateway.ResultScanFlag || res2["default"] != store.ActionAsk {
		t.Fatalf("PATCH both response = %v", res2)
	}

	e.aw.Close()
	evs, err := e.st.RecentAdminEvents(ctx, e.org, 50)
	if err != nil {
		t.Fatal(err)
	}
	wantActions := map[string]bool{
		"policy.result_scan=block": false,
		"policy.result_scan=flag":  false,
		"policy.default=ask":       false,
	}
	for _, ev := range evs {
		if _, ok := wantActions[ev.Action]; ok {
			wantActions[ev.Action] = true
		}
	}
	for act, saw := range wantActions {
		if !saw {
			t.Errorf("missing audit event %q in %+v", act, evs)
		}
	}
}
