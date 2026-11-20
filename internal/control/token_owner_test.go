package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/gateway"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
)

// F27 (independent review 2026-11-03, F07 R-01 and F14 R-02): a token belongs
// to a member by user id. The person a token names gives it nothing.

func verify(t *testing.T, e *sessionEnv, plain string) auth.Principal {
	t.Helper()
	p, _, err := e.api.Auth.Verify(context.Background(), plain)
	if err != nil {
		t.Fatalf("token does not verify: %v", err)
	}
	return p
}

func TestMyTokenBelongsToTheSignedInMember(t *testing.T) {
	e := newSessionEnv(t)
	ada, adaP := e.user(t, "ada", store.RoleMember)
	res := e.call(t, adaP, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "laptop"}, http.StatusCreated)
	if res["bound"] != true || res["person"] != "ada" {
		t.Fatalf("created %v", res)
	}
	p := verify(t, e, res["token"].(string))
	if p.UserID != ada.ID || p.Kind != store.KindGateway || !slices.Equal(p.Roles, []string{store.RoleMember}) {
		t.Fatalf("principal %+v, want ada's user id on a member gateway token", p)
	}
}

// The hole: an owner or admin issued a gateway token that named a member and
// called upstream servers with that member's own account.
func TestAnAdministratorCannotIssueATokenThatIsAMember(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, owner := e.user(t, "boss", store.RoleOwner)
	ada, adaP := e.user(t, "ada", store.RoleMember)
	mkTeamWithServer(t, e, "design", "figma", ada.ID)

	for name, body := range map[string]map[string]any{
		"person names the member":       {"name": "as-ada", "role": "member", "kind": "gateway", "person": "ada"},
		"the name is the member's name": {"name": "ada", "role": "member", "kind": "gateway"},
		"a user id in the request":      {"name": "sneaky", "role": "member", "kind": "gateway", "person": "ada", "user_id": ada.ID},
	} {
		if name == "a user id in the request" {
			// Unknown fields are refused, so nobody can believe it worked.
			code, raw := e.api.Serve(ctx, owner, http.MethodPost, "/api/v1/tokens", body)
			if code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
				t.Errorf("%s: %d %s, want the request refused", name, code, raw)
			}
			continue
		}
		res := e.call(t, owner, http.MethodPost, "/api/v1/tokens", body, http.StatusCreated)
		if res["bound"] != false {
			t.Errorf("%s: created %v, want bound=false", name, res)
		}
		p := verify(t, e, res["token"].(string))
		if p.UserID != "" {
			t.Errorf("%s: the token is member %q", name, p.UserID)
		}
		acc, err := gateway.Access(ctx, e.st, p)
		if err != nil {
			t.Fatal(err)
		}
		if acc.Reaches("srv-figma") || len(acc.Teams) != 1 || acc.Teams[0] != store.TeamEveryone {
			t.Errorf("%s: reaches ada's team server: %+v", name, acc)
		}
	}
	// Ada's own token does reach it: the control for the checks above.
	res := e.call(t, adaP, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "laptop"}, http.StatusCreated)
	acc, err := gateway.Access(ctx, e.st, verify(t, e, res["token"].(string)))
	if err != nil || !acc.Reaches("srv-figma") {
		t.Fatalf("ada's own token: %+v %v", acc, err)
	}
}

// mkTeamWithServer makes a team with one member and one server only that team reaches.
func mkTeamWithServer(t *testing.T, e *sessionEnv, team, alias, userID string) {
	t.Helper()
	ctx := context.Background()
	ev := func(a string) store.AuditEvent {
		return store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: "t", Action: a, Decision: "allow", Outcome: "ok"}
	}
	if err := e.st.CreateTeam(ctx, e.org, store.Team{Slug: team, Name: team, CreatedAt: time.Now()}, ev("team.create")); err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddTeamMember(ctx, e.org, team, userID, time.Now(), ev("team.member.add")); err != nil {
		t.Fatal(err)
	}
	srv := store.Server{OrgID: e.org, ID: "srv-" + alias, Alias: alias, Transport: "http", Config: store.ServerConfig{URL: "https://" + alias + ".example/mcp"},
		Enabled: true, CreatedAt: time.Now(), Teams: []string{team}}
	if err := e.st.CreateServer(ctx, srv, nil); err != nil {
		t.Fatal(err)
	}
}

// An API token is no member, whatever person it names: it cannot make "its
// own" gateway token (the second half of the hole, through /me/tokens).
func TestAnAPITokenCannotMakeAMembersToken(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	e.user(t, "ada", store.RoleMember)
	res := e.call(t, owner, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "script", "role": "admin", "kind": "control", "person": "ada"}, http.StatusCreated)
	p := verify(t, e, res["token"].(string))
	if p.Person != "ada" || p.UserID != "" {
		t.Fatalf("control token principal %+v", p)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/tokens", strings.NewReader(`{"name":"as-ada"}`))
	req.Header.Set("Content-Type", "application/json")
	err := e.api.createMyToken(rec, req, p)
	var he *httpError
	if !errors.As(err, &he) || he.status != http.StatusForbidden {
		t.Fatalf("createMyToken with an API token named ada: %v (wrote %d %s)", err, rec.Code, rec.Body)
	}
	toks, _ := e.st.ListTokens(context.Background(), e.org)
	for _, tok := range toks {
		if tok.Name == "as-ada" {
			t.Fatal("a token was stored")
		}
	}
}

