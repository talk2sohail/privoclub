//go:build acceptance

package acceptance

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// F05 A through the real binary: with PRIVO_GITHUB_ORG_IDS only members of
// those organisations sign in, even with a valid invite; GitHub is asked
// for read:org.
func TestSignInLimitedToAGitHubOrg(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	gh.orgs = map[int][]int64{1: {42}, 2: {42}} // boss and ada are in it
	g := webGateway(t, gh, "PRIVO_GITHUB_ORG_IDS=42")
	boss := ownerOf(t, g, gh)
	join(t, g, gh, boss, "ada", 2)
	if !strings.Contains(strings.Join(gh.scopes, ","), "read:org") {
		t.Fatalf("scopes asked: %v", gh.scopes)
	}

	// An invite for someone outside the organisation does not let them in.
	code, inv := boss.app(http.MethodPost, "/v1/invites", map[string]any{"person": "mallory", "role": "member"}, nil)
	if code != http.StatusCreated {
		t.Fatalf("invite: %d %v", code, inv)
	}
	link := inv["invite"].(map[string]any)["url"].(string)
	p := newPerson(t, g)
	_, body := p.do(http.MethodGet, link, nil, nil)
	gh.as = fakeGitHubUser{ID: 3, Login: "mallory-gh", Name: "Mallory"}
	start, _ := p.do(http.MethodPost, g.base+"/auth/github/start", url.Values{"invite_token": {inviteTokenField.FindStringSubmatch(body)[1]}}, nil)
	cb, page := p.signInThroughGitHub(start)
	if cb.StatusCode != http.StatusForbidden || !strings.Contains(page, "organisation") {
		t.Fatalf("outside the org: %d %s", cb.StatusCode, page)
	}
	_, evs := boss.app(http.MethodGet, "/v1/audit/events?kind=admin&action=signin.refused", nil, nil)
	if len(list(evs, "events")) != 1 {
		t.Fatalf("the refusal is not audited once: %v", evs)
	}
}
