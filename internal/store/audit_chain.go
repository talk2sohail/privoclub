package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// The audit chain (ADR 0006, 0025). The store keeps blocks and places; the
// audit package computes hashes and signatures.

// AuditBlock is one sealed block of an org's chain.
type AuditBlock struct {
	Seq       int64
	FirstSeq  int64
	LastSeq   int64
	PrevHash  string // hex; "" for the first block
	Hash      string // hex
	KeyID     string
	PublicKey string // base64
	Signature string // base64
	SealedAt  time.Time
}

// ChainedEvent is an audit event with its place in the chain.
type ChainedEvent struct {
	AuditEvent
	Seq           int64
	PayloadSHA256 string // hex SHA-256 of the payload when it was sealed
}

const chainCols = `id, call_id, phase, ts, actor, actor_id, client, server, item, action, decision, approved_by, account, outcome, latency_ms, payload,
	COALESCE(chain_seq, 0), payload_sha256`

func scanChained(sc interface{ Scan(...any) error }) (ChainedEvent, error) {
	var e ChainedEvent
	var at string
	err := sc.Scan(&e.ID, &e.CallID, &e.Phase, &at, &e.Actor, &e.ActorID, &e.Client, &e.Server, &e.Item, &e.Action, &e.Decision, &e.ApprovedBy,
		&e.Account, &e.Outcome, &e.LatencyMS, &e.Payload, &e.Seq, &e.PayloadSHA256)
	e.Time = parseTS(at)
	return e, err
}

// PayloadDigest is the hex SHA-256 the chain keeps for a payload.
func PayloadDigest(p []byte) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:])
}

// SealAudit seals up to limit unsealed events of an org, oldest first, into
// one block, in one transaction. seal gets the previous block (zero for the
// first) and the events with their sequence numbers and payload digests set;
// it returns the block to store, with Hash, KeyID, PublicKey and Signature.
// It returns how many events were sealed (0 when none were waiting).
func (s *Store) SealAudit(ctx context.Context, org OrgID, limit int, now time.Time, seal func(prev AuditBlock, evs []ChainedEvent) (AuditBlock, error)) (int, error) {
	if limit <= 0 {
		limit = 1000
	}
	n := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		prev, err := lastBlock(ctx, tx, org)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+chainCols+` FROM audit_events WHERE org_id = ? AND chain_seq IS NULL
			ORDER BY julianday(ts), id LIMIT ?`, org, limit)
		if err != nil {
			return err
		}
		var evs []ChainedEvent
		for rows.Next() {
			e, err := scanChained(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			evs = append(evs, e)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(evs) == 0 {
			return nil
		}
		next := prev.LastSeq + 1
		for i := range evs {
			evs[i].Seq = next + int64(i)
			evs[i].PayloadSHA256 = PayloadDigest(evs[i].Payload)
		}
		b, err := seal(prev, evs)
		if err != nil {
			return err
		}
		b.Seq, b.FirstSeq, b.LastSeq, b.PrevHash, b.SealedAt = prev.Seq+1, evs[0].Seq, evs[len(evs)-1].Seq, prev.Hash, now
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_blocks (org_id, seq, first_seq, last_seq, prev_hash, hash, key_id, public_key, signature, sealed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, org, b.Seq, b.FirstSeq, b.LastSeq, b.PrevHash, b.Hash, b.KeyID, b.PublicKey, b.Signature, ts(now)); err != nil {
			return err
		}
		for _, e := range evs {
			if _, err := tx.ExecContext(ctx, `UPDATE audit_events SET chain_seq = ?, payload_sha256 = ? WHERE org_id = ? AND id = ? AND chain_seq IS NULL`,
				e.Seq, e.PayloadSHA256, org, e.ID); err != nil {
				return err
			}
		}
		n = len(evs)
		return nil
	})
	return n, err
}

