package control

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
)

// The telemetry endpoints (F28, ADR 0024): how calls went and what happened to
// connections. They show who called what, like the audit, so they need the
// same permission; they read only the caller's org.

func (a *API) telemetryRoutes(mux *http.ServeMux, wrap func(auth.Permission, handler) http.Handler) {
	mux.Handle("GET /api/v1/telemetry/summary", wrap(auth.PermReadAudit, a.telemetrySummary))
	mux.Handle("GET /api/v1/telemetry/timeseries", wrap(auth.PermReadAudit, a.telemetrySeries))
	mux.Handle("GET /api/v1/telemetry/calls", wrap(auth.PermReadAudit, a.telemetryCalls))
	mux.Handle("GET /api/v1/telemetry/logs", wrap(auth.PermReadAudit, a.telemetryLogs))
	mux.Handle("GET /api/v1/telemetry/storage", wrap(auth.PermReadAudit, a.telemetryStorage))
}

// parseWindow reads a window such as 15m, 24h or 7d; def when empty.
func parseWindow(v string, def time.Duration) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	var d time.Duration
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, errorf(http.StatusBadRequest, "window %q: use minutes, hours or days, such as 15m, 24h or 7d", v)
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(v); err != nil {
			return 0, errorf(http.StatusBadRequest, "window %q: use minutes, hours or days, such as 15m, 24h or 7d", v)
		}
	}
	if d < time.Minute || d > telemetry.MaxWindow {
		return 0, errorf(http.StatusBadRequest, "window %q: from 1 minute to 31 days", v)
	}
	return d, nil
}

// autoBucket picks a bucket that gives a readable chart for a window.
func autoBucket(window time.Duration) time.Duration {
	switch {
	case window <= time.Hour:
		return time.Minute
	case window <= 6*time.Hour:
		return 5 * time.Minute
	case window <= 24*time.Hour:
		return 30 * time.Minute
	case window <= 7*24*time.Hour:
		return 6 * time.Hour
	}
	return 24 * time.Hour
}

// statFilter reads the filters the summary, the series and the listing share.
func statFilter(r *http.Request) (store.CallStatQuery, error) {
	q := r.URL.Query()
	f := store.CallStatQuery{Server: q.Get("server"), Item: q.Get("tool"), UserID: q.Get("member"), Client: q.Get("client"),
		Method: q.Get("method"), Outcome: q.Get("outcome")}
	switch f.Outcome {
	case "", store.OutcomeOK, store.OutcomeToolError, store.OutcomeError, store.OutcomeRefused:
	default:
		return f, errorf(http.StatusBadRequest, "outcome must be ok, tool_error, error or refused")
	}
	return f, nil
}

// memberNames maps the user ids in rows to their person names.
func (a *API) memberNames(ctx context.Context, org store.OrgID, ids []string) (map[string]string, error) {
	names := map[string]string{}
	if !slices.ContainsFunc(ids, func(id string) bool { return id != "" }) {
		return names, nil
	}
	users, err := a.Store.ListUsers(ctx, org)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		names[u.ID] = u.Person
	}
	return names, nil
}

func (a *API) telemetrySummary(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	window, err := parseWindow(r.URL.Query().Get("window"), 24*time.Hour)
	if err != nil {
		return err
	}
	by := r.URL.Query().Get("by")
	if by == "" {
		by = "server"
	}
	if _, ok := telemetry.GroupBy[by]; !ok {
		return errorf(http.StatusBadRequest, "by must be server, tool, member, client or method")
	}
	f, err := statFilter(r)
	if err != nil {
		return err
	}
	now := time.Now()
	f.From, f.To = now.Add(-window), now.Add(time.Millisecond)
	rows, truncated, err := a.Store.CallStats(r.Context(), p.OrgID, f)
	if err != nil {
		return err
	}
	total, groups, err := telemetry.Summarize(rows, by)
	if err != nil {
		return errorf(http.StatusBadRequest, "%v", err)
	}
	out := map[string]any{"window": window.String(), "from": f.From.UTC(), "to": now.UTC(), "by": by, "total": total,
		"groups": groups, "truncated": truncated, "recording": a.TelemetryRetentionDays > 0}
	if by == "member" {
		ids := make([]string, 0, len(groups))
		for _, g := range groups {
			ids = append(ids, g.Key)
		}
		names, err := a.memberNames(r.Context(), p.OrgID, ids)
		if err != nil {
			return err
		}
		out["labels"] = names
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *API) telemetrySeries(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	q := r.URL.Query()
	window, err := parseWindow(q.Get("window"), 24*time.Hour)
	if err != nil {
		return err
	}
	bucket := autoBucket(window)
	if v := q.Get("bucket"); v != "" {
		if bucket, err = time.ParseDuration(v); err != nil {
			return errorf(http.StatusBadRequest, "bucket %q: a duration such as 1m or 1h", v)
		}
	}
	// Buckets end at the next boundary after now, so the last one holds now.
	to := time.Now().Truncate(bucket).Add(bucket)
	from := to.Add(-window).Truncate(bucket)
	if err := telemetry.CheckSeries(from, to, bucket); err != nil {
		return errorf(http.StatusBadRequest, "%s", strings.TrimPrefix(err.Error(), telemetry.ErrSeries.Error()+": "))
	}
	f, err := statFilter(r)
	if err != nil {
		return err
	}
	f.From, f.To = from, to
	rows, truncated, err := a.Store.CallStats(r.Context(), p.OrgID, f)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from.UTC(), "to": to.UTC(), "bucket": bucket.String(),
		"buckets": telemetry.Series(rows, from, to, bucket), "truncated": truncated, "recording": a.TelemetryRetentionDays > 0})
	return nil
}

