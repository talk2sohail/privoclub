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
	srv := store.Server{OrgID: e.org, ID: "srv1", Alias: "github", Transport: "http", Config: store.ServerConfig{URL: "https://github.example/mcp"},
		Enabled: true, CreatedAt: time.Now(), Teams: []string{store.TeamEveryone}}
	if err := e.st.CreateServer(context.Background(), srv, nil); err != nil {
		t.Fatal(err)
	}
	return e
}

// accept records and accepts tool's definition on srv1; its hash is "h-"+tool.
func (e *approvalsEnv) accept(t *testing.T, tool string) {
	t.Helper()
	ctx := context.Background()
	pin := store.Pin{ServerID: "srv1", Kind: "tool", Name: tool, Hash: "h-" + tool, Definition: `{"name":` + fmt.Sprintf("%q", tool) + `}`}
	if err := e.st.RecordSeen(ctx, e.org, []store.Pin{pin}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.AcceptPins(ctx, e.org, "srv1", []store.PinKey{{Kind: "tool", Name: tool, Hash: "h-" + tool}}); err != nil {
		t.Fatal(err)
	}
}

func (e *approvalsEnv) ask(t *testing.T, tool, digest, ruleID string) string {
	t.Helper()
	return e.askAs(t, e.adaTok, "ada", tool, digest, ruleID)
}

func (e *approvalsEnv) askAs(t *testing.T, tokenID, person, tool, digest, ruleID string) string {
	t.Helper()
	e.accept(t, tool)
	out, err := e.gate.Gate(context.Background(), approval.Request{Org: e.org, RequesterID: tokenID, RequesterName: person + "-laptop", RequesterPerson: person,
		ServerID: "srv1", ServerAlias: "github", Tool: tool, ArgsDigest: digest, Args: json.RawMessage(`{"pr":1}`), RuleID: ruleID, PinHash: "h-" + tool})
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
	// The grant is pinned to the definition that was asked about.
	defer func() {
		rules, _ := e.st.ListPolicies(context.Background(), e.org)
		for _, r := range rules {
			if r.Action == store.ActionAllow && r.PinHash != "h-merge*" {
				t.Errorf("grant pinned to %q", r.PinHash)
			}
		}
	}()

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
	if g["subject"] != "user:"+ada.ID || g["server"] != "github" || g["tool"] != `merge\*` || g["action"] != "allow" || g["grant_of"] != id {
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
	e.call(t, e.owner, http.MethodPatch, "/api/v1/settings/policy", map[string]any{"default": "ask"}, http.StatusOK)
	// Asked by the org default: the grant ranks 0.
	id := e.ask(t, "merge", "d1", "")
	if g := e.call(t, e.appr, http.MethodPost, "/api/v1/approvals/"+id+"/approve", map[string]any{"grant": "15m"}, http.StatusOK)["grant"].(map[string]any); g["grant_of"] != id {
		t.Fatalf("grant %v", g)
	}
	// No grant without an approval: a refused self-approval grants nothing.
	_, adaSelf := e.user(t, "dan", "member,approver")
	danTok := e.call(t, adaSelf, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "dan-laptop"}, http.StatusCreated)["id"].(string)
	ownID := e.askAs(t, danTok, "dan", "merge", "d9", "")
	e.call(t, adaSelf, http.MethodPost, "/api/v1/approvals/"+ownID+"/approve", map[string]any{"grant": "8h"}, http.StatusForbidden)
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

// Security review 2026-11-20, finding 1: nobody allows a tool for themselves,
// even where they may approve their own calls.
func TestNoGrantForOneself(t *testing.T) {
	e := newApprovalsEnv(t)
	ctx := context.Background()
	e.call(t, e.owner, http.MethodPatch, "/api/v1/settings/policy", map[string]any{"default": "ask"}, http.StatusOK)
	if err := e.st.SetSetting(ctx, e.org, approval.SettingAllowSelf, "true"); err != nil {
		t.Fatal(err)
	}
	_, dan := e.user(t, "dan", "member,approver")
	danTok := e.call(t, dan, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "dan-laptop"}, http.StatusCreated)["id"].(string)
	id := e.askAs(t, danTok, "dan", "delete_repo", "d1", "")
	e.call(t, dan, http.MethodPost, "/api/v1/approvals/"+id+"/approve", map[string]any{"grant": "8h"}, http.StatusForbidden)
	if a, _ := e.st.GetApproval(ctx, e.org, id); a.Status != store.ApprovalPending {
		t.Fatalf("a refused self-grant decided the approval: %s", a.Status)
	}
	// Approving one's own call alone is still allowed here.
	e.call(t, dan, http.MethodPost, "/api/v1/approvals/"+id+"/approve", nil, http.StatusOK)
	if rules, _ := e.st.ListPolicies(ctx, e.org); len(rules) != 0 {
		t.Fatalf("rules %+v", rules)
	}
}

// Security review 2026-11-20, finding 2: the rules are read again when the
// grant is made; a deny added while the request waited is not outranked.
func TestGrantRefusedWhenRulesChanged(t *testing.T) {
	e := newApprovalsEnv(t)
	ctx := context.Background()
	ask := e.call(t, e.owner, http.MethodPost, "/api/v1/policies", map[string]any{"server": "github", "tool": "*", "action": "ask"}, http.StatusCreated)["policy"].(map[string]any)
	id := e.ask(t, "delete_repo", "d1", ask["id"].(string))
	deny := e.call(t, e.owner, http.MethodPost, "/api/v1/policies", map[string]any{"server": "github", "tool": "delete_repo", "action": "deny"}, http.StatusCreated)["policy"].(map[string]any)
	if code, raw := e.api.Serve(ctx, e.appr, http.MethodPost, "/api/v1/approvals/"+id+"/approve", map[string]any{"grant": "8h"}); code != http.StatusConflict || !strings.Contains(string(raw), "would now be denied") {
		t.Fatalf("grant over a newer deny: %d %s", code, raw)
	}
	if a, _ := e.st.GetApproval(ctx, e.org, id); a.Status != store.ApprovalPending {
		t.Fatalf("a refused grant decided the approval: %s", a.Status)
	}
	// With the deny gone and a higher ask, the grant is made: it replaces
	// whichever ask wins, without a rank of its own (ADR 0024).
	e.call(t, e.owner, http.MethodDelete, "/api/v1/policies/"+deny["id"].(string), nil, http.StatusOK)
	e.call(t, e.owner, http.MethodPost, "/api/v1/policies", map[string]any{"server": "github", "tool": "delete_*", "action": "ask", "priority": 40}, http.StatusCreated)
	g := e.call(t, e.appr, http.MethodPost, "/api/v1/approvals/"+id+"/approve", map[string]any{"grant": "1h"}, http.StatusOK)["grant"].(map[string]any)
	if g["grant_of"] != id || g["priority"] != float64(0) {
		t.Fatalf("grant %v", g)
	}
}

// A grant needs the accepted definition that was asked about: a call for a
// definition that is not (or no longer) accepted gets no grant.
func TestGrantNeedsTheAcceptedDefinition(t *testing.T) {
	e := newApprovalsEnv(t)
	ctx := context.Background()
	e.call(t, e.owner, http.MethodPatch, "/api/v1/settings/policy", map[string]any{"default": "ask"}, http.StatusOK)
	e.accept(t, "merge")
	out, err := e.gate.Gate(ctx, approval.Request{Org: e.org, RequesterID: e.adaTok, RequesterName: "ada-laptop", RequesterPerson: "ada",
		ServerID: "srv1", ServerAlias: "github", Tool: "merge", ArgsDigest: "d1", Args: json.RawMessage(`{}`), PinHash: "h-older"})
	if err != nil {
		t.Fatal(err)
	}
	if code, raw := e.api.Serve(ctx, e.appr, http.MethodPost, "/api/v1/approvals/"+out.ID+"/approve", map[string]any{"grant": "1h"}); code != http.StatusConflict || !strings.Contains(string(raw), "no longer the accepted one") {
		t.Fatalf("grant for a definition that is not accepted: %d %s", code, raw)
	}
}
