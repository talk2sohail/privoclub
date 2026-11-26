package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Approval statuses (ADR 0005). Expiry is computed from expires_at.
const (
	ApprovalPending   = "pending"
	ApprovalApproved  = "approved"
	ApprovalDenied    = "denied"
	ApprovalWithdrawn = "withdrawn"
	ApprovalConsumed  = "consumed"
	ApprovalExpired   = "expired" // derived from expires_at; also stored once purged
)

// Approval is a request for a person to allow one exact call.
type Approval struct {
	ID              string
	RequesterID     string
	RequesterName   string
	RequesterPerson string
	RequesterUserID string // the member who asked; "" for a token of nobody (F05 R-04)
	ServerID        string
	ServerAlias     string
	Tool            string
	ArgsDigest      string
	Args            []byte // encrypted
	Reason          string
	RuleID          string // the rule that asked; "" when the org default did
	PinHash         string // the reviewed definition the call was for
	Status          string
	DecidedBy       string
	DecidedAt       *time.Time
	CreatedAt       time.Time
	ExpiresAt       time.Time
	ConsumedAt      *time.Time
}

// EffectiveStatus folds expiry into the stored status.
func (a Approval) EffectiveStatus(now time.Time) string {
	if (a.Status == ApprovalPending || a.Status == ApprovalApproved || a.Status == ApprovalDenied) && !now.Before(a.ExpiresAt) {
		return ApprovalExpired
	}
	return a.Status
}

// ApprovalBinding identifies the exact call an approval covers.
type ApprovalBinding struct {
	RequesterID string
	ServerID    string
	Tool        string
	ArgsDigest  string
}

// CreateApproval stores a pending approval.
func (s *Store) CreateApproval(ctx context.Context, org OrgID, a Approval) error {
	_, err := s.w.ExecContext(ctx, `
		INSERT INTO approvals (org_id, id, requester_id, requester_name, requester_person, requester_user_id, server_id, server_alias, tool, args_digest, args, reason, rule_id, pin_hash, status, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)
		ON CONFLICT DO NOTHING`,
		org, a.ID, a.RequesterID, a.RequesterName, a.RequesterPerson, a.RequesterUserID, a.ServerID, a.ServerAlias, a.Tool, a.ArgsDigest, a.Args, a.Reason, a.RuleID, a.PinHash,
		ts(a.CreatedAt), ts(a.ExpiresAt))
	return mapErr(err)
}

// ExpireStale marks open approvals of a binding whose time has passed as
// expired and drops their stored arguments, so they stop blocking the binding.
func (s *Store) ExpireStale(ctx context.Context, org OrgID, b ApprovalBinding, now time.Time) error {
	_, err := s.w.ExecContext(ctx, `
		UPDATE approvals SET status = 'expired', args = x''
		WHERE org_id = ? AND requester_id = ? AND server_id = ? AND tool = ? AND args_digest = ?
		  AND status IN ('pending', 'approved', 'denied') AND expires_at <= ?`,
		org, b.RequesterID, b.ServerID, b.Tool, b.ArgsDigest, ts(now))
	return err
}

// CountPending returns a requester's unexpired pending approvals.
func (s *Store) CountPending(ctx context.Context, org OrgID, requesterID string, now time.Time) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `
		SELECT count(*) FROM approvals WHERE org_id = ? AND requester_id = ? AND status = 'pending' AND expires_at > ?`,
		org, requesterID, ts(now)).Scan(&n)
	return n, err
}

// RestoreApproval undoes a consume when the call could not start.
func (s *Store) RestoreApproval(ctx context.Context, org OrgID, id string) error {
	_, err := s.w.ExecContext(ctx, `
		UPDATE approvals SET status = 'approved', consumed_at = NULL WHERE org_id = ? AND id = ? AND status = 'consumed'`, org, id)
	return err
}

const approvalCols = `id, requester_id, requester_name, requester_person, requester_user_id, server_id, server_alias, tool, args_digest, args, reason, rule_id, pin_hash, status,
	decided_by, decided_at, created_at, expires_at, consumed_at`

