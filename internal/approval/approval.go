// Package approval implements human approvals (ADR 0005).
//
// An approval is a durable row bound to the requester, server, tool and the
// digest of the exact arguments. It is single use: the call that runs consumes
// it atomically, and any change to the arguments needs a new approval. A call
// that needs approval is held for a short time; if a person decides within it,
// the call continues. Otherwise the caller is told to retry the same call once
// it is approved, and the retry finds and consumes the approval.
package approval

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// Result of gating a call.
type Result int

// Gate results.
const (
	Approved  Result = iota // the call may run; the approval is consumed
	Pending                 // a person has not decided yet
	Denied                  // a person denied this exact call
	Withdrawn               // the requester took the request back; a retry asks again
	Cancelled               // the waiting caller cancelled; a pending approval was withdrawn
)

// Outcome describes a gate decision.
type Outcome struct {
	Result    Result
	ID        string
	ExpiresAt time.Time
	DecidedBy string
	Withdrawn bool // true when cancelling withdrew the pending approval
}

// Limits that keep one caller from flooding approvers or the database.
const (
	MaxPendingPerRequester = 20
	MaxHoldsPerRequester   = 5
)

// ErrTooManyPending is returned when a requester has too many open requests.
var ErrTooManyPending = errors.New("approval: too many pending approval requests; wait for decisions")

// Store is the store subset the service needs.
type Store interface {
	CreateApproval(ctx context.Context, org store.OrgID, a store.Approval) error
	GetApproval(ctx context.Context, org store.OrgID, id string) (store.Approval, error)
	OpenApproval(ctx context.Context, org store.OrgID, b store.ApprovalBinding, now time.Time) (store.Approval, error)
	ExpireStale(ctx context.Context, org store.OrgID, b store.ApprovalBinding, now time.Time) error
	CountPending(ctx context.Context, org store.OrgID, requesterID string, now time.Time) (int, error)
	RestoreApproval(ctx context.Context, org store.OrgID, id string) error
	PurgeArguments(ctx context.Context, org store.OrgID, id string) error
	WithdrawApproval(ctx context.Context, org store.OrgID, id, userID, by string, now time.Time) (bool, error)
	CancelPendingApproval(ctx context.Context, org store.OrgID, id, by string, now time.Time) (bool, error)
	ConsumeApproval(ctx context.Context, org store.OrgID, id string, now time.Time) (bool, error)
	DecideApproval(ctx context.Context, org store.OrgID, id, status, by string, now time.Time) (bool, error)
	Setting(ctx context.Context, org store.OrgID, key, def string) (string, error)
}

// SettingAllowSelf lets requesters approve their own calls when "true".
const SettingAllowSelf = "approvals.allow_self"

