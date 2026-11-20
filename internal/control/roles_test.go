package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
)

// ADR 0025: a person holds a set of roles.

func roles(t *testing.T, e *sessionEnv, person string) []string {
	t.Helper()
	u, err := e.st.UserByPerson(context.Background(), e.org, person)
	if err != nil {
		t.Fatal(err)
	}
	return u.Roles
}

func TestPatchMemberRolesRules(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	_, admin := e.user(t, "carol", store.RoleAdmin)
	e.user(t, "ada", store.RoleMember)
	e.user(t, "dan", store.RoleAdmin)

	res := e.call(t, owner, http.MethodPatch, "/api/v1/members/ada", map[string]any{"roles": []string{"member", "approver"}}, http.StatusOK)
	if got := res["roles"].([]any); len(got) != 2 || got[0] != "approver" || got[1] != "member" || res["role"] != "approver" {
		t.Fatalf("patched %v", res)
	}
	if got := roles(t, e, "ada"); !slices.Equal(got, []string{store.RoleApprover, store.RoleMember}) {
		t.Fatalf("stored %q", got)
	}
	// Admins change sets within member, approver and auditor, for people who
	// are neither owner nor admin.
	e.call(t, admin, http.MethodPatch, "/api/v1/members/ada", map[string]any{"roles": []string{"auditor", "member"}}, http.StatusOK)
	e.call(t, admin, http.MethodPatch, "/api/v1/members/ada", map[string]any{"roles": []string{"admin"}}, http.StatusForbidden)
	e.call(t, admin, http.MethodPatch, "/api/v1/members/boss", map[string]any{"roles": []string{"member"}}, http.StatusForbidden)
	e.call(t, admin, http.MethodPatch, "/api/v1/members/dan", map[string]any{"roles": []string{"member"}}, http.StatusForbidden)
	// Nobody edits their own roles.
	e.call(t, admin, http.MethodPatch, "/api/v1/members/carol", map[string]any{"roles": []string{"member", "approver"}}, http.StatusForbidden)
	// Only valid sets.
	for _, bad := range [][]string{{}, {"admin", "member"}, {"owner", "approver"}, {"root"}} {
		e.call(t, owner, http.MethodPatch, "/api/v1/members/ada", map[string]any{"roles": bad}, http.StatusUnprocessableEntity)
	}
	if got := roles(t, e, "ada"); !slices.Equal(got, []string{store.RoleAuditor, store.RoleMember}) {
		t.Fatalf("after refusals %q", got)
	}
	// One audit event per change, with the set before and after.
	e.aw.Close()
	var actions []string
	evs, _ := e.st.RecentAdminEvents(context.Background(), e.org, 50)
	for _, ev := range evs {
		if strings.HasPrefix(ev.Action, "member.roles:ada:") {
			actions = append(actions, ev.Action+" by "+ev.Actor)
		}
	}
	slices.Sort(actions)
	want := []string{"member.roles:ada:approver+member->auditor+member by carol", "member.roles:ada:member->approver+member by boss"}
	if !slices.Equal(actions, want) {
		t.Fatalf("audit %q, want %q", actions, want)
	}
}

// Scripts written before ADR 0025 send one "role"; it still works.
func TestPatchMemberRoleStillWorks(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	e.user(t, "ada", store.RoleMember)
	e.call(t, owner, http.MethodPatch, "/api/v1/members/ada", map[string]any{"role": "auditor"}, http.StatusOK)
	if got := roles(t, e, "ada"); !slices.Equal(got, []string{store.RoleAuditor}) {
		t.Fatalf("stored %q", got)
	}
	list := e.call(t, owner, http.MethodGet, "/api/v1/members", nil, http.StatusOK)["members"].([]any)
	for _, m := range list {
		if v := m.(map[string]any); v["person"] == "ada" && (v["role"] != "auditor" || len(v["roles"].([]any)) != 1) {
			t.Fatalf("listed %v", v)
		}
	}
}

// A person's own gateway token carries member whatever they hold, and only a
// person whose roles use the gateway gets one.
func TestPersonalGatewayTokenIsMemberOnly(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, owner := e.user(t, "boss", store.RoleOwner)
	_, both := e.user(t, "dan", "member,approver")
	_, appr := e.user(t, "carol", store.RoleApprover)
	for _, p := range []struct {
		name string
		res  map[string]any
	}{
		{"owner", e.call(t, owner, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "boss-laptop"}, http.StatusCreated)},
		{"member and approver", e.call(t, both, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "dan-laptop"}, http.StatusCreated)},
	} {
		if p.res["role"] != store.RoleMember {
			t.Errorf("%s's token: %v", p.name, p.res)
		}
		pr, _, err := e.api.Auth.Verify(ctx, p.res["token"].(string))
		if err != nil || !slices.Equal(pr.Roles, []string{store.RoleMember}) {
			t.Errorf("%s's token principal %+v %v", p.name, pr, err)
		}
	}
	e.call(t, appr, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "x"}, http.StatusForbidden)
}

