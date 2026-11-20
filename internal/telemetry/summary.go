package telemetry

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
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
	// Times cover calls that ran: refusals are quick and would hide slow
	// servers (ADR 0024 point 4). Total is the latency: the time from arrival
	// to answer without any wait for a person's approval (review of
	// 2026-11-15, T-10). When a window holds more calls than the sample, the
	// times come from the most recent ones; the counts always cover all.
	Total    Stats `json:"total_ms"`
	Decide   Stats `json:"decide_ms"`
	Audit    Stats `json:"audit_ms"`
	Connect  Stats `json:"connect_ms"`
	Upstream Stats `json:"upstream_ms"`
	Args     Stats `json:"args_bytes"`
	Result   Stats `json:"result_bytes"`
}

// GroupBy lists the keys a summary can be broken down by.
var GroupBy = []string{"server", "tool", "member", "client", "method"}

// ValidBy reports whether by is one of GroupBy.
func ValidBy(by string) bool { return slices.Contains(GroupBy, by) }

// Summarize combines the counts of every call with the times of a sample,
// as a whole and by group, largest group first.
func Summarize(counts []store.StatCount, sample []store.StatSample) (Group, []Group) {
	parts := map[string][]store.StatSample{}
	for _, x := range sample {
		parts[x.Key] = append(parts[x.Key], x)
	}
	total := Group{}
	groups := make([]Group, 0, len(counts))
	for _, c := range counts {
		g := group(c, parts[c.Key])
		groups = append(groups, g)
		total.Calls += c.Calls
		total.OK += c.OK
		total.ToolErrors += c.ToolErrors
		total.Errors += c.Errors
		total.Refused += c.Refused
	}
	total = group(store.StatCount{Calls: total.Calls, OK: total.OK, ToolErrors: total.ToolErrors, Errors: total.Errors, Refused: total.Refused}, sample)
	slices.SortFunc(groups, func(a, b Group) int {
		if a.Calls != b.Calls {
			return b.Calls - a.Calls
		}
		return strings.Compare(a.Key, b.Key)
	})
	return total, groups
}

func group(c store.StatCount, xs []store.StatSample) Group {
	g := Group{Key: c.Key, Calls: c.Calls, OK: c.OK, ToolErrors: c.ToolErrors, Errors: c.Errors, Refused: c.Refused}
	if ran := g.OK + g.ToolErrors + g.Errors; ran > 0 {
		g.ErrorRate = float64(g.Errors) / float64(ran)
	}
	n := len(xs)
	total, decide, audit, connect, up, args, result := make([]int64, 0, n), make([]int64, 0, n), make([]int64, 0, n), make([]int64, 0, n),
		make([]int64, 0, n), make([]int64, 0, n), make([]int64, 0, n)
	for _, x := range xs {
		total = append(total, x.LatencyUS)
		decide = append(decide, x.DecideUS)
		audit = append(audit, x.AuditUS)
		connect = append(connect, x.ConnectUS)
		up = append(up, x.UpstreamUS)
		args = append(args, x.ArgsBytes)
		result = append(result, x.ResultBytes)
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
	P50        float64   `json:"p50_ms"` // 0 when the sample holds no call of the bucket
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

// BucketCount is how many buckets of size bucket cover [from, to).
func BucketCount(from, to time.Time, bucket time.Duration) int {
	return int((to.Sub(from) + bucket - 1) / bucket)
}

// CheckSeries tells whether [from, to) in buckets of size bucket is allowed.
func CheckSeries(from, to time.Time, bucket time.Duration) error {
	switch {
	case !from.Before(to):
		return fmt.Errorf("%w: the start must be before the end", ErrSeries)
	case to.Sub(from) > MaxWindow+bucket: // the last bucket may reach past the window
		return fmt.Errorf("%w: the window is at most 31 days", ErrSeries)
	case bucket < MinBucket:
		return fmt.Errorf("%w: a bucket is at least one minute", ErrSeries)
	case BucketCount(from, to, bucket) > MaxBuckets:
		return fmt.Errorf("%w: at most %d buckets", ErrSeries, MaxBuckets)
	}
	return nil
}

// Series lays counts by bucket index (store.CountCallStats with Buckets from
// from) and a time sample into buckets of size bucket from from (inclusive)
// to to (exclusive). Every bucket is present, empty ones too, so a chart has
// no gaps. Call CheckSeries first.
func Series(counts []store.StatCount, sample []store.StatSample, from, to time.Time, bucket time.Duration) []Bucket {
	n := BucketCount(from, to, bucket)
	out := make([]Bucket, n)
	lat := make([][]int64, n)
	for i := range out {
		out[i].Start = from.Add(time.Duration(i) * bucket).UTC()
	}
	for _, c := range counts {
		i, err := strconv.Atoi(c.Key)
		if err != nil || i < 0 || i >= n {
			continue
		}
		out[i].Calls, out[i].Errors, out[i].ToolErrors, out[i].Refused = c.Calls, c.Errors, c.ToolErrors, c.Refused
	}
	for _, x := range sample {
		if x.Time.Before(from) || !x.Time.Before(to) {
			continue
		}
		i := int(x.Time.Sub(from) / bucket)
		lat[i] = append(lat[i], x.LatencyUS)
	}
	for i := range out {
		st := stats(lat[i], 1000)
		out[i].P50, out[i].P95 = st.P50, st.P95
	}
	return out
}
