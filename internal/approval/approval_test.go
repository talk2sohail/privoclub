package approval

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

func setup(t *testing.T) (*Service, *store.Store, store.OrgID) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, _ := st.EnsureOrg(ctx, "default", "Default")
	kek, _ := vault.NewKey()
	kr := keyring.New(st, vault.StaticKeyProvider(kek, nil))
	if err := kr.EnsureOrgKey(ctx, org.ID); err != nil {
		t.Fatal(err)
	}
	return &Service{Store: st, Keys: kr, Hold: 50 * time.Millisecond, Poll: 5 * time.Millisecond}, st, org.ID
}

func req(org store.OrgID, digest string) Request {
	return Request{Org: org, RequesterID: "tokA", RequesterName: "ada", RequesterPerson: "ada", ServerID: "srv", ServerAlias: "github",
		Tool: "delete_repo", ArgsDigest: digest, Args: json.RawMessage(`{"repo":"sandbox"}`)}
}

var approver = Decider{TokenID: "tokB", Name: "bob", Person: "bob"}

func TestPendingThenApprovedThenSingleUse(t *testing.T) {
	s, _, org := setup(t)
	ctx := context.Background()

	out, err := s.Gate(ctx, req(org, "d1"))
	if err != nil || out.Result != Pending || out.ID == "" {
		t.Fatalf("first gate: %+v %v", out, err)
	}
	again, _ := s.Gate(ctx, req(org, "d1"))
	if again.ID != out.ID {
		t.Fatal("a retry created a second approval instead of reusing the pending one")
	}
	if err := s.Decide(ctx, org, out.ID, approver, true); err != nil {
		t.Fatal(err)
	}
	ran, _ := s.Gate(ctx, req(org, "d1"))
	if ran.Result != Approved || ran.ID != out.ID {
		t.Fatalf("after approval: %+v", ran)
	}
	next, _ := s.Gate(ctx, req(org, "d1"))
	if next.Result != Pending || next.ID == out.ID {
		t.Fatalf("approval was reusable: %+v", next)
	}
}

func TestDifferentArgumentsNeedTheirOwnApproval(t *testing.T) {
	s, _, org := setup(t)
	ctx := context.Background()
	a, _ := s.Gate(ctx, req(org, "d1"))
	_ = s.Decide(ctx, org, a.ID, approver, true)
	b, _ := s.Gate(ctx, req(org, "d2-prod"))
	if b.Result != Pending || b.ID == a.ID {
		t.Fatalf("approval for other arguments was used: %+v", b)
	}
}

