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
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, e := range events {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO audit_events (org_id, id, call_id, phase, ts, actor, actor_id, client, server, item, action, decision, approved_by, outcome, latency_ms, payload)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				org, e.ID, e.CallID, e.Phase, ts(e.Time), e.Actor, e.ActorID, e.Client, e.Server, e.Item, e.Action,
				e.Decision, e.ApprovedBy, e.Outcome, e.LatencyMS, e.Payload)
			if err != nil {
				return err
			}
		}
		return nil
	})
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
		SELECT i.id, i.call_id, i.ts, i.actor, i.actor_id, i.client, i.server, i.item, i.action, i.decision, i.approved_by, i.payload,
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
		if err := rows.Scan(&i.ID, &i.CallID, &its, &i.Actor, &i.ActorID, &i.Client, &i.Server, &i.Item, &i.Action, &i.Decision, &i.ApprovedBy, &i.Payload,
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
