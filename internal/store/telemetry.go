package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Call statistics and the diagnostic log (F28, ADR 0024): how calls went,
// apart from the audit. Both are best effort and pruned after a retention.

// Outcomes of a call statistic.
const (
	OutcomeOK        = "ok"
	OutcomeToolError = "tool_error"
	OutcomeError     = "error"
	OutcomeRefused   = "refused"
)

// CallStat is one call's statistic. Durations are microseconds.
type CallStat struct {
	ID          string // the call id the audit uses
	Time        time.Time
	UserID      string
	TokenID     string
	Client      string
	Server      string
	Method      string
	Item        string
	Outcome     string
	ErrorClass  string
	DecideUS    int64 // rules and approval, without WaitUS
	WaitUS      int64 // held for a person's approval (counted in TotalUS, not in latency)
	AuditUS     int64
	ConnectUS   int64
	UpstreamUS  int64
	TotalUS     int64
	ArgsBytes   int64
	ResultBytes int64
}

// Diagnostic levels and categories.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"

	CategoryUpstream  = "upstream"
	CategoryGrant     = "grant"
	CategoryGateway   = "gateway"
	CategoryAudit     = "audit"
	CategoryLifecycle = "lifecycle"
	CategorySignIn    = "signin" // people signing in to Privo (ADR 0034)
)

// Diagnostic is one operational event.
type Diagnostic struct {
	ID       string
	Time     time.Time
	Level    string
	Category string
	Server   string
	UserID   string
	Message  string
	Repeats  int // occurrences this row stands for, at least 1
}

// InsertCallStats appends statistics in one transaction.
func (s *Store) InsertCallStats(ctx context.Context, org OrgID, rows ...CallStat) error {
	if len(rows) == 0 {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		st, err := tx.PrepareContext(ctx, `
			INSERT INTO call_stats (org_id, id, ts_ms, user_id, token_id, client, server, method, item, outcome, error_class,
				decide_us, wait_us, audit_us, connect_us, upstream_us, total_us, args_bytes, result_bytes)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (org_id, id) DO NOTHING`)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		for _, r := range rows {
			if _, err := st.ExecContext(ctx, org, r.ID, r.Time.UnixMilli(), r.UserID, r.TokenID, r.Client, r.Server, r.Method, r.Item,
				r.Outcome, r.ErrorClass, r.DecideUS, r.WaitUS, r.AuditUS, r.ConnectUS, r.UpstreamUS, r.TotalUS, r.ArgsBytes, r.ResultBytes); err != nil {
				return err
			}
		}
		return nil
	})
}

