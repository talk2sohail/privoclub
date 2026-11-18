package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
)

// F07 review R-01 (High): a token is bound to its member by id, and only the
// member makes it. An administrator cannot issue a token that acts as a member.

func TestAdminCannotIssueTokenForAMember(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	e.user(t, "ada", store.RoleMember)

	for _, body := range []map[string]any{
		{"name": "x", "kind": "gateway", "person": "ada"},
		{"name": "ada", "kind": "gateway"}, // the person defaults to the name
		{"name": "y", "kind": "gateway", "person": " ada "},
		{"name": "z", "kind": "control", "role": "auditor", "person": "ada"},
		{"name": "w", "kind": "gateway", "person": "boss"}, // the owner's own person too: owners make theirs signed in
	} {
		code, raw := e.api.Serve(context.Background(), owner, http.MethodPost, "/api/v1/tokens", body)
		if code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "Tokens page") {
			t.Errorf("token %v: %d %s, want 422 pointing at the Tokens page", body, code, raw)
		}
	}
	// A service or script that is no member still gets a token, bound to no one.
	res := e.call(t, owner, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "ci", "kind": "gateway", "person": "ci-bot"}, http.StatusCreated)
	if res["personal"] != false {
		t.Fatalf("automation token: %v", res)
	}
	if tok, err := e.st.TokenByID(context.Background(), res["id"].(string)); err != nil || tok.UserID != "" || tok.Person != "ci-bot" {
		t.Fatalf("stored automation token: %+v %v", tok, err)
	}
}

func TestMyTokenIsBoundToTheSignedInUser(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	ada, adaP := e.user(t, "ada", store.RoleMember)
	res := e.call(t, adaP, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "laptop"}, http.StatusCreated)
	if res["personal"] != true {
		t.Fatalf("created %v", res)
	}
	tok, err := e.st.TokenByID(ctx, res["id"].(string))
	if err != nil || tok.UserID != ada.ID || tok.Person != "ada" {
		t.Fatalf("stored %+v %v", tok, err)
	}
	p, _, err := e.api.Auth.Verify(ctx, res["token"].(string))
	if err != nil || p.UserID != ada.ID {
		t.Fatalf("principal %+v %v", p, err)
	}
	list := e.call(t, owner(t, e), http.MethodGet, "/api/v1/tokens", nil, http.StatusOK)["tokens"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["personal"] != true {
		t.Fatalf("admins see %v", list)
	}
}

func owner(t *testing.T, e *sessionEnv) auth.Principal {
	t.Helper()
	_, p := e.user(t, "boss", store.RoleOwner)
	return p
}

// A control token has no member behind it, whatever person it names, so it
// makes, lists and revokes no "own" tokens.
func TestControlTokenHasNoOwnTokens(t *testing.T) {
	e := newSessionEnv(t)
	_, adaP := e.user(t, "ada", store.RoleMember)
	res := e.call(t, adaP, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "laptop"}, http.StatusCreated)
	id := res["id"].(string)
	ctl := auth.Principal{OrgID: e.org, TokenID: "tok-ctl", Name: "ops", Roles: []string{store.RoleOwner}, Kind: store.KindControl, Person: "ada"}
	for _, tc := range []struct {
		name string
		h    handler
		req  *http.Request
	}{
		{"create", e.api.createMyToken, httptest.NewRequest(http.MethodPost, "/api/v1/me/tokens", strings.NewReader(`{"name":"x"}`))},
		{"list", e.api.listMyTokens, httptest.NewRequest(http.MethodGet, "/api/v1/me/tokens", nil)},
		{"revoke", e.api.revokeMyToken, httptest.NewRequest(http.MethodDelete, "/api/v1/me/tokens/"+id, nil)},
	} {
		tc.req.Header.Set("Content-Type", "application/json")
		tc.req.SetPathValue("id", id)
		err := tc.h(httptest.NewRecorder(), tc.req, ctl)
		var ae *httpError
		if !errors.As(err, &ae) || ae.status != http.StatusForbidden {
			t.Errorf("%s with a control token naming ada: %v, want 403", tc.name, err)
		}
	}
	if tok, _ := e.st.TokenByID(context.Background(), id); tok.RevokedAt != nil {
		t.Fatal("a control token revoked ada's own token")
	}
}

// Removing a member revokes the tokens bound to them, and, for tokens made
// before R-01, the ones that carry their person.
func TestRemoveMemberRevokesBoundAndNamedTokens(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	boss := owner(t, e)
	ada, adaP := e.user(t, "ada", store.RoleMember)
	e.call(t, adaP, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "laptop"}, http.StatusCreated)
	now := time.Now()
	legacy := store.APIToken{OrgID: e.org, ID: "tok-legacy", Name: "old-ada", Role: store.RoleMember, Kind: store.KindGateway, Person: "ada",
		SecretHash: "h", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := e.st.CreateToken(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	res := e.call(t, boss, http.MethodPost, "/api/v1/members/ada/remove", nil, http.StatusOK)
	if res["tokens_revoked"] != float64(2) {
		t.Fatalf("remove %v", res)
	}
	toks, _ := e.st.ListTokens(ctx, e.org)
	for _, tok := range toks {
		if tok.UserID == ada.ID || tok.Person == "ada" {
			t.Fatalf("still active: %+v", tok)
		}
	}
}
