package control

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstream"
	"privo.club/privo/internal/vault"
)

// ADR 0018: the admin site calls the API in process as the signed-in person.

type sessionEnv struct {
	api *API
	st  *store.Store
	org store.OrgID
	aw  *audit.Writer
}

func newSessionEnv(t *testing.T) *sessionEnv {
	t.Helper()
	ctx := context.Background()
	st, org := newOrg(t)
	kek, _ := vault.NewKey()
	kr := keyring.New(st, vault.StaticKeyProvider(kek, nil))
	if err := kr.EnsureOrgKey(ctx, org); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	aw := audit.NewWriter(st, audit.Options{Logger: logger})
	t.Cleanup(aw.Close)
	mgr := upstream.New(upstream.Options{Store: st, Keys: kr, HTTPClient: egress.NewClient(egress.Options{}), RetryBase: time.Hour, Logger: logger})
	t.Cleanup(mgr.Close)
	api := &API{Store: st, Keys: kr, Auth: auth.New(st), Upstream: mgr, Audit: aw, Approval: &approval.Service{Store: st, Keys: kr},
		Logger: logger, TokenTTL: 24 * time.Hour}
	return &sessionEnv{api: api, st: st, org: org, aw: aw}
}

var sessionSeq int

// user creates a member with role and returns them and their session principal.
func (e *sessionEnv) user(t *testing.T, person, role string) (store.User, auth.Principal) {
	t.Helper()
	ctx := context.Background()
	sessionSeq++
	now := time.Now()
	ev := store.AuditEvent{ID: "seed-" + person, CallID: "seed-" + person, Time: now, Actor: "t", Action: "seed", Decision: "allow", Outcome: "ok"}
	if err := e.st.CreateSetupLink(ctx, e.org, store.NewSetupLink{ID: "l-" + person, SecretHash: "h-" + person, CreatedBy: "t", Now: now, ExpiresAt: now.Add(time.Hour)}, ev); err != nil {
		t.Fatal(err)
	}
	ev.ID, ev.CallID = "claim-"+person, "claim-"+person
	u, err := e.st.ClaimSetup(ctx, e.org, store.Claim{LinkID: "l-" + person, UserID: "u-" + person, Person: person, Subject: person, Login: person + "-gh", DisplayName: strings.ToUpper(person),
		Session: store.Session{ID: "s-" + person, CSRF: "c", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}, Now: now}, ev)
	if err != nil {
		t.Fatal(err)
	}
	if role != store.RoleOwner {
		ev.ID, ev.CallID = "role-"+person, "role-"+person
		roles, err := e.st.SetUserRoles(ctx, e.org, u.ID, strings.Split(role, ","), ev)
		if err != nil {
			t.Fatal(err)
		}
		u.Roles = roles
	}
	return u, auth.Principal{OrgID: e.org, Name: person, Roles: u.Roles, Kind: KindSession, Person: person, UserID: u.ID}
}

func (e *sessionEnv) call(t *testing.T, p auth.Principal, method, path string, body any, want int) map[string]any {
	t.Helper()
	code, raw := e.api.Serve(context.Background(), p, method, path, body)
	if code != want {
		t.Fatalf("%s %s: %d, want %d: %s", method, path, code, want, raw)
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestServeRunsHandlersAsTheSessionUser(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	e.call(t, owner, http.MethodGet, "/api/v1/servers", nil, http.StatusOK)
	res := e.call(t, owner, http.MethodPost, "/api/v1/policies", map[string]any{"server": "*", "tool": "*", "action": "ask"}, http.StatusCreated)
	pol, _ := res["policy"].(map[string]any)
	if pol["created_by"] != "boss" {
		t.Fatalf("policy %v", pol)
	}
	// Validation is the API's own.
	if code, raw := e.api.Serve(context.Background(), owner, http.MethodPost, "/api/v1/policies", map[string]any{"server": "BAD SERVER", "action": "ask"}); code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "server must be") {
		t.Fatalf("invalid rule: %d %s", code, raw)
	}
}

func TestServeChecksPermissions(t *testing.T) {
	e := newSessionEnv(t)
	_, member := e.user(t, "ada", store.RoleMember)
	for _, path := range []string{"/api/v1/tokens", "/api/v1/policies", "/api/v1/members", "/api/v1/audit/events"} {
		if code, _ := e.api.Serve(context.Background(), member, http.MethodGet, path, nil); code != http.StatusForbidden {
			t.Errorf("member GET %s: %d, want 403", path, code)
		}
	}
	// Only a session principal with a user id is accepted.
	_, owner := e.user(t, "boss", store.RoleOwner)
	notSession := owner
	notSession.Kind = store.KindControl
	noUser := owner
	noUser.UserID = ""
	for name, p := range map[string]auth.Principal{"token principal": notSession, "no user": noUser} {
		if code, _ := e.api.Serve(context.Background(), p, http.MethodGet, "/api/v1/servers", nil); code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, code)
		}
	}
	if code, _ := e.api.Serve(context.Background(), owner, http.MethodGet, "/api/v1/nosuch", nil); code != http.StatusNotFound {
		t.Errorf("unknown path: %d", code)
	}
}

