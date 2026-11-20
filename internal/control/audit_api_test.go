package control

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// F12: search, export, integrity, checkpoints, keys, erasure and retention.

func seedCalls(t *testing.T, e *sessionEnv, n int, actorID string) {
	t.Helper()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%d", actorID, i)
		ev := store.AuditEvent{ID: "i-" + id, CallID: "c-" + id, Phase: "intent", Time: base.Add(time.Duration(i) * time.Second), Actor: "=cmd|calc", ActorID: actorID,
			Server: "github", Item: "merge", Action: "tools/call", Decision: "allow", Payload: []byte("sealed-arguments")}
		if err := e.st.InsertAudit(context.Background(), e.org, ev); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditSearchAPI(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	_, auditor := e.user(t, "otto", store.RoleAuditor)
	_, member := e.user(t, "mia", store.RoleMember)
	seedCalls(t, e, 5, "tok-ada")
	seedCalls(t, e, 2, "tok-bob")
	first := e.call(t, auditor, http.MethodGet, "/api/v1/audit/events?actor=tok-ada&limit=3", nil, http.StatusOK)
	if len(first["events"].([]any)) != 3 || first["next"] == "" {
		t.Fatalf("first page %v", first)
	}
	second := e.call(t, auditor, http.MethodGet, "/api/v1/audit/events?actor=tok-ada&limit=3&before="+first["next"].(string), nil, http.StatusOK)
	if len(second["events"].([]any)) != 2 || second["next"] != "" {
		t.Fatalf("second page %v", second)
	}
	e.call(t, auditor, http.MethodGet, "/api/v1/audit/events?from=yesterday", nil, http.StatusBadRequest)
	e.call(t, auditor, http.MethodGet, "/api/v1/audit/events?kind=secrets", nil, http.StatusBadRequest)
	e.call(t, member, http.MethodGet, "/api/v1/audit/events", nil, http.StatusForbidden)

	exp := e.call(t, auditor, http.MethodGet, "/api/v1/audit/export?server=github", nil, http.StatusOK)
	csv := exp["csv"].(string)
	if exp["rows"] != float64(7) || !strings.HasPrefix(csv, "time,call,actor") || strings.Contains(csv, "sealed-arguments") {
		t.Fatalf("export %v", exp)
	}
	if !strings.Contains(csv, "'=cmd|calc") {
		t.Fatalf("a formula reached the CSV:\n%s", csv)
	}
	e.call(t, member, http.MethodGet, "/api/v1/audit/export", nil, http.StatusForbidden)
	_ = owner
}

func TestAuditIntegrityAPI(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, auditor := e.user(t, "otto", store.RoleAuditor)
	_, owner := e.user(t, "boss", store.RoleOwner)
	e.call(t, auditor, http.MethodGet, "/api/v1/audit/integrity", nil, http.StatusServiceUnavailable)
	kek, _ := vault.NewKey()
	e.api.AuditKeys = vault.StaticKeyProvider(kek, nil)
	seedCalls(t, e, 4, "tok-ada")
	e.aw.Close() // flush the admin events of the setup
	sealer := &audit.Sealer{Store: e.st, Keys: e.api.AuditKeys, Org: e.org}
	if _, err := sealer.SealOnce(ctx); err != nil {
		t.Fatal(err)
	}
	res := e.call(t, auditor, http.MethodGet, "/api/v1/audit/integrity", nil, http.StatusOK)
	rep := res["report"].(map[string]any)
	if rep["ok"] != true || rep["blocks"] != float64(1) || res["retention_days"] != float64(0) {
		t.Fatalf("integrity %v", res)
	}
	keys := e.call(t, auditor, http.MethodGet, "/api/v1/audit/keys", nil, http.StatusOK)["keys"].([]any)
	cps := e.call(t, auditor, http.MethodGet, "/api/v1/audit/checkpoints", nil, http.StatusOK)["checkpoints"].([]any)
	if len(keys) != 1 || len(cps) != 1 || cps[0].(map[string]any)["key_id"] != keys[0].(map[string]any)["key_id"] {
		t.Fatalf("keys %v checkpoints %v", keys, cps)
	}
	if got := e.call(t, auditor, http.MethodGet, "/api/v1/audit/checkpoints?after=1", nil, http.StatusOK)["checkpoints"].([]any); len(got) != 0 {
		t.Fatalf("after the last: %v", got)
	}
	// Retention is set by those who manage the audit, within bounds.
	e.call(t, auditor, http.MethodPatch, "/api/v1/settings/audit", map[string]any{"retention_days": 30}, http.StatusForbidden)
	if r := e.call(t, owner, http.MethodPatch, "/api/v1/settings/audit", map[string]any{"retention_days": 30}, http.StatusOK); r["retention_days"] != float64(30) {
		t.Fatalf("retention %v", r)
	}
	e.call(t, owner, http.MethodPatch, "/api/v1/settings/audit", map[string]any{"retention_days": -1}, http.StatusUnprocessableEntity)
	e.call(t, owner, http.MethodPatch, "/api/v1/settings/audit", map[string]any{"retention_days": 99999}, http.StatusUnprocessableEntity)
}

func TestAuditEraseAPI(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, owner := e.user(t, "boss", store.RoleOwner)
	_, auditor := e.user(t, "otto", store.RoleAuditor)
	_, ada := e.user(t, "ada", store.RoleMember)
	tok := e.call(t, ada, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "laptop"}, http.StatusCreated)["id"].(string)
	seedCalls(t, e, 3, tok)
	seedCalls(t, e, 2, "tok-bob")
	e.call(t, auditor, http.MethodPost, "/api/v1/audit/erase", map[string]any{"person": "ada"}, http.StatusForbidden)
	e.call(t, owner, http.MethodPost, "/api/v1/audit/erase", map[string]any{"person": "nobody"}, http.StatusNotFound)
	if res := e.call(t, owner, http.MethodPost, "/api/v1/audit/erase", map[string]any{"person": "ada"}, http.StatusOK); res["erased"] != float64(3) {
		t.Fatalf("erase %v", res)
	}
	recs, _, _ := e.st.SearchAudit(ctx, e.org, store.AuditQuery{Actor: "tok-bob"})
	for _, r := range recs {
		if len(r.Intent.Payload) == 0 {
			t.Fatal("another person's arguments were erased")
		}
	}
}