func lastBlock(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, org OrgID) (AuditBlock, error) {
	var b AuditBlock
	var at string
	err := q.QueryRowContext(ctx, `SELECT seq, first_seq, last_seq, prev_hash, hash, key_id, public_key, signature, sealed_at
		FROM audit_blocks WHERE org_id = ? ORDER BY seq DESC LIMIT 1`, org).
		Scan(&b.Seq, &b.FirstSeq, &b.LastSeq, &b.PrevHash, &b.Hash, &b.KeyID, &b.PublicKey, &b.Signature, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditBlock{}, nil
	}
	b.SealedAt = parseTS(at)
	return b, err
}

// AuditBlocks returns an org's blocks in order, from the block after
// afterSeq, at most limit (0 for all).
func (s *Store) AuditBlocks(ctx context.Context, org OrgID, afterSeq int64, limit int) ([]AuditBlock, error) {
	q := `SELECT seq, first_seq, last_seq, prev_hash, hash, key_id, public_key, signature, sealed_at FROM audit_blocks WHERE org_id = ? AND seq > ? ORDER BY seq`
	args := []any{org, afterSeq}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AuditBlock
	for rows.Next() {
		var b AuditBlock
		var at string
		if err := rows.Scan(&b.Seq, &b.FirstSeq, &b.LastSeq, &b.PrevHash, &b.Hash, &b.KeyID, &b.PublicKey, &b.Signature, &at); err != nil {
			return nil, err
		}
		b.SealedAt = parseTS(at)
		out = append(out, b)
	}
	return out, rows.Err()
}

