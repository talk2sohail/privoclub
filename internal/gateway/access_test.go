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
	users  map[string]store.User // by person
	err    error
	asked  []string // user ids passed to AccessFor
	failOn string
}

func (s *accessStore) UserByPerson(_ context.Context, _ store.OrgID, person string) (store.User, error) {
	if s.err != nil {
		return store.User{}, s.err
	}
	u, ok := s.users[person]
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	return u, nil
}

func (s *accessStore) AccessFor(_ context.Context, _ store.OrgID, userID string) (store.Access, error) {
	s.asked = append(s.asked, userID)
	if userID == s.failOn && s.failOn != "" {
		return store.Access{}, errors.New("db down")
	}
	return store.Access{Servers: map[string]bool{"srv-" + userID: true}, Teams: []string{store.TeamEveryone}}, nil
}

func TestAccessResolvesTheCallersMember(t *testing.T) {
	ctx := context.Background()
	st := &accessStore{users: map[string]store.User{"ada": {ID: "u-ada"}}}
	g := &Gateway{Store: st}

	if a, err := g.access(ctx, auth.Principal{OrgID: "o", Person: "ada", Kind: store.KindGateway}); err != nil || !a.Reaches("srv-u-ada") {
		t.Fatalf("token of a member: %v %v", a, err)
	}
	if _, err := g.access(ctx, auth.Principal{OrgID: "o", UserID: "u-bob", Person: "bob", Kind: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.access(ctx, auth.Principal{OrgID: "o", Person: "ci-bot", Kind: store.KindGateway}); err != nil {
		t.Fatal(err)
	}
	want := []string{"u-ada", "u-bob", ""}
	if len(st.asked) != 3 || st.asked[0] != want[0] || st.asked[1] != want[1] || st.asked[2] != want[2] {
		t.Fatalf("AccessFor asked for %q, want %q (member, signed-in user, no member)", st.asked, want)
	}
}

func TestAccessFailsClosed(t *testing.T) {
	ctx := context.Background()
	g := &Gateway{Store: &accessStore{err: errors.New("db down")}}
	if _, err := g.access(ctx, auth.Principal{OrgID: "o", Person: "ada"}); err == nil {
		t.Fatal("a failed member lookup gave access")
	}
	g = &Gateway{Store: &accessStore{failOn: "u-ada", users: map[string]store.User{"ada": {ID: "u-ada"}}}}
	if _, err := g.access(ctx, auth.Principal{OrgID: "o", Person: "ada"}); err == nil {
		t.Fatal("a failed access lookup gave access")
	}
	if _, err := g.visible(ctx, auth.Principal{OrgID: "o", Person: "ada"}, "tool"); err == nil {
		t.Fatal("lists went on without access")
	}
}