func scanApproval(sc interface{ Scan(...any) error }) (Approval, error) {
	var a Approval
	var decidedAt, consumedAt sql.NullString
	var created, expires string
	err := sc.Scan(&a.ID, &a.RequesterID, &a.RequesterName, &a.RequesterPerson, &a.RequesterUserID, &a.ServerID, &a.ServerAlias, &a.Tool, &a.ArgsDigest, &a.Args,
		&a.Reason, &a.RuleID, &a.PinHash, &a.Status, &a.DecidedBy, &decidedAt, &created, &expires, &consumedAt)
	if err != nil {
		return a, err
	}
	a.CreatedAt, a.ExpiresAt = parseTS(created), parseTS(expires)
	if decidedAt.Valid {
		t := parseTS(decidedAt.String)
		a.DecidedAt = &t
	}
	if consumedAt.Valid {
		t := parseTS(consumedAt.String)
		a.ConsumedAt = &t
	}
	return a, nil
}

// GetApproval returns one approval. It reads through the read pool, so
// polling never competes with the single writer.
func (s *Store) GetApproval(ctx context.Context, org OrgID, id string) (Approval, error) {
	a, err := scanApproval(s.r.QueryRowContext(ctx, `SELECT `+approvalCols+` FROM approvals WHERE org_id = ? AND id = ?`, org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// OpenApproval returns the newest unexpired pending, approved or denied
// approval for a binding. A denial stands until it expires, so retrying the
// identical call does not ask again.
func (s *Store) OpenApproval(ctx context.Context, org OrgID, b ApprovalBinding, now time.Time) (Approval, error) {
	a, err := scanApproval(s.w.QueryRowContext(ctx, `
		SELECT `+approvalCols+` FROM approvals
		WHERE org_id = ? AND requester_id = ? AND server_id = ? AND tool = ? AND args_digest = ?
		  AND status IN ('pending', 'approved', 'denied') AND expires_at > ?
		ORDER BY CASE status WHEN 'denied' THEN 0 WHEN 'approved' THEN 1 ELSE 2 END, created_at DESC LIMIT 1`,
		org, b.RequesterID, b.ServerID, b.Tool, b.ArgsDigest, ts(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// ConsumeApproval atomically marks an approved, unexpired approval as used.
// It returns false when another call consumed it first or it is not approved.
func (s *Store) ConsumeApproval(ctx context.Context, org OrgID, id string, now time.Time) (bool, error) {
	res, err := s.w.ExecContext(ctx, `
		UPDATE approvals SET status = 'consumed', consumed_at = ?
		WHERE org_id = ? AND id = ? AND status = 'approved' AND consumed_at IS NULL AND expires_at > ?`,
		ts(now), org, id, ts(now))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DecideApproval moves a pending, unexpired approval to approved or denied.
func (s *Store) DecideApproval(ctx context.Context, org OrgID, id, status, by string, now time.Time) (bool, error) {
	if status != ApprovalApproved && status != ApprovalDenied && status != ApprovalWithdrawn {
		return false, errors.New("store: invalid approval decision")
	}
	res, err := s.w.ExecContext(ctx, `
		UPDATE approvals SET status = ?, decided_by = ?, decided_at = ?
		WHERE org_id = ? AND id = ? AND status = 'pending' AND expires_at > ?`,
		status, by, ts(now), org, id, ts(now))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ListApprovals returns an org's approvals, newest first, optionally filtered
// by stored status.
func (s *Store) ListApprovals(ctx context.Context, org OrgID, status string, limit int) ([]Approval, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + approvalCols + ` FROM approvals WHERE org_id = ?`
	args := []any{org}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PurgeArguments drops the stored arguments of an approval that can no
// longer run (consumed, denied or expired).
func (s *Store) PurgeArguments(ctx context.Context, org OrgID, id string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE approvals SET args = x'' WHERE org_id = ? AND id = ? AND status <> 'pending' AND status <> 'approved'`, org, id)
	return err
}

// Approval views for ListApprovalsPage.
const (
	ViewPending = "pending" // waiting for a decision and not expired
	ViewHistory = "history" // everything else: decided, withdrawn, used or expired
)

// ApprovalPage is one page of approvals, newest first. Next is the cursor of
// the following page, "" on the last.
type ApprovalPage struct {
	Approvals []Approval
	Next      string
}

// ListApprovalsPage returns a page of an org's pending approvals or of its
// history. before is the Next of the previous page ("" for the first);
// limit is 1 to 200 (default 50).
func (s *Store) ListApprovalsPage(ctx context.Context, org OrgID, view, before string, limit int, now time.Time) (ApprovalPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + approvalCols + ` FROM approvals WHERE org_id = ?`
	args := []any{org}
	switch view {
	case ViewPending:
		q += ` AND status = 'pending' AND expires_at > ?`
	case ViewHistory:
		q += ` AND NOT (status = 'pending' AND expires_at > ?)`
	default:
		return ApprovalPage{}, errors.New("store: unknown approval view")
	}
	args = append(args, ts(now))
	if before != "" {
		at, id, ok := strings.Cut(before, "|")
		if !ok || id == "" || parseTS(at).IsZero() {
			return ApprovalPage{}, ErrBadCursor
		}
		q += ` AND (julianday(created_at) < julianday(?) OR (julianday(created_at) = julianday(?) AND id < ?))`
		args = append(args, at, at, id)
	}
	q += ` ORDER BY julianday(created_at) DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return ApprovalPage{}, err
	}
	defer func() { _ = rows.Close() }()
	var page ApprovalPage
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return ApprovalPage{}, err
		}
		page.Approvals = append(page.Approvals, a)
	}
	if err := rows.Err(); err != nil {
		return ApprovalPage{}, err
	}
	if len(page.Approvals) > limit {
		page.Approvals = page.Approvals[:limit]
		last := page.Approvals[limit-1]
		page.Next = ts(last.CreatedAt) + "|" + last.ID
	}
	return page, nil
}

// ErrBadCursor is returned for a page cursor this store did not make.
var ErrBadCursor = errors.New("store: unknown page cursor")

// MyApprovals returns the pending, unexpired approvals asked for with any
// token bound to the member userID, newest first (at most 50).
func (s *Store) MyApprovals(ctx context.Context, org OrgID, userID string, now time.Time) ([]Approval, error) {
	if userID == "" {
		return nil, nil
	}
	rows, err := s.r.QueryContext(ctx, `
		SELECT `+approvalCols+` FROM approvals
		WHERE org_id = ? AND status = 'pending' AND expires_at > ?
		  AND requester_id IN (SELECT id FROM api_tokens WHERE org_id = ? AND user_id = ?)
		ORDER BY julianday(created_at) DESC, id DESC LIMIT 50`, org, ts(now), org, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// WithdrawApproval withdraws a pending, unexpired approval that was asked for
// with a token bound to the member userID, records by and drops its
// arguments. It returns false when there is no such approval.
func (s *Store) WithdrawApproval(ctx context.Context, org OrgID, id, userID, by string, now time.Time) (bool, error) {
	if userID == "" {
		return false, nil
	}
	res, err := s.w.ExecContext(ctx, `
		UPDATE approvals SET status = 'withdrawn', decided_by = ?, decided_at = ?, args = x''
		WHERE org_id = ? AND id = ? AND status = 'pending' AND expires_at > ?
		  AND requester_id IN (SELECT id FROM api_tokens WHERE org_id = ? AND user_id = ?)`,
		by, ts(now), org, id, ts(now), org, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CancelPendingApproval atomically withdraws a pending, unexpired approval when
// the held caller cancels, recording by and dropping its stored arguments. It
// returns false if the approval was already decided, withdrawn, consumed or
// expired.
func (s *Store) CancelPendingApproval(ctx context.Context, org OrgID, id, by string, now time.Time) (bool, error) {
	res, err := s.w.ExecContext(ctx, `
		UPDATE approvals SET status = 'withdrawn', decided_by = ?, decided_at = ?, args = x''
		WHERE org_id = ? AND id = ? AND status = 'pending' AND expires_at > ?`,
		by, ts(now), org, id, ts(now))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
