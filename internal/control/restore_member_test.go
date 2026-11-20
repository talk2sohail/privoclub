package control

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"privo.club/privo/internal/store"
)

// A removed member comes back by whoever could change their roles (F14
// third slice); with no tokens from before.
func TestRestoreMemberRules(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	_, admin := e.user(t, "adam", store.RoleAdmin)
	ada, _ := e.user(t, "ada", store.RoleMember)
	e.user(t, "cy", store.RoleAdmin)

	// Not removed yet: nothing to restore.
	e.call(t, admin, http.MethodPost, "/api/v1/members/ada/restore", nil, http.StatusNotFound)

	e.call(t, owner, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "x"}, http.StatusCreated) // unrelated token stays
	e.call(t, admin, http.MethodPost, "/api/v1/members/ada/remove", nil, http.StatusOK)
	e.call(t, owner, http.MethodPost, "/api/v1/members/cy/remove", nil, http.StatusOK)

	// An admin restores a member, not an admin; an owner restores both.
	e.call(t, admin, http.MethodPost, "/api/v1/members/cy/restore", nil, http.StatusForbidden)
	out := e.call(t, admin, http.MethodPost, "/api/v1/members/ada/restore", nil, http.StatusOK)
	if out["restored"] != "ada" {
		t.Fatalf("answer: %v", out)
	}
	e.call(t, owner, http.MethodPost, "/api/v1/members/cy/restore", nil, http.StatusOK)
	e.call(t, owner, http.MethodPost, "/api/v1/members/nobody/restore", nil, http.StatusNotFound)

	u, err := e.st.UserByID(context.Background(), e.org, ada.ID)
	if err != nil || u.DisabledAt != nil || !u.Has(store.RoleMember) {
		t.Fatalf("ada after restore: %+v %v", u, err)
	}
	toks, _ := e.st.ListTokens(context.Background(), e.org)
	for _, tk := range toks {
		if tk.UserID == ada.ID {
			t.Fatalf("a token of hers came back: %+v", tk)
		}
	}
	if acts := adminActions(t, e); !strings.Contains(acts, "member.restore:ada") || !strings.Contains(acts, "member.restore:cy") {
		t.Fatalf("audit: %s", acts)
	}
	// The member list shows her as a member again.
	list := e.call(t, owner, http.MethodGet, "/api/v1/members", nil, http.StatusOK)
	for _, m := range list["members"].([]any) {
		if mm := m.(map[string]any); mm["person"] == "ada" && mm["removed"] == true {
			t.Fatalf("still listed as removed: %v", mm)
		}
	}
}