func TestInviteTakesRoles(t *testing.T) {
	e := newSessionEnv(t)
	e.api.PublicURL, _ = url.Parse("https://privo.example")
	_, owner := e.user(t, "boss", store.RoleOwner)
	_, admin := e.user(t, "carol", store.RoleAdmin)
	res := e.call(t, admin, http.MethodPost, "/api/v1/invites", map[string]any{"person": "ada", "roles": []string{"member", "approver"}}, http.StatusCreated)
	inv := res["invite"].(map[string]any)
	if got := inv["roles"].([]any); len(got) != 2 || got[0] != "approver" || got[1] != "member" {
		t.Fatalf("invite %v", inv)
	}
	e.call(t, owner, http.MethodPost, "/api/v1/invites", map[string]any{"person": "eve", "role": "auditor"}, http.StatusCreated)
	e.call(t, owner, http.MethodPost, "/api/v1/invites", map[string]any{"person": "fay"}, http.StatusCreated) // member by default
	e.call(t, admin, http.MethodPost, "/api/v1/invites", map[string]any{"person": "gus", "roles": []string{"admin"}}, http.StatusForbidden)
	e.call(t, owner, http.MethodPost, "/api/v1/invites", map[string]any{"person": "gus", "roles": []string{"admin"}}, http.StatusCreated)
	for _, bad := range [][]string{{"owner"}, {"admin", "member"}, {}} {
		e.call(t, owner, http.MethodPost, "/api/v1/invites", map[string]any{"person": "hal", "roles": bad}, http.StatusUnprocessableEntity)
	}
}

// A member who also approves sees their own requests marked and cannot
// decide them, unless the org allows self-approval; they decide others'.
func TestMemberApproverAndOwnRequests(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, dan := e.user(t, "dan", "member,approver")
	gate := &approval.Service{Store: e.st, Keys: e.api.Keys, Hold: 5 * time.Millisecond, Poll: time.Millisecond}
	ask := func(person, digest string) string {
		out, err := gate.Gate(ctx, approval.Request{Org: e.org, RequesterID: "tok-" + person, RequesterName: person + "-laptop", RequesterPerson: person,
			ServerID: "srv1", ServerAlias: "github", Tool: "merge", ArgsDigest: digest, Args: json.RawMessage(`{"pr":1}`)})
		if err != nil || out.Result != approval.Pending {
			t.Fatalf("Gate = %+v, %v", out, err)
		}
		return out.ID
	}
	own, other := ask("dan", "d1"), ask("eve", "d2")

	list := e.call(t, dan, http.MethodGet, "/api/v1/approvals?status=pending", nil, http.StatusOK)["approvals"].([]any)
	seen := map[string]map[string]any{}
	for _, a := range list {
		v := a.(map[string]any)
		seen[v["id"].(string)] = v
	}
	if seen[own]["own"] != true || seen[own]["decide_blocked"] == nil || seen[other]["own"] != nil || seen[other]["decide_blocked"] != nil {
		t.Fatalf("list %v", seen)
	}
	e.call(t, dan, http.MethodPost, "/api/v1/approvals/"+own+"/approve", nil, http.StatusForbidden)
	e.call(t, dan, http.MethodPost, "/api/v1/approvals/"+other+"/approve", nil, http.StatusOK)

	if err := e.st.SetSetting(ctx, e.org, approval.SettingAllowSelf, "true"); err != nil {
		t.Fatal(err)
	}
	list = e.call(t, dan, http.MethodGet, "/api/v1/approvals?status=pending", nil, http.StatusOK)["approvals"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["own"] != true || list[0].(map[string]any)["decide_blocked"] != nil {
		t.Fatalf("with self-approval allowed: %v", list)
	}
	e.call(t, dan, http.MethodPost, "/api/v1/approvals/"+own+"/approve", nil, http.StatusOK)
}

// A member's own token is stored as member, so it is protected by its
// person's roles: an admin cannot revoke an owner's or another admin's.
func TestAdminCannotRevokeAnOwnersOwnToken(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	_, admin := e.user(t, "carol", store.RoleAdmin)
	_, other := e.user(t, "dan", store.RoleAdmin)
	_, ada := e.user(t, "ada", store.RoleMember)
	tok := func(p auth.Principal, name string) string {
		return e.call(t, p, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": name}, http.StatusCreated)["id"].(string)
	}
	bossTok, danTok, adaTok, carolTok := tok(owner, "boss-laptop"), tok(other, "dan-laptop"), tok(ada, "ada-laptop"), tok(admin, "carol-laptop")
	e.call(t, admin, http.MethodDelete, "/api/v1/tokens/"+bossTok, nil, http.StatusForbidden)
	e.call(t, admin, http.MethodDelete, "/api/v1/tokens/"+danTok, nil, http.StatusForbidden)
	e.call(t, admin, http.MethodDelete, "/api/v1/tokens/"+adaTok, nil, http.StatusOK)
	e.call(t, admin, http.MethodDelete, "/api/v1/tokens/"+carolTok, nil, http.StatusOK) // their own
	e.call(t, owner, http.MethodDelete, "/api/v1/tokens/"+danTok, nil, http.StatusOK)
}
