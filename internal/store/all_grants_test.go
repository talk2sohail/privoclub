package store

import (
	"context"
	"errors"
	"testing"
)

// F07 A: everyone's connections across servers, for admins: org-scoped,
// filtered, paged, never a token.
func TestListAllGrantsIsOrgScopedAndPaged(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	linear := oauthServer(t, s, org, "linear")
	notion := oauthServer(t, s, org, "notion")
	ada := member(t, s, org, "2001", "ada", RoleMember)
	bob := member(t, s, org, "2002", "bob", RoleMember)
	n := 0
	save := func(srv string, u User) {
		n++
		if _, err := s.SaveGrant(ctx, org, grantFor(srv, u.ID, "c"+string(rune('0'+n))), false, adminEv("upstream.connect:"+string(rune('0'+n)))); err != nil {
			t.Fatal(err)
		}
	}
	save(linear, ada)
	save(notion, ada)
	save(linear, bob)
	other, _ := s.EnsureOrg(ctx, "other", "Other")

	all, next, err := s.ListAllGrants(ctx, org, GrantQuery{})
	if err != nil || len(all) != 3 || next != "" {
		t.Fatalf("all: %d %q %v", len(all), next, err)
	}
	for _, g := range all {
		if g.Alias == "" || g.Person == "" || g.Kind != PurposeOAuthGrant || len(g.Credential.Ciphertext) != 0 {
			t.Fatalf("a listed grant: %+v", g)
		}
	}
	if got, _, _ := s.ListAllGrants(ctx, org, GrantQuery{Person: "ada"}); len(got) != 2 {
		t.Fatalf("ada's: %d", len(got))
	}
	if got, _, _ := s.ListAllGrants(ctx, org, GrantQuery{Server: "linear"}); len(got) != 2 {
		t.Fatalf("linear's: %d", len(got))
	}
	if got, _, _ := s.ListAllGrants(ctx, other.ID, GrantQuery{}); len(got) != 0 {
		t.Fatalf("another org sees %d", len(got))
	}
	// Pages of two, then the rest; the same three in the same order.
	first, next, err := s.ListAllGrants(ctx, org, GrantQuery{Limit: 2})
	if err != nil || len(first) != 2 || next == "" {
		t.Fatalf("first page: %d %q %v", len(first), next, err)
	}
	rest, next2, err := s.ListAllGrants(ctx, org, GrantQuery{Limit: 2, After: next})
	if err != nil || len(rest) != 1 || next2 != "" {
		t.Fatalf("second page: %d %q %v", len(rest), next2, err)
	}
	if first[0].Alias+first[0].Person == rest[0].Alias+rest[0].Person {
		t.Fatal("the pages overlap")
	}
	if _, _, err := s.ListAllGrants(ctx, org, GrantQuery{After: "garbage"}); !errors.Is(err, ErrBadGrantQuery) {
		t.Fatalf("a cursor this store did not make: %v", err)
	}
}