// AuditEventsInRange returns the sealed events with chain_seq in [first, last], in order.
func (s *Store) AuditEventsInRange(ctx context.Context, org OrgID, first, last int64) ([]ChainedEvent, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+chainCols+` FROM audit_events WHERE org_id = ? AND chain_seq BETWEEN ? AND ? ORDER BY chain_seq`, org, first, last)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ChainedEvent
	for rows.Next() {
		e, err := scanChained(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountUnsealed returns how many events wait for the sealer.
func (s *Store) CountUnsealed(ctx context.Context, org OrgID) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE org_id = ? AND chain_seq IS NULL`, org).Scan(&n)
	return n, err
}

// PruneAudit deletes sealed events older than before (retention). Unsealed
// events and blocks are never deleted. It returns how many rows went.
func (s *Store) PruneAudit(ctx context.Context, org OrgID, before time.Time) (int, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM audit_events WHERE org_id = ? AND chain_seq IS NOT NULL AND julianday(ts) < julianday(?)`, org, ts(before))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// EraseAuditPayloads clears the recorded payloads of events whose actor id is
// one of actorIDs (GDPR erasure), keeping their digests so the chain still
// verifies, and writes ev. Unsealed events get their digest first. It
// returns how many payloads were erased.
func (s *Store) EraseAuditPayloads(ctx context.Context, org OrgID, actorIDs []string, ev AuditEvent) (int, error) {
	if len(actorIDs) == 0 {
		return 0, nil
	}
	ev.Phase = "admin"
	n := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		in := `actor_id IN (` + placeholders(len(actorIDs)) + `)`
		args := append([]any{org}, anySlice(actorIDs)...)
		// The sealer would digest an empty payload; record the real digest
		// now, so the chain covers what was there.
		rows, err := tx.QueryContext(ctx, `SELECT id, payload FROM audit_events WHERE org_id = ? AND `+in+` AND payload IS NOT NULL AND length(payload) > 0 AND payload_sha256 = ''`, args...)
		if err != nil {
			return err
		}
		type pending struct {
			id      string
			payload []byte
		}
		var ps []pending
		for rows.Next() {
			var p pending
			if err := rows.Scan(&p.id, &p.payload); err != nil {
				_ = rows.Close()
				return err
			}
			ps = append(ps, p)
		}
		_ = rows.Close()
		for _, p := range ps {
			if _, err := tx.ExecContext(ctx, `UPDATE audit_events SET payload_sha256 = ? WHERE org_id = ? AND id = ?`, PayloadDigest(p.payload), org, p.id); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE audit_events SET payload = NULL WHERE org_id = ? AND `+in+` AND payload IS NOT NULL AND length(payload) > 0`, args...)
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
	return n, err
}

// AuditQuery filters a search of audit events. Empty fields do not filter.
type AuditQuery struct {
	Kind     string // "calls" (intents with their completion) or "admin"
	Actor    string // the actor's name or id, exactly
	Server   string
	Action   string // a prefix of the action
	Decision string // a prefix of the decision
	From, To time.Time
	Before   string // cursor: the Next of the previous page
	Limit    int    // 1 to 1000, default 100
}

// ErrBadAuditQuery is returned for a query this store did not make.
var ErrBadAuditQuery = errors.New("store: bad audit query")

// SearchAudit returns matching events, newest first, and the cursor of the
// next page ("" on the last). For calls, each intent comes with its
// completion's outcome and latency.
func (s *Store) SearchAudit(ctx context.Context, org OrgID, q AuditQuery) ([]CallRecord, string, error) {
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 100
	}
	phase := "intent"
	switch q.Kind {
	case "", "calls":
	case "admin":
		phase = "admin"
	default:
		return nil, "", ErrBadAuditQuery
	}
	where := `i.org_id = ? AND i.phase = ?`
	args := []any{org, phase}
	if q.Actor != "" {
		where += ` AND (i.actor = ? OR i.actor_id = ?)`
		args = append(args, q.Actor, q.Actor)
	}
	if q.Server != "" {
		where += ` AND i.server = ?`
		args = append(args, q.Server)
	}
	if q.Action != "" {
		where += ` AND substr(i.action, 1, ?) = ?`
		args = append(args, len(q.Action), q.Action)
	}
	if q.Decision != "" {
		where += ` AND substr(i.decision, 1, ?) = ?`
		args = append(args, len(q.Decision), q.Decision)
	}
	if !q.From.IsZero() {
		where += ` AND julianday(i.ts) >= julianday(?)`
		args = append(args, ts(q.From))
	}
	if !q.To.IsZero() {
		where += ` AND julianday(i.ts) < julianday(?)`
		args = append(args, ts(q.To))
	}
	if q.Before != "" {
		at, id, ok := strings.Cut(q.Before, "|")
		if !ok || id == "" || parseTS(at).IsZero() {
			return nil, "", ErrBadAuditQuery
		}
		where += ` AND (julianday(i.ts) < julianday(?) OR (julianday(i.ts) = julianday(?) AND i.id < ?))`
		args = append(args, at, at, id)
	}
	args = append(args, q.Limit+1)
	rows, err := s.r.QueryContext(ctx, `
		SELECT i.id, i.call_id, i.ts, i.actor, i.actor_id, i.client, i.server, i.item, i.action, i.decision, i.approved_by, i.account, i.outcome, i.payload,
		       c.id, c.ts, c.outcome, c.latency_ms
		FROM audit_events i
		LEFT JOIN audit_events c ON c.org_id = i.org_id AND c.call_id = i.call_id AND c.phase = 'completion' AND i.phase = 'intent'
		WHERE `+where+`
		ORDER BY julianday(i.ts) DESC, i.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	var out []CallRecord
	for rows.Next() {
		var r CallRecord
		var its string
		var cid, cts, cout sql.NullString
		var clat sql.NullInt64
		i := &r.Intent
		if err := rows.Scan(&i.ID, &i.CallID, &its, &i.Actor, &i.ActorID, &i.Client, &i.Server, &i.Item, &i.Action, &i.Decision, &i.ApprovedBy, &i.Account,
			&i.Outcome, &i.Payload, &cid, &cts, &cout, &clat); err != nil {
			return nil, "", err
		}
		i.Phase, i.Time = phase, parseTS(its)
		if cid.Valid {
			r.Completion = &AuditEvent{ID: cid.String, CallID: i.CallID, Phase: "completion", Time: parseTS(cts.String), Outcome: cout.String, LatencyMS: clat.Int64}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > q.Limit {
		out = out[:q.Limit]
		last := out[q.Limit-1].Intent
		next = ts(last.Time) + "|" + last.ID
	}
	return out, next, nil
}