func ms(us int64) float64 { return float64(us) / 1000 }

func (a *API) telemetryCalls(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	f, err := statFilter(r)
	if err != nil {
		return err
	}
	f.Newest = true
	f.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return errorf(http.StatusBadRequest, "before must be a time such as 2026-11-14T10:00:00Z")
		}
		f.To = t
	}
	rows, more, err := a.Store.CallStats(r.Context(), p.OrgID, f)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(rows))
	for _, c := range rows {
		ids = append(ids, c.UserID)
	}
	names, err := a.memberNames(r.Context(), p.OrgID, ids)
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		e := map[string]any{"id": c.ID, "time": c.Time, "server": c.Server, "method": c.Method, "item": c.Item,
			"outcome": c.Outcome, "client": c.Client, "token_id": c.TokenID,
			"decide_ms": ms(c.DecideUS), "audit_ms": ms(c.AuditUS), "connect_ms": ms(c.ConnectUS), "upstream_ms": ms(c.UpstreamUS),
			"total_ms": ms(c.TotalUS), "args_bytes": c.ArgsBytes, "result_bytes": c.ResultBytes}
		if c.ErrorClass != "" {
			e["error_class"] = c.ErrorClass
		}
		if c.UserID != "" {
			e["user_id"], e["member"] = c.UserID, names[c.UserID]
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"calls": out, "more": more})
	return nil
}

var (
	diagLevels     = []string{store.LevelInfo, store.LevelWarn, store.LevelError}
	diagCategories = []string{store.CategoryUpstream, store.CategoryGrant, store.CategoryGateway, store.CategoryAudit, store.CategoryLifecycle}
)

func (a *API) telemetryLogs(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	q := r.URL.Query()
	dq := store.DiagnosticQuery{Category: q.Get("category"), Level: q.Get("level")}
	if dq.Category != "" && !slices.Contains(diagCategories, dq.Category) {
		return errorf(http.StatusBadRequest, "category must be one of %s", strings.Join(diagCategories, ", "))
	}
	if dq.Level != "" && !slices.Contains(diagLevels, dq.Level) {
		return errorf(http.StatusBadRequest, "level must be one of %s", strings.Join(diagLevels, ", "))
	}
	dq.Limit, _ = strconv.Atoi(q.Get("limit"))
	for name, dst := range map[string]*time.Time{"since": &dq.Since, "before": &dq.Before} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return errorf(http.StatusBadRequest, "%s must be a time such as 2026-11-14T10:00:00Z", name)
			}
			*dst = t
		}
	}
	ds, err := a.Store.Diagnostics(r.Context(), p.OrgID, dq)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(ds))
	for _, d := range ds {
		ids = append(ids, d.UserID)
	}
	names, err := a.memberNames(r.Context(), p.OrgID, ids)
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(ds))
	for _, d := range ds {
		e := map[string]any{"id": d.ID, "time": d.Time, "level": d.Level, "category": d.Category, "message": d.Message, "repeats": d.Repeats}
		if d.Server != "" {
			e["server"] = d.Server
		}
		if d.UserID != "" {
			e["user_id"], e["member"] = d.UserID, names[d.UserID]
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": out})
	return nil
}

func (a *API) telemetryStorage(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	info, err := a.Store.TelemetryStorage(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	when := func(t time.Time) any {
		if t.IsZero() {
			return nil
		}
		return t
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recording": a.TelemetryRetentionDays > 0, "retention_days": a.TelemetryRetentionDays,
		"calls": info.Calls, "oldest_call": when(info.OldestCall), "logs": info.Diagnostics, "oldest_log": when(info.OldestDiag),
		"dropped": a.Telemetry.Dropped(),
	})
	return nil
}