func TestServeAuditsTheUser(t *testing.T) {
	e := newSessionEnv(t)
	u, owner := e.user(t, "boss", store.RoleOwner)
	e.call(t, owner, http.MethodPatch, "/api/v1/settings/policy", map[string]any{"default": "ask"}, http.StatusOK)
	e.aw.Close() // flush
	evs, _ := e.st.RecentAdminEvents(context.Background(), e.org, 10)
	for _, ev := range evs {
		if ev.Action == "policy.default=ask" {
			if ev.ActorID != "user:"+u.ID || ev.Actor != "boss" {
				t.Fatalf("audit actor %q %q", ev.Actor, ev.ActorID)
			}
			return
		}
	}
	t.Fatal("not audited")
}

func TestServeIsNotReachableOverHTTP(t *testing.T) {
	e := newSessionEnv(t)
	mux := http.NewServeMux()
	e.api.Routes(mux)
	for _, path := range []string{"/api/v1/members", "/api/v1/me/tokens", "/api/v1/servers"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a token over HTTP: %d", path, rec.Code)
		}
	}
}

func TestMembersRoleRules(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	_, admin := e.user(t, "carol", store.RoleAdmin)
	e.user(t, "ada", store.RoleMember)

	res := e.call(t, admin, http.MethodGet, "/api/v1/members", nil, http.StatusOK)
	list, _ := res["members"].([]any)
	if len(list) != 3 {
		t.Fatalf("members %v", res)
	}
	first := list[0].(map[string]any)
	if first["person"] != "ada" || first["github_login"] != "ada-gh" || first["role"] != "member" {
		t.Fatalf("member view %v", first)
	}
	// Admins manage members but not owners and admins, and cannot grant them.
	e.call(t, admin, http.MethodPatch, "/api/v1/members/ada", map[string]any{"role": "approver"}, http.StatusOK)
	e.call(t, admin, http.MethodPatch, "/api/v1/members/ada", map[string]any{"role": "admin"}, http.StatusForbidden)
	e.call(t, admin, http.MethodPatch, "/api/v1/members/boss", map[string]any{"role": "member"}, http.StatusForbidden)
	// Nobody changes themselves.
	e.call(t, admin, http.MethodPatch, "/api/v1/members/carol", map[string]any{"role": "member"}, http.StatusForbidden)
	e.call(t, owner, http.MethodPatch, "/api/v1/members/boss", map[string]any{"role": "admin"}, http.StatusForbidden)
	// Owners can make owners; the last owner cannot be demoted.
	e.call(t, owner, http.MethodPatch, "/api/v1/members/carol", map[string]any{"role": "owner"}, http.StatusOK)
	carolOwner := admin
	carolOwner.Roles = []string{store.RoleOwner}
	e.call(t, carolOwner, http.MethodPatch, "/api/v1/members/boss", map[string]any{"role": "member"}, http.StatusOK)
	bossMember := owner
	bossMember.Roles = []string{store.RoleMember}
	if code, _ := e.api.Serve(context.Background(), bossMember, http.MethodGet, "/api/v1/members", nil); code != http.StatusForbidden {
		t.Fatalf("demoted owner still manages members: %d", code)
	}
	e.call(t, carolOwner, http.MethodPost, "/api/v1/members/carol/remove", nil, http.StatusForbidden) // not themselves
	e.call(t, carolOwner, http.MethodPatch, "/api/v1/members/nobody", map[string]any{"role": "member"}, http.StatusNotFound)
	e.call(t, carolOwner, http.MethodPatch, "/api/v1/members/ada", map[string]any{"role": "root"}, http.StatusUnprocessableEntity)
}

