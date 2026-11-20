package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
)

// F10 second slice: scoped grants, withdrawing, history.

type approvalsEnv struct {
	*sessionEnv
	owner, ada, appr auth.Principal
	adaTok           string // ada's own gateway token id
	gate             *approval.Service
}

func newApprovalsEnv(t *testing.T) *approvalsEnv {
	t.Helper()
	e := &approvalsEnv{sessionEnv: newSessionEnv(t)}
	_, e.owner = e.user(t, "boss", store.RoleOwner)
	_, e.ada = e.user(t, "ada", store.RoleMember)
	_, e.appr = e.user(t, "carol", store.RoleApprover)
	e.adaTok = e.call(t, e.ada, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "ada-laptop"}, http.StatusCreated)["id"].(string)
	e.gate = &approval.Service{Store: e.st, Keys: e.api.Keys, Hold: 5 * time.Millisecond, Poll: time.Millisecond}
	e.api.Approval = e.gate
	return e
}

func (e *approvalsEnv) ask(t *testing.T, tool, digest, ruleID string) string {
	t.Helper()
	out, err := e.gate.Gate(context.Background(), approval.Request{Org: e.org, RequesterID: e.adaTok, RequesterName: "ada-laptop", RequesterPerson: "ada",
		ServerID: "srv1", ServerAlias: "github", Tool: tool, ArgsDigest: digest, Args: json.RawMessage(`{"pr":1}`), RuleID: ruleID})
	if err != nil || out.Result != approval.Pending {
		t.Fatalf("Gate = %+v, %v", out, err)
	}
	return out.ID
}

func TestApproveWithGrant(t *testing.T) {
	e := newApprovalsEnv(t)
	ctx := context.Background()
	rule := e.call(t, e.owner, http.MethodPost, "/api/v1/policies", map[string]any{"server": "github", "tool": "merge*", "action": "ask", "priority": 5}, http.StatusCreated)["policy"].(map[string]any)
	id := e.ask(t, "merge*", "d1", rule["id"].(string))

	// Bad grants change nothing.
	e.call(t, e.appr, http.MethodPost, "/api/v1/approvals/"+id+"/approve", map[string]any{"grant": "24h"}, http.StatusUnprocessableEntity)
	e.call(t, e.appr, http.MethodPost, "/api/v1/approvals/"+id+"/deny", map[string]any{"grant": "1h"}, http.StatusUnprocessableEntity)
	if a, _ := e.st.GetApproval(ctx, e.org, id); a.Status != store.ApprovalPending {
		t.Fatalf("a refused grant decided the approval: %s", a.Status)
	}

	before := time.Now()
	res := e.call(t, e.appr, http.MethodPost, "/api/v1/approvals/"+id+"/approve", map[string]any{"grant": "1h"}, http.StatusOK)
	g := res["grant"].(map[string]any)
	ada, _ := e.st.UserByPerson(ctx, e.org, "ada")
	if g["subject"] != "user:"+ada.ID || g["server"] != "github" || g["tool"] != `merge\*` || g["action"] != "allow" || g["priority"] != float64(6) {
		t.Fatalf("grant %v", g)
	}
	exp, _ := time.Parse(time.RFC3339Nano, g["expires_at"].(string))
	if exp.Before(before.Add(59*time.Minute)) || exp.After(time.Now().Add(61*time.Minute)) {
		t.Fatalf("grant expires %v", exp)
	}
	// Rules name the person; the grant and the approval are audited.
	list := e.call(t, e.owner, http.MethodGet, "/api/v1/policies", nil, http.StatusOK)["policies"].([]any)
	if !slices.ContainsFunc(list, func(v any) bool { return v.(map[string]any)["subject_person"] == "ada" }) {
		t.Fatalf("rules %v", list)
	}
	e.aw.Close()
	evs, _ := e.st.RecentAdminEvents(ctx, e.org, 30)
	var actions []string
	for _, ev := range evs {
		actions = append(actions, ev.Action)
	}
	if !slices.Contains(actions, "approval.approve:"+id) || !slices.ContainsFunc(actions, func(a string) bool {
		return strings.HasPrefix(a, "policy.grant:") && strings.Contains(a, ":user:"+ada.ID+":github/merge*:1h0m0s")
	}) {
		t.Fatalf("audit %q", actions)
	}
}