func TestDecisionDuringHoldLetsCallContinue(t *testing.T) {
	s, st, org := setup(t)
	s.Hold = 2 * time.Second
	ctx := context.Background()
	var wg sync.WaitGroup
	var out Outcome
	wg.Add(1)
	go func() { defer wg.Done(); out, _ = s.Gate(ctx, req(org, "d1")) }()
	var id string
	deadline := time.Now().Add(time.Second)
	for id == "" && time.Now().Before(deadline) {
		list, _ := st.ListApprovals(ctx, org, store.ApprovalPending, 10)
		if len(list) == 1 {
			id = list[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.Decide(ctx, org, id, approver, true); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if out.Result != Approved {
		t.Fatalf("held call did not continue: %+v", out)
	}
}

func TestDenied(t *testing.T) {
	s, _, org := setup(t)
	ctx := context.Background()
	a, _ := s.Gate(ctx, req(org, "d1"))
	_ = s.Decide(ctx, org, a.ID, approver, false)
	if out, _ := s.Gate(ctx, req(org, "d1")); out.Result != Denied {
		t.Fatalf("denied call: %+v", out)
	}
	if err := s.Decide(ctx, org, a.ID, approver, true); !errors.Is(err, ErrNotPending) {
		t.Fatalf("decided twice: %v", err)
	}
}

func TestSelfApproval(t *testing.T) {
	s, st, org := setup(t)
	ctx := context.Background()
	a, _ := s.Gate(ctx, req(org, "d1"))
	// Same token, or another token belonging to the same person.
	for _, self := range []Decider{{TokenID: "tokA", Name: "other", Person: "x"}, {TokenID: "tokA2", Name: "ada-ui", Person: "ada"}} {
		if err := s.Decide(ctx, org, a.ID, self, true); !errors.Is(err, ErrSelfApproval) {
			t.Fatalf("self approval %+v: %v", self, err)
		}
	}
	_ = st.SetSetting(ctx, org, SettingAllowSelf, "true")
	if err := s.Decide(ctx, org, a.ID, Decider{TokenID: "tokA", Name: "ada", Person: "ada"}, true); err != nil {
		t.Fatalf("self approval with setting on: %v", err)
	}
}

func TestExpiry(t *testing.T) {
	s, _, org := setup(t)
	ctx := context.Background()
	now := time.Now()
	s.now = func() time.Time { return now }
	a, _ := s.Gate(ctx, req(org, "d1"))
	_ = s.Decide(ctx, org, a.ID, approver, true)
	now = now.Add(11 * time.Minute)
	out, _ := s.Gate(ctx, req(org, "d1"))
	if out.Result != Pending || out.ID == a.ID {
		t.Fatalf("expired approval was used: %+v", out)
	}
	if err := s.Decide(ctx, org, a.ID, approver, true); !errors.Is(err, ErrNotPending) {
		t.Fatalf("decided an expired approval: %v", err)
	}
}

func TestArgumentsEncryptedAndReadable(t *testing.T) {
	s, st, org := setup(t)
	ctx := context.Background()
	out, _ := s.Gate(ctx, req(org, "d1"))
	a, _ := st.GetApproval(ctx, org, out.ID)
	if string(a.Args) == `{"repo":"sandbox"}` {
		t.Fatal("arguments stored in plain text")
	}
	args, err := s.Arguments(ctx, org, a)
	if err != nil || string(args) != `{"repo":"sandbox"}` {
		t.Fatalf("arguments %s %v", args, err)
	}
}

func TestConcurrentConsumeIsSingleUse(t *testing.T) {
	s, _, org := setup(t)
	ctx := context.Background()
	a, _ := s.Gate(ctx, req(org, "d1"))
	_ = s.Decide(ctx, org, a.ID, approver, true)
	var mu sync.Mutex
	approved := 0
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, _ := s.Gate(ctx, req(org, "d1")); out.Result == Approved {
				mu.Lock()
				approved++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if approved != 1 {
		t.Fatalf("approval used %d times", approved)
	}
}

func TestConcurrentIdenticalCallsShareOneApproval(t *testing.T) {
	s, st, org := setup(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.Gate(ctx, req(org, "d1")) }()
	}
	wg.Wait()
	list, _ := st.ListApprovals(ctx, org, store.ApprovalPending, 50)
	if len(list) != 1 {
		t.Fatalf("%d pending approvals for one exact call", len(list))
	}
	_ = s.Decide(ctx, org, list[0].ID, approver, true)
	if out, _ := s.Gate(ctx, req(org, "d1")); out.Result != Approved {
		t.Fatalf("approved call did not run: %+v", out)
	}
}

func TestPendingCapPerRequester(t *testing.T) {
	s, _, org := setup(t)
	ctx := context.Background()
	for i := 0; i < MaxPendingPerRequester; i++ {
		if _, err := s.Gate(ctx, req(org, "d"+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Gate(ctx, req(org, "one-too-many")); !errors.Is(err, ErrTooManyPending) {
		t.Fatalf("err = %v, want ErrTooManyPending", err)
	}
}

func TestArgumentsPurgedOnceUsedOrDenied(t *testing.T) {
	s, st, org := setup(t)
	ctx := context.Background()
	a, _ := s.Gate(ctx, req(org, "d1"))
	_ = s.Decide(ctx, org, a.ID, approver, true)
	_, _ = s.Gate(ctx, req(org, "d1"))
	if got, _ := st.GetApproval(ctx, org, a.ID); len(got.Args) != 0 {
		t.Fatal("arguments kept after the approval was used")
	}
	b, _ := s.Gate(ctx, req(org, "d2"))
	_ = s.Decide(ctx, org, b.ID, approver, false)
	if got, _ := st.GetApproval(ctx, org, b.ID); len(got.Args) != 0 {
		t.Fatal("arguments kept after denial")
	}
}

func TestRestoreAfterFailedStart(t *testing.T) {
	s, _, org := setup(t)
	ctx := context.Background()
	a, _ := s.Gate(ctx, req(org, "d1"))
	_ = s.Decide(ctx, org, a.ID, approver, true)
	if out, _ := s.Gate(ctx, req(org, "d1")); out.Result != Approved {
		t.Fatal("not approved")
	}
	if err := s.Restore(ctx, org, a.ID); err != nil {
		t.Fatal(err)
	}
	if out, _ := s.Gate(ctx, req(org, "d1")); out.Result != Approved || out.ID != a.ID {
		t.Fatalf("restored approval not usable: %+v", out)
	}
}

// F10: a person takes back their own request; nobody else can.
func TestWithdrawOwnRequestOnly(t *testing.T) {
	s, st, org := setup(t)
	ctx := context.Background()
	now := time.Now()
	for _, tok := range []store.APIToken{
		{OrgID: org, ID: "tokA", Name: "ada-laptop", Role: store.RoleMember, Kind: store.KindGateway, Person: "ada", UserID: "u-ada", SecretHash: "h1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
	} {
		if err := st.CreateToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
	}
	r := req(org, "w1")
	r.RuleID = "rule-ask"
	out, err := s.Gate(ctx, r)
	if err != nil || out.Result != Pending {
		t.Fatalf("gate: %+v %v", out, err)
	}
	if a, _ := st.GetApproval(ctx, org, out.ID); a.RuleID != "rule-ask" {
		t.Fatalf("the asking rule is not kept: %+v", a)
	}
	for _, other := range []Decider{{UserID: "u-bob", Name: "bob", Person: "bob"}, {TokenID: "tokA", Name: "ada-laptop", Person: "ada"}, {Name: "x"}} {
		if err := s.Withdraw(ctx, org, out.ID, other); !errors.Is(err, ErrNotYours) {
			t.Fatalf("withdrawn by %+v: %v", other, err)
		}
	}
	ada := Decider{UserID: "u-ada", Name: "ada", Person: "ada"}
	if err := s.Withdraw(ctx, org, out.ID, ada); err != nil {
		t.Fatal(err)
	}
	if err := s.Withdraw(ctx, org, out.ID, ada); !errors.Is(err, ErrNotYours) {
		t.Fatalf("withdrawn twice: %v", err)
	}
	if err := s.Decide(ctx, org, out.ID, approver, true); !errors.Is(err, ErrNotPending) {
		t.Fatalf("approved after it was withdrawn: %v", err)
	}
	// The same call asks again.
	again, _ := s.Gate(ctx, r)
	if again.Result != Pending || again.ID == out.ID {
		t.Fatalf("retry after withdrawing: %+v", again)
	}
}

func TestHeldCallEndsWhenWithdrawn(t *testing.T) {
	s, st, org := setup(t)
	s.Hold = 5 * time.Second
	ctx := context.Background()
	now := time.Now()
	if err := st.CreateToken(ctx, store.APIToken{OrgID: org, ID: "tokA", Name: "ada-laptop", Role: store.RoleMember, Kind: store.KindGateway,
		Person: "ada", UserID: "u-ada", SecretHash: "h1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	done := make(chan Outcome, 1)
	go func() {
		out, _ := s.Gate(ctx, req(org, "w2"))
		done <- out
	}()
	var id string
	for i := 0; i < 200 && id == ""; i++ {
		if mine, _ := st.MyApprovals(ctx, org, "u-ada", time.Now()); len(mine) == 1 {
			id = mine[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.Withdraw(ctx, org, id, Decider{UserID: "u-ada", Name: "ada"}); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		if out.Result != Withdrawn {
			t.Fatalf("held call ended with %+v", out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the held call did not end when the request was withdrawn")
	}
}
