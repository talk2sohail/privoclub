package gateway

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstream"
)

// ADR 0020: who the caller is decides which servers they reach. These tests
// cover how a principal becomes a member; the store tests cover what a member
// reaches, and the acceptance test the lists and calls end to end.

type accessStore struct {
	Store
	users  map[string]store.User // by id
	asked  []string              // user ids passed to AccessFor
	roles  map[string][]string   // a member's current roles; member when not set
	failOn string
}

func (s *accessStore) UserByID(_ context.Context, _ store.OrgID, id string) (store.User, error) {
	u, ok := s.users[id]
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	return u, nil
}

func (s *accessStore) AccessFor(_ context.Context, _ store.OrgID, userID string) (store.Access, error) {
	s.asked = append(s.asked, userID)
	if userID == s.failOn {
		return store.Access{}, errors.New("db down")
	}
	acc := store.Access{Servers: map[string]bool{"srv-" + userID: true}, Teams: []string{store.TeamEveryone}}
	if userID != "" {
		acc.Roles = []string{store.RoleMember}
		if r, ok := s.roles[userID]; ok {
			acc.Roles = r
		}
	}
	return acc, nil
}

// F27: the member is the principal's user id. A person name is a label: a
// token that names a member without belonging to them is nobody's.
func TestAccessUsesTheUserIDNeverThePersonName(t *testing.T) {
	ctx := context.Background()
	st := &accessStore{failOn: "none"}
	g := &Gateway{Store: st}

	if a, err := g.access(ctx, auth.Principal{OrgID: "o", TokenID: "t1", UserID: "u-ada", Person: "ada", Kind: store.KindGateway}); err != nil || !a.Reaches("srv-u-ada") {
		t.Fatalf("ada's own token: %v %v", a, err)
	}
	if _, err := g.access(ctx, auth.Principal{OrgID: "o", UserID: "u-bob", Person: "bob", Kind: "session"}); err != nil {
		t.Fatal(err)
	}
	a, err := g.access(ctx, auth.Principal{OrgID: "o", TokenID: "t2", Person: "ada", Kind: store.KindGateway})
	if err != nil || a.Reaches("srv-u-ada") {
		t.Fatalf("a token that only names ada reached her servers: %v %v", a, err)
	}
	want := []string{"u-ada", "u-bob", ""}
	if len(st.asked) != 3 || st.asked[0] != want[0] || st.asked[1] != want[1] || st.asked[2] != want[2] {
		t.Fatalf("AccessFor asked for %q, want %q (own token, signed-in user, nobody's token)", st.asked, want)
	}
}

func TestAccessFailsClosed(t *testing.T) {
	ctx := context.Background()
	g := &Gateway{Store: &accessStore{failOn: "u-ada"}}
	if _, err := g.access(ctx, auth.Principal{OrgID: "o", UserID: "u-ada"}); err == nil {
		t.Fatal("a failed access lookup gave access")
	}
	if _, err := g.visible(ctx, auth.Principal{OrgID: "o", UserID: "u-ada"}, "tool"); err == nil {
		t.Fatal("lists went on without access")
	}
}

// Whose account a per-person call runs as (ADR 0017, F27).
func TestGrantOwnerIsTheTokensMember(t *testing.T) {
	ctx := context.Background()
	gone := time.Now()
	g := &Gateway{Store: &accessStore{users: map[string]store.User{"u-ada": {ID: "u-ada", Person: "ada"}, "u-gone": {ID: "u-gone", Person: "gone", DisabledAt: &gone}}}}
	c := call{item: upstream.Item{Alias: "linear"}}

	c.p = auth.Principal{OrgID: "o", TokenID: "t1", UserID: "u-ada", Person: "ada"}
	if id, err := g.grantOwner(ctx, c); err != nil || id != "u-ada" {
		t.Fatalf("ada's own token: %q %v", id, err)
	}
	for name, p := range map[string]auth.Principal{
		"a token that only names ada":   {OrgID: "o", TokenID: "t2", Person: "ada"},
		"a token of a removed member":   {OrgID: "o", TokenID: "t3", UserID: "u-gone", Person: "gone"},
		"a token of an unknown user id": {OrgID: "o", TokenID: "t4", UserID: "u-nobody", Person: "ada"},
	} {
		c.p = p
		id, err := g.grantOwner(ctx, c)
		var ae *accountError
		if id != "" || !errors.As(err, &ae) {
			t.Errorf("%s: owner %q, error %v; want no owner and a message for the person", name, id, err)
		}
	}
}

// ADR 0025: a member's own token works while one of their current roles uses
// the gateway; a person who became only an approver reaches nothing with it.
func TestDemotedMembersTokenReachesNothing(t *testing.T) {
	ctx := context.Background()
	g := &Gateway{Store: &accessStore{failOn: "none", roles: map[string][]string{"u-carol": {store.RoleApprover}, "u-dan": {store.RoleApprover, store.RoleMember}}}}
	tok := func(user string) auth.Principal {
		return auth.Principal{OrgID: "o", UserID: user, Person: user, Roles: []string{store.RoleMember}, Kind: store.KindGateway}
	}
	if a, err := g.access(ctx, tok("u-carol")); err != nil || a.Reaches("srv-u-carol") || len(a.Teams) != 0 {
		t.Fatalf("approver-only member's token: %+v %v", a, err)
	}
	if a, err := g.access(ctx, tok("u-dan")); err != nil || !a.Reaches("srv-u-dan") {
		t.Fatalf("member and approver's token: %+v %v", a, err)
	}
	// A token without a member keeps working on its own role.
	if a, err := g.access(ctx, auth.Principal{OrgID: "o", Roles: []string{store.RoleMember}, Kind: store.KindGateway}); err != nil || !a.Reaches("srv-") {
		t.Fatalf("automation token: %+v %v", a, err)
	}
}

// Rules see a member's current roles, never the role stored on their token;
// an automation token is seen with its own.
func TestRoleRulesSeeTheMembersCurrentRoles(t *testing.T) {
	own := auth.Principal{UserID: "u-ada", Roles: []string{store.RoleMember}}
	if got := CallerRoles(own, store.Access{Roles: []string{store.RoleApprover, store.RoleMember}}); !slices.Equal(got, []string{store.RoleApprover, store.RoleMember}) {
		t.Fatalf("own token: %q", got)
	}
	if got := CallerRoles(own, store.Access{}); len(got) != 0 {
		t.Fatalf("a removed member's token keeps roles %q", got)
	}
	ci := auth.Principal{Roles: []string{store.RoleAdmin}}
	if got := CallerRoles(ci, store.Access{Roles: []string{store.RoleMember}}); !slices.Equal(got, []string{store.RoleAdmin}) {
		t.Fatalf("automation token: %q", got)
	}
}
