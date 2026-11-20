package web

import (
	"net/http"
	"strings"
	"testing"

	"privo.club/privo/internal/store"
)

// F05 A: with PRIVO_GITHUB_ORG_IDS, only members of those GitHub
// organisations sign in, even with a valid invite; GitHub is asked for
// read:org only then.
func TestSignInRefusedOutsideTheOrgs(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.GitHub.OrgIDs = []int64{42} })
	e.gh.wantScope = "read:org"
	e.gh.orgs = map[int64][]int64{octo.ID: {7, 42}}
	// In the organisation: signs in.
	e.appSignedIn(octo, "octo", store.RoleOwner)

	// A member whose GitHub account is not in it: refused, and audited.
	outsider := ghUser{ID: 3003, Login: "outsider", Name: "Out"}
	e.member(outsider, "out", store.RoleMember)
	res := e.signIn(e.browser(), outsider)
	if res.StatusCode != http.StatusForbidden || !strings.Contains(res.Body, "organisation") {
		t.Fatalf("outside the orgs: %d %s", res.StatusCode, res.Body)
	}
	evs, _ := e.st.RecentAdminEvents(t.Context(), e.org, 50)
	found := false
	for _, ev := range evs {
		if ev.Action == "signin.refused:not_in_org" && ev.Actor == "outsider" {
			found = true
		}
	}
	if !found {
		t.Fatal("the refusal is not in the audit")
	}
}

func TestNoReadOrgScopeWhenUnset(t *testing.T) {
	e := newEnv(t)
	e.gh.wantScope = "" // the fake fails on any scope
	e.appSignedIn(octo, "octo", store.RoleOwner)
}
