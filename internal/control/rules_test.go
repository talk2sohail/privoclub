package control

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"privo.club/privo/internal/store"
)

// ADR 0024: rules with conditions on arguments; export and import as YAML.

func TestCreateRuleWithConditions(t *testing.T) {
	e := newSessionEnv(t)
	_, owner := e.user(t, "boss", store.RoleOwner)
	body := map[string]any{"server": "github", "tool": "delete_repo", "action": "deny",
		"conditions": []map[string]any{{"arg": "repo", "op": "equals", "value": "prod"}, {"arg": "n", "op": "in", "value": []any{1, 2}}}}
	pol := e.call(t, owner, http.MethodPost, "/api/v1/policies", body, http.StatusCreated)["policy"].(map[string]any)
	cs := pol["conditions"].([]any)
	if len(cs) != 2 || cs[0].(map[string]any)["value"] != "prod" {
		t.Fatalf("created %v", pol)
	}
	list := e.call(t, owner, http.MethodGet, "/api/v1/policies", nil, http.StatusOK)["policies"].([]any)
	if len(list) != 1 || len(list[0].(map[string]any)["conditions"].([]any)) != 2 {
		t.Fatalf("listed %v", list)
	}
	for _, bad := range []map[string]any{
		{"server": "*", "action": "deny", "conditions": []map[string]any{{"arg": "a", "op": "regex", "value": ".*"}}},
		{"server": "*", "action": "deny", "conditions": []map[string]any{{"arg": "", "op": "exists"}}},
		{"server": "*", "action": "deny", "conditions": []map[string]any{{"arg": "a", "op": "in", "value": "x"}}},
	} {
		e.call(t, owner, http.MethodPost, "/api/v1/policies", bad, http.StatusUnprocessableEntity)
	}
}

func TestPoliciesExportImport(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	_, owner := e.user(t, "boss", store.RoleOwner)
	ada, _ := e.user(t, "ada", store.RoleMember)
	_, appr := e.user(t, "carol", store.RoleApprover)
	e.call(t, owner, http.MethodPost, "/api/v1/teams", map[string]any{"slug": "designers", "name": "Designers"}, http.StatusCreated)
	e.call(t, owner, http.MethodPost, "/api/v1/policies", map[string]any{"server": "github", "tool": "delete_repo", "action": "deny",
		"conditions": []map[string]any{{"arg": "repo", "op": "equals", "value": "prod"}}}, http.StatusCreated)
	e.call(t, owner, http.MethodPost, "/api/v1/policies", map[string]any{"server": "*", "tool": "*", "subject": "team:designers", "action": "ask", "priority": 3}, http.StatusCreated)
	e.call(t, owner, http.MethodPost, "/api/v1/policies", map[string]any{"server": "*", "tool": "read_*", "subject": "user:" + ada.ID, "action": "allow"}, http.StatusCreated)
	if err := e.st.CreatePolicies(ctx, e.org, store.Policy{ID: "grant-1", Server: "github", Tool: "merge", Subject: "user:" + ada.ID, Action: store.ActionAllow,
		GrantOf: "appr-1", CreatedBy: "carol"}); err != nil {
		t.Fatal(err)
	}
	e.call(t, owner, http.MethodPatch, "/api/v1/settings/policy", map[string]any{"default": "ask"}, http.StatusOK)

	exp := e.call(t, owner, http.MethodGet, "/api/v1/policies/export", nil, http.StatusOK)
	y := exp["yaml"].(string)
	for _, want := range []string{"version: 1", "default: ask", "op: equals", "value: prod", "subject: team:designers", "subject: person:ada"} {
		if !strings.Contains(y, want) {
			t.Errorf("export lacks %q:\n%s", want, y)
		}
	}
	if strings.Contains(y, "merge") || exp["rules"] != float64(3) {
		t.Fatalf("the grant was exported:\n%s", y)
	}
	// Members and approvers can do neither.
	e.call(t, appr, http.MethodGet, "/api/v1/policies/export", nil, http.StatusForbidden)
	e.call(t, appr, http.MethodPost, "/api/v1/policies/import", map[string]any{"yaml": y, "mode": "merge"}, http.StatusForbidden)

	// A dry run of a replace says what it would do and changes nothing.
	dry := e.call(t, owner, http.MethodPost, "/api/v1/policies/import", map[string]any{"yaml": y, "mode": "replace", "dry_run": true}, http.StatusOK)
	if dry["add"] != float64(3) || dry["delete"] != float64(3) {
		t.Fatalf("dry run %v", dry)
	}
	if rules, _ := e.st.ListPolicies(ctx, e.org); len(rules) != 4 {
		t.Fatalf("a dry run changed the rules: %d", len(rules))
	}
	// Replace: the same rules again, the grant kept.
	e.call(t, owner, http.MethodPost, "/api/v1/policies/import", map[string]any{"yaml": y, "mode": "replace"}, http.StatusOK)
	rules, _ := e.st.ListPolicies(ctx, e.org)
	var grants, conditioned, adas int
	for _, r := range rules {
		if r.GrantOf != "" {
			grants++
		}
		if r.Conditions != "" {
			conditioned++
		}
		if r.Subject == "user:"+ada.ID && r.GrantOf == "" {
			adas++
		}
	}
	if len(rules) != 4 || grants != 1 || conditioned != 1 || adas != 1 {
		t.Fatalf("after replace: %+v", rules)
	}
	// Merge adds; anything unknown or invalid refuses the whole file.
	e.call(t, owner, http.MethodPost, "/api/v1/policies/import", map[string]any{"yaml": "version: 1\nrules:\n  - server: '*'\n    tool: '*'\n    action: deny\n", "mode": "merge"}, http.StatusOK)
	for name, bad := range map[string]string{
		"unknown team":   "version: 1\nrules:\n  - {server: '*', tool: '*', action: allow}\n  - {server: '*', tool: '*', subject: 'team:finance', action: allow}\n",
		"unknown person": "version: 1\nrules:\n  - {server: '*', tool: '*', subject: 'person:nobody', action: allow}\n",
		"bad condition":  "version: 1\nrules:\n  - {server: '*', tool: '*', action: deny, when: [{arg: a, op: regex, value: x}]}\n",
		"unknown field":  "version: 1\nrules:\n  - {server: '*', tool: '*', action: deny, colour: red}\n",
		"bad version":    "version: 2\nrules: []\n",
		"not yaml":       "{{{",
	} {
		before, _ := e.st.ListPolicies(ctx, e.org)
		for _, dry := range []bool{true, false} {
			code, raw := e.api.Serve(ctx, owner, http.MethodPost, "/api/v1/policies/import", map[string]any{"yaml": bad, "mode": "merge", "dry_run": dry})
			if code != http.StatusUnprocessableEntity {
				t.Errorf("%s (dry run %v): %d %s", name, dry, code, raw)
			}
		}
		if after, _ := e.st.ListPolicies(ctx, e.org); len(after) != len(before) {
			t.Errorf("%s changed the rules", name)
		}
	}
	e.call(t, owner, http.MethodPost, "/api/v1/policies/import", map[string]any{"yaml": y, "mode": "overwrite"}, http.StatusUnprocessableEntity)
	e.aw.Close()
	evs, _ := e.st.RecentAdminEvents(ctx, e.org, 50)
	var saw []string
	for _, ev := range evs {
		if strings.HasPrefix(ev.Action, "policy.import:") || strings.HasPrefix(ev.Action, "policy.export:") {
			saw = append(saw, ev.Action)
		}
	}
	if len(saw) != 3 {
		t.Fatalf("audit %q", saw)
	}
}
