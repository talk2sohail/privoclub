package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AuditEvent is one audit row (ADR 0006).
type AuditEvent struct {
	ID         string
	CallID     string
	Phase      string // intent | completion | admin
	Time       time.Time
	Actor      string
	ActorID    string
	UserID     string // the member whose account and teams a call ran as; "" for a token of nobody (F27)
	Client     string
	Server     string
	Item       string // upstream tool, prompt or resource name
	Action     string // tools/call, resources/read, prompts/get, or an admin action
	Decision   string
	ApprovedBy string
	Outcome    string
	LatencyMS  int64
	Payload    []byte // encrypted arguments when recording was on
}

// InsertAudit appends audit events in one transaction.
func (s *Store) InsertAudit(ctx context.Context, org OrgID, events ...AuditEvent) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return insertAudit(ctx, tx, org, events) })
}

// insertAudit appends events inside a caller's transaction, so a state change
// and its audit record commit or roll back together.
func insertAudit(ctx context.Context, tx *sql.Tx, org OrgID, events []AuditEvent) error {
	for _, e := range events {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO audit_events (org_id, id, call_id, phase, ts, actor, actor_id, user_id, client, server, item, action, decision, approved_by, outcome, latency_ms, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			org, e.ID, e.CallID, e.Phase, ts(e.Time), e.Actor, e.ActorID, e.UserID, e.Client, e.Server, e.Item, e.Action,
			e.Decision, e.ApprovedBy, e.Outcome, e.LatencyMS, e.Payload)
		if err != nil {
			return err
		}
	}
	return nil
}

// CallRecord joins a call's intent with its completion, if any.
type CallRecord struct {
	Intent     AuditEvent
	Completion *AuditEvent
}

// RecentCalls returns the newest calls first.
func (s *Store) RecentCalls(ctx context.Context, org OrgID, limit int) ([]CallRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.r.QueryContext(ctx, `
		SELECT i.id, i.call_id, i.ts, i.actor, i.actor_id, i.user_id, i.client, i.server, i.item, i.action, i.decision, i.approved_by, i.payload,
		       c.id, c.ts, c.outcome, c.latency_ms
		FROM audit_events i
		LEFT JOIN audit_events c ON c.org_id = i.org_id AND c.call_id = i.call_id AND c.phase = 'completion'
		WHERE i.org_id = ? AND i.phase = 'intent'
		ORDER BY i.ts DESC, i.id DESC LIMIT ?`, org, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CallRecord
	for rows.Next() {
		var r CallRecord
		var its string
		var cid, cts, cout sql.NullString
		var clat sql.NullInt64
		i := &r.Intent
		if err := rows.Scan(&i.ID, &i.CallID, &its, &i.Actor, &i.ActorID, &i.UserID, &i.Client, &i.Server, &i.Item, &i.Action, &i.Decision, &i.ApprovedBy, &i.Payload,
			&cid, &cts, &cout, &clat); err != nil {
			return nil, err
		}
		i.Phase, i.Time = "intent", parseTS(its)
		if cid.Valid {
			r.Completion = &AuditEvent{ID: cid.String, CallID: i.CallID, Phase: "completion", Time: parseTS(cts.String),
				Outcome: cout.String, LatencyMS: clat.Int64}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecentAdminEvents returns the newest admin-phase events first: what the
// control API did (tokens, servers, policies, settings, approvals) and the
// bootstrap install. RecentCalls never returns them (R-09, independent review
// 2026-10-29).
func (s *Store) RecentAdminEvents(ctx context.Context, org OrgID, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.r.QueryContext(ctx, `
		SELECT id, call_id, ts, actor, actor_id, client, server, action, decision, outcome
		FROM audit_events
		WHERE org_id = ? AND phase = 'admin'
		ORDER BY ts DESC, id DESC LIMIT ?`, org, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AuditEvent
	for rows.Next() {
		e := AuditEvent{Phase: "admin"}
		var at string
		if err := rows.Scan(&e.ID, &e.CallID, &at, &e.Actor, &e.ActorID, &e.Client, &e.Server, &e.Action, &e.Decision, &e.Outcome); err != nil {
			return nil, err
		}
		e.Time = parseTS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Setting reads a per-org setting; def is returned when unset.
func (s *Store) Setting(ctx context.Context, org OrgID, key, def string) (string, error) {
	var v string
	err := s.r.QueryRowContext(ctx, `SELECT value FROM settings WHERE org_id = ? AND key = ?`, org, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	return v, err
}

// SetSetting writes a per-org setting.
func (s *Store) SetSetting(ctx context.Context, org OrgID, key, value string) error {
	_, err := s.w.ExecContext(ctx, `
		INSERT INTO settings (org_id, key, value) VALUES (?, ?, ?)
		ON CONFLICT (org_id, key) DO UPDATE SET value = excluded.value`, org, key, value)
	return err
}