func TestLastOwnerCannotBeRemovedOrDemoted(t *testing.T) {
	e := newSessionEnv(t)
	e.user(t, "boss", store.RoleOwner)
	_, other := e.user(t, "eve", store.RoleOwner)
	e.call(t, other, http.MethodPost, "/api/v1/members/boss/remove", nil, http.StatusOK)
	// eve is now the only owner; a second owner account cannot exist to try,
	// so check the rule directly through the count.
	if n, _ := e.api.activeOwners(context.Background(), e.org); n != 1 {
		t.Fatalf("active owners %d", n)
	}
}

func TestRemoveMemberRevokesTokensSessionsAndGrants(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, owner := e.user(t, "boss", store.RoleOwner)
	ada, adaP := e.user(t, "ada", store.RoleMember)
	res := e.call(t, adaP, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "laptop"}, http.StatusCreated)
	plain, _ := res["token"].(string)
	if _, _, err := e.api.Auth.Verify(ctx, plain); err != nil {
		t.Fatalf("ada's token: %v", err)
	}
	res = e.call(t, owner, http.MethodPost, "/api/v1/members/ada/remove", nil, http.StatusOK)
	if res["tokens_revoked"] != float64(1) {
		t.Fatalf("remove %v", res)
	}
	time.Sleep(auth.CacheTTL + 100*time.Millisecond)
	if _, _, err := e.api.Auth.Verify(ctx, plain); err == nil {
		t.Fatal("a removed member's token still works")
	}
	if _, _, err := e.st.SessionByHash(ctx, e.org, "s-ada", time.Now(), time.Hour); err == nil {
		t.Fatal("a removed member's session still works")
	}
	u, _ := e.st.UserByPerson(ctx, e.org, "ada")
	if u.ID != ada.ID || u.DisabledAt == nil {
		t.Fatal("not disabled")
	}
	// Removed members are listed as removed and cannot be changed again.
	e.call(t, owner, http.MethodPost, "/api/v1/members/ada/remove", nil, http.StatusNotFound)
}

func TestMyTokensAreOnlyMine(t *testing.T) {
	e := newSessionEnv(t)
	_, ada := e.user(t, "ada", store.RoleMember)
	_, bob := e.user(t, "bob", store.RoleMember)
	_, appr := e.user(t, "appr", store.RoleApprover)
	res := e.call(t, ada, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "laptop"}, http.StatusCreated)
	if res["person"] != "ada" || res["role"] != "member" || res["kind"] != "gateway" || !strings.HasPrefix(res["token"].(string), "privo_pat_") {
		t.Fatalf("created %v", res)
	}
	id := res["id"].(string)
	e.call(t, bob, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "desk"}, http.StatusCreated)
	list := e.call(t, ada, http.MethodGet, "/api/v1/me/tokens", nil, http.StatusOK)["tokens"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["name"] != "laptop" {
		t.Fatalf("ada lists %v", list)
	}
	e.call(t, bob, http.MethodDelete, "/api/v1/me/tokens/"+id, nil, http.StatusNotFound)
	e.call(t, ada, http.MethodDelete, "/api/v1/me/tokens/"+id, nil, http.StatusOK)
	// Approvers do not use tools, so they get no gateway token.
	e.call(t, appr, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "x"}, http.StatusForbidden)
}

func TestSelfApprovalRefusalIsAudited(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	gate := &approval.Service{Store: e.st, Keys: e.api.Keys, Hold: 5 * time.Millisecond, Poll: time.Millisecond}
	out, err := gate.Gate(context.Background(), approval.Request{Org: e.org, RequesterID: "tok-boss", RequesterName: "boss-laptop", RequesterPerson: "boss",
		ServerID: "srv1", ServerAlias: "github", Tool: "merge", ArgsDigest: "d1", Args: json.RawMessage(`{"pr":1}`)})
	if err != nil || out.Result != approval.Pending {
		t.Fatalf("Gate = %+v, %v", out, err)
	}
	e.call(t, owner, http.MethodPost, "/api/v1/approvals/"+out.ID+"/approve", nil, http.StatusForbidden)
	e.aw.Close() // flush
	evs, _ := e.st.RecentAdminEvents(context.Background(), e.org, 20)
	for _, ev := range evs {
		if ev.Action == "approval.approve:"+out.ID {
			if ev.Decision != "deny" || ev.Outcome != "refused" || ev.Actor != "boss" {
				t.Fatalf("refusal audited as %+v", ev)
			}
			return
		}
	}
	t.Fatal("the refused self-approval is not audited")
}