func TestMyTokensAreFoundByMemberNotByName(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	ada, adaP := e.user(t, "ada", store.RoleMember)
	bob, bobP := e.user(t, "bob", store.RoleMember)
	mk := func(id, person, user string) {
		t.Helper()
		err := e.st.CreateToken(ctx, store.APIToken{OrgID: e.org, ID: id, Name: id, Role: store.RoleMember, Kind: store.KindGateway, Person: person, UserID: user,
			SecretHash: "h", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
	}
	mk("t-ada", "ada", ada.ID)        // ada's own
	mk("t-old-ada", "ada", "")        // made before F27, or by an admin: names ada, belongs to nobody
	mk("t-bob-as-ada", "ada", bob.ID) // belongs to bob, whatever it names
	mk("t-old-bob", "bob", "")

	list := e.call(t, adaP, http.MethodGet, "/api/v1/me/tokens", nil, http.StatusOK)["tokens"].([]any)
	got := map[string]any{}
	for _, raw := range list {
		tok := raw.(map[string]any)
		got[tok["id"].(string)] = tok["bound"]
	}
	if len(got) != 2 || got["t-ada"] != true || got["t-old-ada"] != false {
		t.Fatalf("ada lists %v, want her own token (bound) and the one that names her (not bound)", got)
	}
	e.call(t, adaP, http.MethodDelete, "/api/v1/me/tokens/t-bob-as-ada", nil, http.StatusNotFound)
	e.call(t, adaP, http.MethodDelete, "/api/v1/me/tokens/t-old-bob", nil, http.StatusNotFound)
	e.call(t, adaP, http.MethodDelete, "/api/v1/me/tokens/t-old-ada", nil, http.StatusOK)
	e.call(t, adaP, http.MethodDelete, "/api/v1/me/tokens/t-ada", nil, http.StatusOK)
	e.call(t, bobP, http.MethodDelete, "/api/v1/me/tokens/t-bob-as-ada", nil, http.StatusOK)
}

func TestRemovingAMemberRevokesTheirTokensAndTheOnesThatNameThem(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, owner := e.user(t, "boss", store.RoleOwner)
	ada, adaP := e.user(t, "ada", store.RoleMember)
	bob, _ := e.user(t, "bob", store.RoleMember)
	e.call(t, adaP, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "laptop"}, http.StatusCreated)
	for id, tok := range map[string]store.APIToken{
		"t-old-ada":  {Person: "ada"},
		"t-bob":      {Person: "bob", UserID: bob.ID},
		"t-bob-ada":  {Person: "ada", UserID: bob.ID}, // belongs to bob: stays
		"t-ada-anon": {Person: "someone", UserID: ada.ID},
	} {
		tok.OrgID, tok.ID, tok.Name, tok.Role, tok.Kind = e.org, id, id, store.RoleMember, store.KindGateway
		tok.SecretHash, tok.CreatedAt, tok.ExpiresAt = "h", time.Now(), time.Now().Add(time.Hour)
		if err := e.st.CreateToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
	}
	res := e.call(t, owner, http.MethodPost, "/api/v1/members/ada/remove", nil, http.StatusOK)
	if res["tokens_revoked"] != float64(3) {
		t.Fatalf("remove %v, want 3 tokens revoked (laptop, t-old-ada, t-ada-anon)", res)
	}
	left := map[string]bool{}
	toks, _ := e.st.ListTokens(ctx, e.org)
	for _, tok := range toks {
		left[tok.ID] = true
	}
	if !left["t-bob"] || !left["t-bob-ada"] || left["t-old-ada"] || left["t-ada-anon"] {
		t.Fatalf("active tokens after removing ada: %v", left)
	}
}

func TestAuditListNamesTheMemberACallRanAs(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, owner := e.user(t, "boss", store.RoleOwner)
	ada, _ := e.user(t, "ada", store.RoleMember)
	now := time.Now()
	if err := e.st.InsertAudit(ctx, e.org,
		store.AuditEvent{ID: ids.New(), CallID: "c-own", Phase: "intent", Time: now, Actor: "laptop", ActorID: "t-ada", UserID: ada.ID, Server: "linear", Item: "whoami", Action: "tools/call", Decision: "allow"},
		store.AuditEvent{ID: ids.New(), CallID: "c-svc", Phase: "intent", Time: now.Add(time.Millisecond), Actor: "ada", ActorID: "t-svc", Server: "echo", Item: "echo", Action: "tools/call", Decision: "allow"},
	); err != nil {
		t.Fatal(err)
	}
	events := e.call(t, owner, http.MethodGet, "/api/v1/audit/events", nil, http.StatusOK)["events"].([]any)
	byCall := map[string]map[string]any{}
	for _, raw := range events {
		ev := raw.(map[string]any)
		byCall[ev["id"].(string)] = ev
	}
	if own := byCall["c-own"]; own["user"] != "ada" || own["user_id"] != ada.ID {
		t.Fatalf("ada's own call: %v", own)
	}
	// A token named "ada" that belongs to nobody is not shown as ada.
	if svc := byCall["c-svc"]; svc["user"] != nil || svc["user_id"] != nil {
		t.Fatalf("a call by a token of nobody names a member: %v", svc)
	}
}
