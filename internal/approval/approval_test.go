package approval

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
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
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, _ := st.EnsureOrg(ctx, "default", "Default")
	// ada is a member: a token bound to her needs her to exist (ADR 0021).
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "privo.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (org_id, id, person, display_name, created_at) VALUES (?, 'u-ada', 'ada', 'Ada', '2026-01-01T00:00:00Z')`,
		`INSERT INTO user_roles (org_id, user_id, role, granted_at) VALUES (?, 'u-ada', 'member', '2026-01-01T00:00:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, q, org.ID); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
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

// F05 R-04: a person never decides their own request, whatever case their
// name is written in, and a member is known by their user id first.
func TestOwnKnowsTheMemberAndIgnoresCase(t *testing.T) {
	a := store.Approval{RequesterID: "tok-1", RequesterPerson: "Alice", RequesterUserID: "u-alice"}
	for name, d := range map[string]Decider{
		"the same token":                  {TokenID: "tok-1"},
		"her session":                     {UserID: "u-alice", Person: "alice"},
		"her name in another case":        {Person: "ALICE"},
		"her user id under another label": {UserID: "u-alice", Person: "al"},
	} {
		if !Own(d, a) {
			t.Errorf("%s: not seen as her own", name)
		}
	}
	for name, d := range map[string]Decider{
		"another member":   {UserID: "u-bob", Person: "bob"},
		"another token":    {TokenID: "tok-2", Person: "bob"},
		"nobody at all":    {},
		"an empty user id": {UserID: ""},
	} {
		if Own(d, a) {
			t.Errorf("%s: seen as her own", name)
		}
	}
	// A request without a member (a token of nobody) matches no user id.
	if Own(Decider{UserID: "u-bob"}, store.Approval{RequesterID: "tok-9", RequesterPerson: "ci"}) {
		t.Error("an empty requester user id matched")
	}
}

func TestCancelDuringHoldWithdrawsPendingApproval(t *testing.T) {
	s, st, org := setup(t)
	s.Hold = 5 * time.Second
	s.Poll = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())

	type res struct {
		out Outcome
		err error
	}
	done := make(chan res, 1)
	go func() {
		out, err := s.Gate(ctx, req(org, "cancel-hold"))
		done <- res{out, err}
	}()

	var id string
	deadline := time.Now().Add(2 * time.Second)
	for id == "" && time.Now().Before(deadline) {
		list, _ := st.ListApprovals(context.Background(), org, store.ApprovalPending, 10)
		if len(list) == 1 {
			id = list[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("pending approval was never created")
	}
	cancel()

	got := <-done
	if got.err != nil || got.out.Result != Cancelled || got.out.ID != id || !got.out.Withdrawn {
		t.Fatalf("gate after cancel: %+v err=%v", got.out, got.err)
	}
	a, err := st.GetApproval(context.Background(), org, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != store.ApprovalWithdrawn || a.DecidedBy != "caller cancelled" || len(a.Args) != 0 {
		t.Fatalf("approval row after cancel: %+v", a)
	}
	if err := s.Decide(context.Background(), org, id, approver, true); !errors.Is(err, ErrNotPending) {
		t.Fatalf("decide after cancel: %v, want ErrNotPending", err)
	}
	// A subsequent call asks anew cleanly.
	s.Hold = 30 * time.Millisecond
	next, err := s.Gate(context.Background(), req(org, "cancel-hold"))
	if err != nil || next.Result != Pending || next.ID == id {
		t.Fatalf("retry after cancel: %+v err=%v", next, err)
	}
}

func TestCancelDoesNotOverwriteConcurrentApproval(t *testing.T) {
	s, st, org := setup(t)
	s.Hold = 5 * time.Second
	s.Poll = 2 * time.Second // ensure cancel fires before wait polls again

	ctx, cancel := context.WithCancel(context.Background())
	type res struct {
		out Outcome
		err error
	}
	done := make(chan res, 1)
	go func() {
		out, err := s.Gate(ctx, req(org, "race-approve"))
		done <- res{out, err}
	}()

	var id string
	deadline := time.Now().Add(2 * time.Second)
	for id == "" && time.Now().Before(deadline) {
		list, _ := st.ListApprovals(context.Background(), org, store.ApprovalPending, 10)
		if len(list) == 1 {
			id = list[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("pending approval was never created")
	}

	// Approver decides first, then caller cancels while wait is sleeping on Poll.
	if err := s.Decide(context.Background(), org, id, approver, true); err != nil {
		t.Fatal(err)
	}
	cancel()

	got := <-done
	if got.err != nil || got.out.Result != Cancelled || got.out.Withdrawn {
		t.Fatalf("gate after concurrent approve+cancel: %+v err=%v", got.out, got.err)
	}
	// Crucial invariant: the approval stays 'approved' (neither withdrawn nor consumed).
	a, err := st.GetApproval(context.Background(), org, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != store.ApprovalApproved || len(a.Args) == 0 {
		t.Fatalf("approval row corrupted: status=%q argsLen=%d", a.Status, len(a.Args))
	}
	// A retry with a live context consumes the untouched approval and runs.
	ran, err := s.Gate(context.Background(), req(org, "race-approve"))
	if err != nil || ran.Result != Approved || ran.ID != id {
		t.Fatalf("retry after concurrent approve+cancel: %+v err=%v", ran, err)
	}

	// Also verify concurrent denial is preserved (not overwritten to withdrawn).
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() {
		out, err := s.Gate(ctx2, req(org, "race-deny"))
		done <- res{out, err}
	}()
	var denyID string
	deadline = time.Now().Add(2 * time.Second)
	for denyID == "" && time.Now().Before(deadline) {
		list, _ := st.ListApprovals(context.Background(), org, store.ApprovalPending, 10)
		if len(list) == 1 {
			denyID = list[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	if denyID == "" {
		t.Fatal("second pending approval never created")
	}
	if err := s.Decide(context.Background(), org, denyID, approver, false); err != nil {
		t.Fatal(err)
	}
	cancel2()
	got2 := <-done
	if got2.err != nil || got2.out.Result != Denied || got2.out.Withdrawn {
		t.Fatalf("gate after concurrent deny+cancel: %+v err=%v", got2.out, got2.err)
	}
	denied, _ := st.GetApproval(context.Background(), org, denyID)
	if denied.Status != store.ApprovalDenied {
		t.Fatalf("denied row overwritten to %q", denied.Status)
	}
}

func TestCancelReleasesHoldSlot(t *testing.T) {
	s, st, org := setup(t)
	s.Hold = 5 * time.Second
	s.Poll = 10 * time.Millisecond

	cancels := make([]context.CancelFunc, MaxHoldsPerRequester)
	var wg sync.WaitGroup
	for i := 0; i < MaxHoldsPerRequester; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _ = s.Gate(ctx, req(org, "slot-"+string(rune('a'+idx))))
		}(i)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := s.holds["tokA"]
		s.mu.Unlock()
		if n == MaxHoldsPerRequester {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Non-held call while slots are full returns immediately without holding
	// and cancelling its context afterwards does not withdraw existing pending approvals.
	start := time.Now()
	extraCtx, extraCancel := context.WithCancel(context.Background())
	out, err := s.Gate(extraCtx, req(org, "slot-a"))
	extraCancel()
	if err != nil || out.Result != Pending || time.Since(start) > time.Second {
		t.Fatalf("non-held retry: %+v err=%v took=%v", out, err, time.Since(start))
	}
	if got, _ := st.GetApproval(context.Background(), org, out.ID); got.Status != store.ApprovalPending {
		t.Fatalf("non-held retry changed approval status to %q", got.Status)
	}

	// Cancelling one held call frees a hold slot so a new call can hold until decided.
	cancels[0]()
	deadline2 := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline2) {
		s.mu.Lock()
		n := s.holds["tokA"]
		s.mu.Unlock()
		if n < MaxHoldsPerRequester {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	type res struct {
		out Outcome
		err error
	}
	reusedDone := make(chan res, 1)
	go func() {
		afterOut, afterErr := s.Gate(context.Background(), req(org, "slot-reused"))
		reusedDone <- res{afterOut, afterErr}
	}()
	var reusedID string
	deadline3 := time.Now().Add(2 * time.Second)
	for reusedID == "" && time.Now().Before(deadline3) {
		list, _ := st.ListApprovals(context.Background(), org, store.ApprovalPending, 50)
		for _, a := range list {
			if a.ArgsDigest == "slot-reused" {
				reusedID = a.ID
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if reusedID == "" {
		t.Fatal("slot-reused approval was never created")
	}
	if err := s.Decide(context.Background(), org, reusedID, approver, true); err != nil {
		t.Fatal(err)
	}
	reused := <-reusedDone
	if reused.err != nil || reused.out.Result != Approved || reused.out.ID != reusedID {
		t.Fatalf("call after freed slot did not hold for decision: %+v err=%v", reused.out, reused.err)
	}

	for _, c := range cancels[1:] {
		c()
	}
	wg.Wait()
	s.mu.Lock()
	remaining := s.holds["tokA"]
	s.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("leaked %d hold slots after all calls finished", remaining)
	}
}

type cancelDuringCreateStore struct {
	*store.Store
	cancel            context.CancelFunc
	failCreate        bool
	skipCreateInsert  bool
	failSecondOpenCtx bool
	openCalls         int
}

func (c *cancelDuringCreateStore) CreateApproval(_ context.Context, org store.OrgID, a store.Approval) error {
	if c.failCreate {
		c.cancel()
		if !c.skipCreateInsert {
			if err := c.Store.CreateApproval(context.Background(), org, a); err != nil {
				return err
			}
		}
		return context.Canceled
	}
	return c.Store.CreateApproval(context.Background(), org, a)
}

func (c *cancelDuringCreateStore) OpenApproval(ctx context.Context, org store.OrgID, b store.ApprovalBinding, now time.Time) (store.Approval, error) {
	c.openCalls++
	if c.failSecondOpenCtx && c.openCalls == 2 {
		c.cancel()
		return store.Approval{}, context.Canceled
	}
	return c.Store.OpenApproval(ctx, org, b, now)
}

func TestGateWithdrawsIfCancelledDuringCreate(t *testing.T) {
	t.Run("cancelled after insert in CreateApproval", func(t *testing.T) {
		svc, st, org := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		svc.Store = &cancelDuringCreateStore{Store: st, cancel: cancel, failCreate: true}

		out, err := svc.Gate(ctx, req(org, "cancel-create"))
		if err != nil {
			t.Fatalf("Gate err = %v, want nil with Cancelled outcome", err)
		}
		if out.Result != Cancelled || !out.Withdrawn || out.ID == "" {
			t.Fatalf("Gate out = %+v, want Result=Cancelled Withdrawn=true", out)
		}
		got, gerr := st.GetApproval(context.Background(), org, out.ID)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if got.Status != store.ApprovalWithdrawn || len(got.Args) != 0 || got.DecidedBy != "caller cancelled" {
			t.Fatalf("approval row = %+v, want withdrawn with purged args", got)
		}
	})

	t.Run("cancelled during post-create OpenApproval", func(t *testing.T) {
		svc, st, org := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		svc.Store = &cancelDuringCreateStore{Store: st, cancel: cancel, failSecondOpenCtx: true}

		out, err := svc.Gate(ctx, req(org, "cancel-open"))
		if err != nil {
			t.Fatalf("Gate err = %v, want nil with Cancelled outcome", err)
		}
		if out.Result != Cancelled || !out.Withdrawn || out.ID == "" {
			t.Fatalf("Gate out = %+v, want Result=Cancelled Withdrawn=true", out)
		}
		got, gerr := st.GetApproval(context.Background(), org, out.ID)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if got.Status != store.ApprovalWithdrawn || len(got.Args) != 0 || got.DecidedBy != "caller cancelled" {
			t.Fatalf("approval row = %+v, want withdrawn with purged args", got)
		}
	})

	t.Run("cancelled before CreateApproval commits returns error", func(t *testing.T) {
		svc, st, org := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		svc.Store = &cancelDuringCreateStore{Store: st, cancel: cancel, failCreate: true, skipCreateInsert: true}

		_, err := svc.Gate(ctx, req(org, "cancel-before-insert"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Gate err = %v, want context.Canceled", err)
		}
	})
}