// Service gates calls and records decisions.
type Service struct {
	Store Store
	Keys  *keyring.Keyring
	Hold  time.Duration // how long a call waits for a decision (default 25s)
	TTL   time.Duration // approval lifetime (default 10m)
	Poll  time.Duration // decision polling interval (default 200ms)
	now   func() time.Time

	mu    sync.Mutex
	holds map[string]int // concurrent held calls per requester token
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Request is a call that needs approval.
type Request struct {
	Org             store.OrgID
	RequesterID     string
	RequesterName   string
	RequesterPerson string
	RequesterUserID string // the member who asked; "" for a token of nobody
	ServerID        string
	ServerAlias     string
	Tool            string
	ArgsDigest      string
	Args            json.RawMessage
	Reason          string
	RuleID          string // the rule that asked; "" for the org default
	PinHash         string // the reviewed definition called
}

// Gate consumes a matching approval if one is approved, otherwise creates or
// reuses a pending one and waits up to Hold for a decision.
func (s *Service) Gate(ctx context.Context, r Request) (Outcome, error) {
	b := store.ApprovalBinding{RequesterID: r.RequesterID, ServerID: r.ServerID, Tool: r.Tool, ArgsDigest: r.ArgsDigest}
	a, err := s.Store.OpenApproval(ctx, r.Org, b, s.clock())
	switch {
	case errors.Is(err, store.ErrNotFound):
		if a, err = s.create(ctx, r, b); err != nil {
			if ctx.Err() != nil && a.ID != "" {
				return s.cancelHeld(ctx, r.Org, a)
			}
			return Outcome{}, err
		}
	case err != nil:
		return Outcome{}, err
	}
	if !s.takeHold(r.RequesterID) {
		// Too many calls already waiting for this token: answer at once.
		return s.check(ctx, r.Org, a)
	}
	defer s.releaseHold(r.RequesterID)
	return s.wait(ctx, r.Org, a)
}

func (s *Service) takeHold(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holds == nil {
		s.holds = map[string]int{}
	}
	if s.holds[token] >= MaxHoldsPerRequester {
		return false
	}
	s.holds[token]++
	return true
}

func (s *Service) releaseHold(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holds[token]--; s.holds[token] <= 0 {
		delete(s.holds, token)
	}
}

// check evaluates an approval once, without waiting.
func (s *Service) check(ctx context.Context, org store.OrgID, a store.Approval) (Outcome, error) {
	return s.evaluate(ctx, org, a)
}

// create inserts a pending approval for the binding. The unique index on open
// approvals means two racing callers end up sharing one row.
func (s *Service) create(ctx context.Context, r Request, b store.ApprovalBinding) (store.Approval, error) {
	now := s.clock()
	if err := s.Store.ExpireStale(ctx, r.Org, b, now); err != nil {
		return store.Approval{}, err
	}
	if n, err := s.Store.CountPending(ctx, r.Org, r.RequesterID, now); err != nil {
		return store.Approval{}, err
	} else if n >= MaxPendingPerRequester {
		return store.Approval{}, ErrTooManyPending
	}
	ttl := s.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	a := store.Approval{ID: ids.New(), RequesterID: r.RequesterID, RequesterName: r.RequesterName, RequesterPerson: r.RequesterPerson, RequesterUserID: r.RequesterUserID, ServerID: r.ServerID,
		ServerAlias: r.ServerAlias, Tool: r.Tool, ArgsDigest: r.ArgsDigest, Reason: r.Reason, RuleID: r.RuleID, PinHash: r.PinHash,
		Status: store.ApprovalPending, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	sealed, err := s.seal(ctx, r.Org, a.ID, r.Args)
	if err != nil {
		return store.Approval{}, err
	}
	a.Args = sealed
	if err := s.Store.CreateApproval(ctx, r.Org, a); err != nil {
		return a, err
	}
	// Re-read: if another caller created the row first, use theirs.
	got, err := s.Store.OpenApproval(ctx, r.Org, b, now)
	if err != nil {
		return a, err
	}
	return got, nil
}

// evaluate returns the outcome for the approval's current state, consuming it
// if it is approved.
func (s *Service) evaluate(ctx context.Context, org store.OrgID, a store.Approval) (Outcome, error) {
	out, _, err := s.step(ctx, org, a)
	return out, err
}

// step is evaluate plus whether the state is final (not pending).
func (s *Service) step(ctx context.Context, org store.OrgID, a store.Approval) (Outcome, bool, error) {
	switch a.EffectiveStatus(s.clock()) {
	case store.ApprovalApproved:
		ok, err := s.Store.ConsumeApproval(ctx, org, a.ID, s.clock())
		if err != nil {
			return Outcome{}, true, err
		}
		if ok {
			purgeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = s.Store.PurgeArguments(purgeCtx, org, a.ID)
			cancel()
			return Outcome{Result: Approved, ID: a.ID, ExpiresAt: a.ExpiresAt, DecidedBy: a.DecidedBy}, true, nil
		}
		return Outcome{Result: Pending, ID: a.ID, ExpiresAt: a.ExpiresAt}, true, nil
	case store.ApprovalDenied:
		return Outcome{Result: Denied, ID: a.ID, DecidedBy: a.DecidedBy}, true, nil
	case store.ApprovalWithdrawn:
		return Outcome{Result: Withdrawn, ID: a.ID}, true, nil
	case store.ApprovalExpired, store.ApprovalConsumed:
		return Outcome{Result: Pending, ID: a.ID, ExpiresAt: a.ExpiresAt}, true, nil
	}
	return Outcome{Result: Pending, ID: a.ID, ExpiresAt: a.ExpiresAt}, false, nil
}

// Restore puts back an approval that was consumed for a call that then could
// not start (for example, the audit intent could not be written).
func (s *Service) Restore(ctx context.Context, org store.OrgID, id string) error {
	return s.Store.RestoreApproval(ctx, org, id)
}

func (s *Service) wait(ctx context.Context, org store.OrgID, a store.Approval) (Outcome, error) {
	hold, poll := s.Hold, s.Poll
	if hold <= 0 {
		hold = 25 * time.Second
	}
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	// The hold uses real elapsed time; the clock only decides expiry.
	deadline := time.Now().Add(hold)
	for {
		out, final, err := s.step(ctx, org, a)
		if err != nil {
			if ctx.Err() != nil {
				return s.cancelHeld(ctx, org, a)
			}
			return Outcome{}, err
		}
		if final {
			return out, nil
		}
		if !time.Now().Before(deadline) {
			return Outcome{Result: Pending, ID: a.ID, ExpiresAt: a.ExpiresAt}, nil
		}
		t := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return s.cancelHeld(ctx, org, a)
		case <-t.C:
		}
		if a, err = s.Store.GetApproval(ctx, org, a.ID); err != nil {
			if ctx.Err() != nil {
				return s.cancelHeld(ctx, org, a)
			}
			return Outcome{}, err
		}
	}
}

// cancelHeld withdraws a still-pending approval when the caller waiting on it
// cancels. If another actor decided or expired the approval concurrently, that
// decision is preserved and an approved row is never consumed for a gone caller.
func (s *Service) cancelHeld(ctx context.Context, org store.OrgID, a store.Approval) (Outcome, error) {
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	ok, err := s.Store.CancelPendingApproval(cancelCtx, org, a.ID, "caller cancelled", s.clock())
	if err != nil {
		return Outcome{}, err
	}
	if ok {
		return Outcome{Result: Cancelled, ID: a.ID, Withdrawn: true}, nil
	}
	cur, gerr := s.Store.GetApproval(cancelCtx, org, a.ID)
	if gerr != nil {
		if errors.Is(gerr, store.ErrNotFound) {
			return Outcome{}, ctx.Err()
		}
		return Outcome{}, gerr
	}
	switch cur.EffectiveStatus(s.clock()) {
	case store.ApprovalDenied:
		return Outcome{Result: Denied, ID: cur.ID, DecidedBy: cur.DecidedBy}, nil
	case store.ApprovalWithdrawn:
		if cur.DecidedBy == "caller cancelled" {
			return Outcome{Result: Cancelled, ID: cur.ID}, nil
		}
		return Outcome{Result: Withdrawn, ID: cur.ID}, nil
	default:
		return Outcome{Result: Cancelled, ID: cur.ID}, nil
	}
}

// Errors returned by Decide.
var (
	ErrSelfApproval = errors.New("approval: requesters cannot decide their own calls in this organisation")
	ErrNotPending   = errors.New("approval: not pending (already decided or expired)")
)

// Decider identifies the person deciding. A control token sets TokenID; a
// person signed in to the admin site (ADR 0016) has no token and sets UserID.
type Decider struct {
	TokenID string
	UserID  string
	Name    string
	Person  string
}

// label is how the approver is recorded on the approval and on the call's audit
// row: "name (token id)" for a token, "name (user id)" for a signed-in person.
func (d Decider) label() string {
	if d.TokenID == "" && d.UserID != "" {
		return d.Name + " (user " + d.UserID + ")"
	}
	return d.Name + " (" + d.TokenID + ")"
}

// ErrNotYours is returned when withdrawing a request that is not the
// person's own, not pending, or unknown: the three answer alike.
var ErrNotYours = errors.New("approval: no pending request of yours with this id")

// Withdraw takes back a pending request asked for with one of the member's
// own tokens (F10). A held call ends at once; a retry asks again.
func (s *Service) Withdraw(ctx context.Context, org store.OrgID, id string, by Decider) error {
	ok, err := s.Store.WithdrawApproval(ctx, org, id, by.UserID, by.label(), s.clock())
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotYours
	}
	return nil
}