func TestGrantFromTheDefaultAndRefusals(t *testing.T) {
	e := newApprovalsEnv(t)
	// Asked by the org default: the grant ranks 0.
	id := e.ask(t, "merge", "d1", "")
	if g := e.call(t, e.appr, http.MethodPost, "/api/v1/approvals/"+id+"/approve", map[string]any{"grant": "15m"}, http.StatusOK)["grant"].(map[string]any); g["priority"] != float64(0) {
		t.Fatalf("grant %v", g)
	}
	// No grant without an approval: a refused self-approval grants nothing.
	_, adaSelf := e.user(t, "dan", "member,approver")
	danTok := e.call(t, adaSelf, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "dan-laptop"}, http.StatusCreated)["id"].(string)
	out, _ := e.gate.Gate(context.Background(), approval.Request{Org: e.org, RequesterID: danTok, RequesterName: "dan-laptop", RequesterPerson: "dan",
		ServerID: "srv1", ServerAlias: "github", Tool: "merge", ArgsDigest: "d9", Args: json.RawMessage(`{}`)})
	e.call(t, adaSelf, http.MethodPost, "/api/v1/approvals/"+out.ID+"/approve", map[string]any{"grant": "8h"}, http.StatusForbidden)
	policies := e.call(t, e.owner, http.MethodGet, "/api/v1/policies", nil, http.StatusOK)["policies"].([]any)
	if len(policies) != 1 {
		t.Fatalf("a refused approval left a grant: %v", policies)
	}
	// Approving without a body still works.
	id2 := e.ask(t, "close", "d2", "")
	if res := e.call(t, e.appr, http.MethodPost, "/api/v1/approvals/"+id2+"/approve", nil, http.StatusOK); res["grant"] != nil {
		t.Fatalf("a plain approval granted: %v", res)
	}
}

func TestWithdrawAPI(t *testing.T) {
	e := newApprovalsEnv(t)
	id := e.ask(t, "merge", "d1", "")
	mine := e.call(t, e.ada, http.MethodGet, "/api/v1/me/approvals", nil, http.StatusOK)["approvals"].([]any)
	if len(mine) != 1 || mine[0].(map[string]any)["id"] != id || mine[0].(map[string]any)["arguments"] != nil {
		t.Fatalf("ada's requests %v", mine)
	}
	_, bob := e.user(t, "bob", store.RoleMember)
	if got := e.call(t, bob, http.MethodGet, "/api/v1/me/approvals", nil, http.StatusOK)["approvals"].([]any); len(got) != 0 {
		t.Fatalf("bob sees %v", got)
	}
	e.call(t, bob, http.MethodPost, "/api/v1/me/approvals/"+id+"/withdraw", nil, http.StatusNotFound)
	e.call(t, e.appr, http.MethodPost, "/api/v1/me/approvals/"+id+"/withdraw", nil, http.StatusForbidden) // approvers use no tools
	e.call(t, e.ada, http.MethodPost, "/api/v1/me/approvals/"+id+"/withdraw", nil, http.StatusOK)
	e.call(t, e.ada, http.MethodPost, "/api/v1/me/approvals/"+id+"/withdraw", nil, http.StatusNotFound)
	e.call(t, e.appr, http.MethodPost, "/api/v1/approvals/"+id+"/approve", nil, http.StatusConflict)
	e.aw.Close()
	evs, _ := e.st.RecentAdminEvents(context.Background(), e.org, 20)
	if !slices.ContainsFunc(evs, func(ev store.AuditEvent) bool { return ev.Action == "approval.withdraw:"+id && ev.Actor == "ada" }) {
		t.Fatal("the withdrawal is not audited")
	}
}

func TestApprovalHistoryAPI(t *testing.T) {
	e := newApprovalsEnv(t)
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, e.ask(t, "merge", fmt.Sprint("d", i), ""))
		time.Sleep(2 * time.Millisecond)
	}
	e.call(t, e.appr, http.MethodPost, "/api/v1/approvals/"+ids[0]+"/deny", nil, http.StatusOK)
	e.call(t, e.ada, http.MethodPost, "/api/v1/me/approvals/"+ids[1]+"/withdraw", nil, http.StatusOK)

	first := e.call(t, e.appr, http.MethodGet, "/api/v1/approvals?status=pending&limit=2", nil, http.StatusOK)
	page := first["approvals"].([]any)
	if len(page) != 2 || page[0].(map[string]any)["id"] != ids[4] || page[0].(map[string]any)["arguments"] == nil || first["next"] == "" {
		t.Fatalf("first page %v", first)
	}
	second := e.call(t, e.appr, http.MethodGet, "/api/v1/approvals?status=pending&limit=2&before="+first["next"].(string), nil, http.StatusOK)
	if p := second["approvals"].([]any); len(p) != 1 || p[0].(map[string]any)["id"] != ids[2] || second["next"] != "" {
		t.Fatalf("second page %v", second)
	}
	history := e.call(t, e.appr, http.MethodGet, "/api/v1/approvals?status=history", nil, http.StatusOK)["approvals"].([]any)
	var statuses []string
	for _, h := range history {
		v := h.(map[string]any)
		if v["arguments"] != nil {
			t.Fatalf("history shows arguments: %v", v)
		}
		statuses = append(statuses, v["status"].(string))
	}
	if !slices.Equal(statuses, []string{store.ApprovalWithdrawn, store.ApprovalDenied}) {
		t.Fatalf("history %q", statuses)
	}
	e.call(t, e.appr, http.MethodGet, "/api/v1/approvals?status=history&before=garbage", nil, http.StatusBadRequest)
	e.call(t, e.ada, http.MethodGet, "/api/v1/approvals?status=history", nil, http.StatusForbidden)
}
