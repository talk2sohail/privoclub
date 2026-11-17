package gateway

import (
	"context"
	"errors"
	"testing"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
)

// ADR 0020: who the caller is decides which servers they reach. These tests
// cover how a principal becomes a member; the store tests cover what a member
// reaches, and the acceptance test the lists and calls end to end.

type accessStore struct {
	Store
	asked  []string // user ids passed to AccessFor
	failOn string
}

func (s *accessStore) AccessFor(_ context.Context, _ store.OrgID, userID string) (store.Access, error) {
	s.asked = append(s.asked, userID)
	if userID == s.failOn && s.failOn != "" {
		return store.Access{}, errors.New("db down")
	}
	return store.Access{Servers: map[string]bool{"srv-" + userID: true}, Teams: []string{store.TeamEveryone}}, nil
}

// F07 review R-01: the member is the user a token is bound to, or the
// signed-in person; a person name alone makes no one a member.
func TestAccessResolvesTheCallersMember(t *testing.T) {
	ctx := context.Background()
	st := &accessStore{}
	g := &Gateway{Store: st}

	if a, err := g.access(ctx, auth.Principal{OrgID: "o", UserID: "u-ada", Person: "ada", Kind: store.KindGateway}); err != nil || !a.Reaches("srv-u-ada") {
		t.Fatalf("ada's own token: %v %v", a, err)
	}
	if _, err := g.access(ctx, auth.Principal{OrgID: "o", UserID: "u-bob", Person: "bob", Kind: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.access(ctx, auth.Principal{OrgID: "o", Person: "ada", Kind: store.KindGateway}); err != nil {
		t.Fatal(err)
	}
	want := []string{"u-ada", "u-bob", ""}
	if len(st.asked) != 3 || st.asked[0] != want[0] || st.asked[1] != want[1] || st.asked[2] != want[2] {
		t.Fatalf("AccessFor asked for %q, want %q (own token, signed-in user, a token that only names ada)", st.asked, want)
	}
}

func TestAccessFailsClosed(t *testing.T) {
	ctx := context.Background()
	g := &Gateway{Store: &accessStore{failOn: "u-ada"}}
	ada := auth.Principal{OrgID: "o", UserID: "u-ada", Person: "ada"}
	if _, err := g.access(ctx, ada); err == nil {
		t.Fatal("a failed access lookup gave access")
	}
	if _, err := g.visible(ctx, ada, "tool"); err == nil {
		t.Fatal("lists went on without access")
	}
}

// The per-person case is covered end to end by TestUpstreamOAuthTwoPeople.
func TestAccountIsEmptyOffPerPersonServers(t *testing.T) {
	g := &Gateway{}
	c := call{p: auth.Principal{OrgID: "o", UserID: "u-ada", Person: "ada"}}
	if got := g.account(c); got != "" {
		t.Fatalf("without a per-person server: %q", got)
	}
}