// Own reports whether an approval is the decider's own request: made with
// their token or by their person. Unless the org allows self-approval, they
// cannot decide it.
func Own(by Decider, a store.Approval) bool {
	// By token, by member, and by person name in any case (F05 R-04: a name
	// typed "Alice" on a token and "alice" at sign-in are one person).
	return (by.TokenID != "" && by.TokenID == a.RequesterID) ||
		(by.UserID != "" && by.UserID == a.RequesterUserID) ||
		(by.Person != "" && strings.EqualFold(by.Person, a.RequesterPerson))
}

// Decide approves or denies a pending approval.
func (s *Service) Decide(ctx context.Context, org store.OrgID, id string, by Decider, approve bool) error {
	a, err := s.Store.GetApproval(ctx, org, id)
	if err != nil {
		return err
	}
	if Own(by, a) {
		allow, err := s.Store.Setting(ctx, org, SettingAllowSelf, "false")
		if err != nil {
			return err
		}
		if allow != "true" {
			return ErrSelfApproval
		}
	}
	status := store.ApprovalDenied
	if approve {
		status = store.ApprovalApproved
	}
	ok, err := s.Store.DecideApproval(ctx, org, id, status, by.label(), s.clock())
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotPending
	}
	if !approve {
		_ = s.Store.PurgeArguments(ctx, org, id)
	}
	return nil
}

// Arguments decrypts an approval's arguments for an approver.
func (s *Service) Arguments(ctx context.Context, org store.OrgID, a store.Approval) (json.RawMessage, error) {
	if len(a.Args) < 4 {
		return nil, errors.New("approval: no arguments stored")
	}
	version := binary.BigEndian.Uint32(a.Args[:4])
	dek, err := s.Keys.DEK(ctx, org, version)
	if err != nil {
		return nil, err
	}
	pt, err := vault.Open(dek, a.Args[4:], binding(org, a.ID, version))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(pt), nil
}

func (s *Service) seal(ctx context.Context, org store.OrgID, id string, args json.RawMessage) ([]byte, error) {
	version, dek, err := s.Keys.Current(ctx, org)
	if err != nil {
		return nil, err
	}
	sealed, err := vault.Seal(dek, args, binding(org, id, version))
	if err != nil {
		return nil, err
	}
	return append(binary.BigEndian.AppendUint32(nil, version), sealed...), nil
}

func binding(org store.OrgID, id string, version uint32) vault.Binding {
	return vault.Binding{OrgID: string(org), CredentialID: "approval:" + id, Purpose: "approval.arguments", KeyVersion: version, RowVersion: 1}
}