// InsertDiagnostics appends diagnostic events in one transaction.
func (s *Store) InsertDiagnostics(ctx context.Context, org OrgID, rows ...Diagnostic) error {
	if len(rows) == 0 {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, d := range rows {
			if d.Repeats < 1 {
				d.Repeats = 1
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO diagnostics (org_id, id, ts_ms, level, category, server, user_id, message, repeats)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (org_id, id) DO NOTHING`,
				org, d.ID, d.Time.UnixMilli(), d.Level, d.Category, d.Server, d.UserID, d.Message, d.Repeats); err != nil {
				return err
			}
		}
		return nil
	})
}

// CallStatQuery selects statistics. Empty fields do not filter.
type CallStatQuery struct {
	From, To time.Time // From inclusive, To exclusive; zero means unbounded
	Server   string
	Item     string
	UserID   string
	Client   string
	Method   string
	Outcome  string
	Limit    int  // rows at most; one more is read to tell whether there were more
	Newest   bool // newest first (a listing); otherwise oldest first (aggregation)
}

// MaxStatRows bounds one read for aggregation (ADR 0024 point 4).
const MaxStatRows = 100_000

// CallStats returns the matching statistics and whether more rows matched
// than Limit.
func (s *Store) CallStats(ctx context.Context, org OrgID, q CallStatQuery) ([]CallStat, bool, error) {
	if q.Limit <= 0 || q.Limit > MaxStatRows {
		q.Limit = MaxStatRows
	}
	where, args := statWhere(org, q)
	order := "ts_ms ASC, id ASC"
	if q.Newest {
		order = "ts_ms DESC, id DESC"
	}
	args = append(args, q.Limit+1)
	//nolint:gosec // the WHERE clause is built from fixed column names; values are bound
	rows, err := s.r.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, ts_ms, user_id, token_id, client, server, method, item, outcome, error_class,
			decide_us, wait_us, audit_us, connect_us, upstream_us, total_us, args_bytes, result_bytes
		FROM call_stats WHERE %s ORDER BY %s LIMIT ?`, strings.Join(where, " AND "), order), args...)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	var out []CallStat
	for rows.Next() {
		var r CallStat
		var ms int64
		if err := rows.Scan(&r.ID, &ms, &r.UserID, &r.TokenID, &r.Client, &r.Server, &r.Method, &r.Item, &r.Outcome, &r.ErrorClass,
			&r.DecideUS, &r.WaitUS, &r.AuditUS, &r.ConnectUS, &r.UpstreamUS, &r.TotalUS, &r.ArgsBytes, &r.ResultBytes); err != nil {
			return nil, false, err
		}
		r.Time = time.UnixMilli(ms).UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > q.Limit
	if more {
		out = out[:q.Limit]
	}
	return out, more, nil
}

// DiagnosticQuery selects diagnostic events, newest first.
type DiagnosticQuery struct {
	Since    time.Time // inclusive; zero means unbounded
	Before   time.Time // exclusive; zero means now
	Category string
	Level    string
	Limit    int // default 100, at most 500
}

// Diagnostics returns the matching events, newest first.
func (s *Store) Diagnostics(ctx context.Context, org OrgID, q DiagnosticQuery) ([]Diagnostic, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	where, args := []string{"org_id = ?"}, []any{org}
	if !q.Since.IsZero() {
		where, args = append(where, "ts_ms >= ?"), append(args, q.Since.UnixMilli())
	}
	if !q.Before.IsZero() {
		where, args = append(where, "ts_ms < ?"), append(args, q.Before.UnixMilli())
	}
	if q.Category != "" {
		where, args = append(where, "category = ?"), append(args, q.Category)
	}
	if q.Level != "" {
		where, args = append(where, "level = ?"), append(args, q.Level)
	}
	args = append(args, q.Limit)
	//nolint:gosec // the WHERE clause is built from fixed column names; values are bound
	rows, err := s.r.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, ts_ms, level, category, server, user_id, message, repeats
		FROM diagnostics WHERE %s ORDER BY ts_ms DESC, id DESC LIMIT ?`, strings.Join(where, " AND ")), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Diagnostic
	for rows.Next() {
		var d Diagnostic
		var ms int64
		if err := rows.Scan(&d.ID, &ms, &d.Level, &d.Category, &d.Server, &d.UserID, &d.Message, &d.Repeats); err != nil {
			return nil, err
		}
		d.Time = time.UnixMilli(ms).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// TelemetryInfo says what the two tables hold for an org.
type TelemetryInfo struct {
	Calls       int64
	OldestCall  time.Time // zero when there are none
	Diagnostics int64
	OldestDiag  time.Time
}

// TelemetryStorage reports what the statistics and the log hold.
func (s *Store) TelemetryStorage(ctx context.Context, org OrgID) (TelemetryInfo, error) {
	var info TelemetryInfo
	for _, t := range []struct {
		table  string
		n      *int64
		oldest *time.Time
	}{{"call_stats", &info.Calls, &info.OldestCall}, {"diagnostics", &info.Diagnostics, &info.OldestDiag}} {
		var ms sql.NullInt64
		//nolint:gosec // the table name is one of two constants
		if err := s.r.QueryRowContext(ctx, "SELECT COUNT(*), MIN(ts_ms) FROM "+t.table+" WHERE org_id = ?", org).Scan(t.n, &ms); err != nil {
			return TelemetryInfo{}, err
		}
		if ms.Valid {
			*t.oldest = time.UnixMilli(ms.Int64).UTC()
		}
	}
	return info, nil
}

// pruneBatch bounds one delete so a large clean-up never holds the single
// writer for long.
const pruneBatch = 5000

// PruneTelemetry deletes an org's statistics and diagnostic events older than
// before, in batches. It returns how many rows it deleted.
func (s *Store) PruneTelemetry(ctx context.Context, org OrgID, before time.Time) (int64, error) {
	var total int64
	for _, table := range []string{"call_stats", "diagnostics"} {
		for {
			var n int64
			err := s.tx(ctx, func(tx *sql.Tx) error {
				//nolint:gosec // the table name is one of two constants
				res, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid IN (SELECT rowid FROM "+table+
					" WHERE org_id = ? AND ts_ms < ? LIMIT ?)", org, before.UnixMilli(), pruneBatch)
				if err != nil {
					return err
				}
				n, err = res.RowsAffected()
				return err
			})
			if err != nil {
				return total, err
			}
			total += n
			if n < pruneBatch {
				break
			}
		}
	}
	return total, nil
}

// statWhere is the WHERE clause of a CallStatQuery, from fixed column names.
func statWhere(org OrgID, q CallStatQuery) ([]string, []any) {
	where, args := []string{"org_id = ?"}, []any{org}
	if !q.From.IsZero() {
		where, args = append(where, "ts_ms >= ?"), append(args, q.From.UnixMilli())
	}
	if !q.To.IsZero() {
		where, args = append(where, "ts_ms < ?"), append(args, q.To.UnixMilli())
	}
	for _, f := range []struct{ col, v string }{{"server", q.Server}, {"item", q.Item}, {"user_id", q.UserID},
		{"client", q.Client}, {"method", q.Method}, {"outcome", q.Outcome}} {
		if f.v != "" {
			where, args = append(where, f.col+" = ?"), append(args, f.v)
		}
	}
	return where, args
}

// statKeys are the expressions a count or a sample can be grouped by.
var statKeys = map[string]string{
	"":       "''",
	"server": "server",
	"tool":   "server || '/' || item",
	"member": "user_id",
	"client": "client",
	"method": "method",
}

// StatCount counts the calls of one group, or of one bucket.
type StatCount struct {
	Key        string // the group's key; for buckets, the bucket's index
	Calls      int
	OK         int
	ToolErrors int
	Errors     int
	Refused    int
}

// Buckets of a count: Bucket set groups by time instead of by a key.
type Buckets struct {
	From time.Time
	Size time.Duration
}

// CountCallStats counts every matching call by group (by is a key of
// statKeys) or, with b set, by time bucket. Counting in the database reads no
// rows into memory, so no call is left out however many there are (review of
// 2026-11-15, T-02).
func (s *Store) CountCallStats(ctx context.Context, org OrgID, q CallStatQuery, by string, b *Buckets) ([]StatCount, error) {
	key, ok := statKeys[by]
	if !ok {
		return nil, fmt.Errorf("store: no statistic key %q", by)
	}
	where, args := statWhere(org, q)
	if b != nil {
		if b.Size < time.Millisecond {
			return nil, errors.New("store: a bucket of at least a millisecond")
		}
		key = "CAST((ts_ms - ?) / ? AS TEXT)"
		args = append([]any{b.From.UnixMilli(), b.Size.Milliseconds()}, args...)
	}
	//nolint:gosec // the key and the WHERE clause come from fixed expressions; values are bound
	rows, err := s.r.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s AS k, COUNT(*),
			SUM(outcome = 'ok'), SUM(outcome = 'tool_error'), SUM(outcome = 'refused'),
			SUM(outcome NOT IN ('ok', 'tool_error', 'refused'))
		FROM call_stats WHERE %s GROUP BY k`, key, strings.Join(where, " AND ")), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []StatCount
	for rows.Next() {
		var c StatCount
		if err := rows.Scan(&c.Key, &c.Calls, &c.OK, &c.ToolErrors, &c.Refused, &c.Errors); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// StatSample is what the time figures of a call that ran need, and nothing
// more: a sample of many is held in memory.
type StatSample struct {
	Key         string
	Time        time.Time
	LatencyUS   int64 // TotalUS without the approval wait
	DecideUS    int64
	AuditUS     int64
	ConnectUS   int64
	UpstreamUS  int64
	ArgsBytes   int64
	ResultBytes int64
}

// SampleCallStats returns the most recent calls that ran (refusals are quick
// and would hide slow servers), at most limit of them, newest first, and
// whether there were more (review of 2026-11-15, T-02 and T-03).
func (s *Store) SampleCallStats(ctx context.Context, org OrgID, q CallStatQuery, by string, limit int) ([]StatSample, bool, error) {
	key, ok := statKeys[by]
	if !ok {
		return nil, false, fmt.Errorf("store: no statistic key %q", by)
	}
	if limit <= 0 || limit > MaxStatRows {
		limit = MaxStatRows
	}
	where, args := statWhere(org, q)
	where = append(where, "outcome IN ('ok', 'tool_error', 'error')")
	args = append(args, limit+1)
	//nolint:gosec // the key and the WHERE clause come from fixed expressions; values are bound
	rows, err := s.r.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s, ts_ms, max(total_us - wait_us, 0), decide_us, audit_us, connect_us, upstream_us, args_bytes, result_bytes
		FROM call_stats WHERE %s ORDER BY ts_ms DESC, id DESC LIMIT ?`, key, strings.Join(where, " AND ")), args...)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]StatSample, 0, 1024)
	for rows.Next() {
		var x StatSample
		var ms int64
		if err := rows.Scan(&x.Key, &ms, &x.LatencyUS, &x.DecideUS, &x.AuditUS, &x.ConnectUS, &x.UpstreamUS, &x.ArgsBytes, &x.ResultBytes); err != nil {
			return nil, false, err
		}
		x.Time = time.UnixMilli(ms).UTC()
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}
