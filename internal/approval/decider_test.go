package approval

import (
	"context"
	"errors"
	"testing"

	"privo.club/privo/internal/store"
)

// A person who decides in the browser has no token (ADR 0016): the decider is
// identified by user id, and the recorded approver says so.

func TestDecideRecordsUser(t *testing.T) {
	s, st, org := setup(t)
	ctx := context.Background()
	a, _ := s.Gate(ctx, req(org, "d1"))
	if err := s.Decide(ctx, org, a.ID, Decider{UserID: "u-77", Name: "carol", Person: "carol"}, true); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetApproval(ctx, org, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DecidedBy != "carol (user u-77)" {
		t.Errorf("DecidedBy = %q, want %q", got.DecidedBy, "carol (user u-77)")
	}
	// The call that consumes it reports who approved.
	out, _ := s.Gate(ctx, req(org, "d1"))
	if out.Result != Approved || out.DecidedBy != "carol (user u-77)" {
		t.Errorf("outcome %+v", out)
	}

	// A token decider is recorded as before.
	b, _ := s.Gate(ctx, req(org, "d2"))
	if err := s.Decide(ctx, org, b.ID, approver, true); err != nil {
		t.Fatal(err)
	}
	gotB, _ := st.GetApproval(ctx, org, b.ID)
	if gotB.DecidedBy != "bob (tokB)" {
		t.Errorf("token decider recorded as %q", gotB.DecidedBy)
	}
}

func TestDecideSelfApprovalByPersonForUsers(t *testing.T) {
	s, st, org := setup(t)
	ctx := context.Background()
	a, _ := s.Gate(ctx, req(org, "d1")) // requested by person "ada"
	self := Decider{UserID: "u-1", Name: "ada", Person: "ada"}
	if err := s.Decide(ctx, org, a.ID, self, true); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("a user whose person is the requester's: %v, want ErrSelfApproval", err)
	}
	_ = st.SetSetting(ctx, org, SettingAllowSelf, "true")
	if err := s.Decide(ctx, org, a.ID, self, true); err != nil {
		t.Fatalf("with allow_self on: %v", err)
	}
}

// Both ids empty must never count as "the same token".
func TestEmptyTokenIDIsNotTheRequester(t *testing.T) {
	s, _, org := setup(t)
	ctx := context.Background()
	r := req(org, "d1")
	r.RequesterID, r.RequesterPerson = "", "someone-else"
	a, err := s.Gate(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decide(ctx, org, a.ID, Decider{UserID: "u-9", Name: "dave", Person: "dave"}, true); err != nil {
		t.Fatalf("a web decider was taken for the requester: %v", err)
	}
}

func TestDecideUnknownApproval(t *testing.T) {
	s, _, org := setup(t)
	if err := s.Decide(context.Background(), org, "nope", Decider{UserID: "u-1", Name: "x", Person: "x"}, true); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want store.ErrNotFound", err)
	}
}
