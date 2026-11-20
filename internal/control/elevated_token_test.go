package control

import (
	"net/http"
	"strings"
	"testing"

	"privo.club/privo/internal/store"
)

// F14 R-07: a browser session minting an owner or admin control token, which
// works without signing in until it expires, says so first; the audit marks
// it as made in the browser.
func TestElevatedTokenFromASessionNeedsConfirmation(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	for _, role := range []string{store.RoleOwner, store.RoleAdmin} {
		out := e.call(t, owner, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "ops-" + role, "role": role, "kind": "control"}, http.StatusUnprocessableEntity)
		if msg, _ := out["error"].(string); !strings.Contains(msg, "confirm") {
			t.Fatalf("%s without confirm: %v", role, out)
		}
		e.call(t, owner, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "ops-" + role, "role": role, "kind": "control", "confirm": true}, http.StatusCreated)
	}
	// Lesser roles need no confirmation.
	e.call(t, owner, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "read", "role": "auditor", "kind": "control"}, http.StatusCreated)

	evs, err := e.st.RecentAdminEvents(t.Context(), e.org, 50)
	if err != nil {
		t.Fatal(err)
	}
	tagged := 0
	for _, ev := range evs {
		if strings.HasPrefix(ev.Action, "token.create:control:owner:") || strings.HasPrefix(ev.Action, "token.create:control:admin:") {
			if ev.Client != "web" {
				t.Fatalf("an elevated token made in the browser is not tagged: %+v", ev)
			}
			tagged++
		}
	}
	if tagged != 2 {
		t.Fatalf("%d tagged events, want 2", tagged)
	}
}
