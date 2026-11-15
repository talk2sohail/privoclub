package telemetry

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"privo.club/privo/internal/store"
)

// Stats describes a set of durations in milliseconds (or sizes in bytes).
type Stats struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Min float64 `json:"min"`
	Max float64 `json:"max"`
	Avg float64 `json:"avg"`
}

// Group is the summary of the calls that share one key.
type Group struct {
	Key        string  `json:"key"`
	Calls      int     `json:"calls"`
	OK         int     `json:"ok"`
	ToolErrors int     `json:"tool_errors"`
	Errors     int     `json:"errors"`
	Refused    int     `json:"refused"`
	ErrorRate  float64 `json:"error_rate"` // errors over calls that ran
	// Durations cover calls that ran: refusals are quick and would hide slow
	// servers (ADR 0024 point 4).
	Total    Stats `json:"total_ms"`
	Decide   Stats `json:"decide_ms"`
	Audit    Stats `json:"audit_ms"`
	Connect  Stats `json:"connect_ms"`
	Upstream Stats `json:"upstream_ms"`
	Args     Stats `json:"args_bytes"`
	Result   Stats `json:"result_bytes"`
}

// GroupBy holds the keys a summary can be broken down by.
var GroupBy = map[string]func(store.CallStat) string{
	"server": func(c store.CallStat) string { return c.Server },
	"tool":   func(c store.CallStat) string { return c.Server + "/" + c.Item },
	"member": func(c store.CallStat) string { return c.UserID },
	"client": func(c store.CallStat) string { return c.Client },
	"method": func(c store.CallStat) string { return c.Method },
}

// Summarize summarises rows as a whole and grouped by the key by names (one
// of GroupBy), largest group first.
func Summarize(rows []store.CallStat, by string) (Group, []Group, error) {
	key, ok := GroupBy[by]
	if !ok && by != "" {
		return Group{}, nil, fmt.Errorf("by must be one of server, tool, member, client, method")
	}
	total := summarize("", rows)
	if !ok {
		return total, nil, nil
	}
	parts := map[string][]store.CallStat{}
	for _, r := range rows {
		k := key(r)
		parts[k] = append(parts[k], r)
	}
	groups := make([]Group, 0, len(parts))
	for k, rs := range parts {
		groups = append(groups, summarize(k, rs))
	}
	slices.SortFunc(groups, func(a, b Group) int {
		if a.Calls != b.Calls {
			return b.Calls - a.Calls
		}
		if a.Key < b.Key {
			return -1
		}
		if a.Key > b.Key {
			return 1
		}
		return 0
	})
	return total, groups, nil
}

func summarize(k string, rows []store.CallStat) Group {
	g := Group{Key: k, Calls: len(rows)}
	var total, decide, audit, connect, up, args, result []int64
	for _, r := range rows {
		switch r.Outcome {
		case store.OutcomeOK:
			g.OK++
		case store.OutcomeToolError:
			g.ToolErrors++
		case store.OutcomeRefused:
			g.Refused++
			continue
		default:
			g.Errors++
		}
		total = append(total, r.TotalUS)
		decide = append(decide, r.DecideUS)
		audit = append(audit, r.AuditUS)
		connect = append(connect, r.ConnectUS)
		up = append(up, r.UpstreamUS)
		args = append(args, r.ArgsBytes)
		result = append(result, r.ResultBytes)
	}
	if ran := g.OK + g.ToolErrors + g.Errors; ran > 0 {
		g.ErrorRate = float64(g.Errors) / float64(ran)
	}
	const ms = 1000.0
	g.Total, g.Decide, g.Audit, g.Connect, g.Upstream = stats(total, ms), stats(decide, ms), stats(audit, ms), stats(connect, ms), stats(up, ms)
	g.Args, g.Result = stats(args, 1), stats(result, 1)
	return g
}

// stats sorts vs in place and describes them divided by unit. Percentiles are
// nearest-rank: the smallest value with at least p of the values at or below.
func stats(vs []int64, unit float64) Stats {
	if len(vs) == 0 {
		return Stats{}
	}
	slices.Sort(vs)
	var sum float64
	for _, v := range vs {
		sum += float64(v)
	}
	return Stats{P50: float64(rank(vs, 50)) / unit, P95: float64(rank(vs, 95)) / unit, P99: float64(rank(vs, 99)) / unit,
		Min: float64(vs[0]) / unit, Max: float64(vs[len(vs)-1]) / unit, Avg: sum / float64(len(vs)) / unit}
}

// rank is the nearest-rank percentile of sorted vs.
func rank(sorted []int64, p int) int64 {
	i := (p*len(sorted) + 99) / 100 // ceil(p/100 * n)
	if i < 1 {
		i = 1
	}
	return sorted[i-1]
}

// Bucket is one interval of a time series.
type Bucket struct {
	Start      time.Time `json:"start"`
	Calls      int       `json:"calls"`
	Errors     int       `json:"errors"`
	ToolErrors int       `json:"tool_errors"`
	Refused    int       `json:"refused"`
	P50        float64   `json:"p50_ms"`
	P95        float64   `json:"p95_ms"`
}

// Limits of a time series (ADR 0024 point 4).
const (
	MinBucket  = time.Minute
	MaxBuckets = 1000
	MaxWindow  = 31 * 24 * time.Hour
)

// ErrSeries is wrapped by every reason a series cannot be made.
var ErrSeries = errors.New("telemetry: series")

// CheckSeries tells whether [from, to) in buckets of size bucket is allowed.
func CheckSeries(from, to time.Time, bucket time.Duration) error {
	switch {
	case !from.Before(to):
		return fmt.Errorf("%w: the start must be before the end", ErrSeries)
	case to.Sub(from) > MaxWindow:
		return fmt.Errorf("%w: the window is at most 31 days", ErrSeries)
	case bucket < MinBucket:
		return fmt.Errorf("%w: a bucket is at least one minute", ErrSeries)
	case int64(to.Sub(from)/bucket) >= MaxBuckets:
		return fmt.Errorf("%w: at most %d buckets", ErrSeries, MaxBuckets)
	}
	return nil
}

// Series counts rows into buckets of size bucket from from (inclusive) to to
// (exclusive). Every bucket is present, empty ones too, so a chart has no
// gaps. Call CheckSeries first.
func Series(rows []store.CallStat, from, to time.Time, bucket time.Duration) []Bucket {
	n := int((to.Sub(from) + bucket - 1) / bucket)
	out := make([]Bucket, n)
	lat := make([][]int64, n)
	for i := range out {
		out[i].Start = from.Add(time.Duration(i) * bucket).UTC()
	}
	for _, r := range rows {
		if r.Time.Before(from) || !r.Time.Before(to) {
			continue
		}
		i := int(r.Time.Sub(from) / bucket)
		b := &out[i]
		b.Calls++
		switch r.Outcome {
		case store.OutcomeRefused:
			b.Refused++
			continue
		case store.OutcomeToolError:
			b.ToolErrors++
		case store.OutcomeError:
			b.Errors++
		}
		lat[i] = append(lat[i], r.TotalUS)
	}
	for i := range out {
		s := stats(lat[i], 1000)
		out[i].P50, out[i].P95 = s.P50, s.P95
	}
	return out
}
