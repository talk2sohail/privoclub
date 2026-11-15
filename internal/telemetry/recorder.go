// Package telemetry records how calls went and what happened to connections
// (F28, ADR 0024): call statistics, a diagnostic log and Prometheus metrics.
//
// Unlike the audit, everything here is best effort. A full queue drops a row
// and counts it; nothing a caller does waits for, or fails because of, a
// statistic. Nothing leaves the server unless an operator scrapes /metrics.
package telemetry

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
)

// Store is the store subset the recorder uses.
type Store interface {
	InsertCallStats(ctx context.Context, org store.OrgID, rows ...store.CallStat) error
	InsertDiagnostics(ctx context.Context, org store.OrgID, rows ...store.Diagnostic) error
}

// Options configure a Recorder. Zero values take the defaults.
type Options struct {
	QueueSize     int           // default 10,000
	FlushInterval time.Duration // default 1 s
	BatchSize     int           // default 500
	RepeatWindow  time.Duration // the same diagnostic within it is one row: default 1 min
	MaxRepeatKeys int           // distinct diagnostics remembered for that: default 10,000
	Metrics       *Metrics      // optional: every call is also counted here
	Logger        *slog.Logger
	Now           func() time.Time
}

type item struct {
	org  store.OrgID
	call *store.CallStat
	diag *store.Diagnostic
}

// Recorder queues statistics and diagnostics and writes them in batches. A nil
// *Recorder records nothing, so callers need no checks when telemetry is off.
type Recorder struct {
	store   Store
	o       Options
	queue   chan item
	mu      sync.RWMutex // guards closed and sends on queue
	closed  bool
	done    chan struct{}
	once    sync.Once
	dropped atomic.Uint64
}

// New starts a recorder. Call Close to flush and stop it. With s nil nothing
// is stored and calls are only counted in o.Metrics (retention 0).
func New(s Store, o Options) *Recorder {
	if o.QueueSize <= 0 {
		o.QueueSize = 10_000
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = time.Second
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 500
	}
	if o.RepeatWindow <= 0 {
		o.RepeatWindow = time.Minute
	}
	if o.MaxRepeatKeys <= 0 {
		o.MaxRepeatKeys = 10_000
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	r := &Recorder{store: s, o: o, queue: make(chan item, o.QueueSize), done: make(chan struct{})}
	go r.run()
	return r
}

// Call records one call's statistic. It never blocks.
func (r *Recorder) Call(org store.OrgID, c store.CallStat) {
	if r == nil {
		return
	}
	r.o.Metrics.ObserveCall(c)
	if r.store == nil {
		return // recording off: metrics only
	}
	r.send(item{org: org, call: &c})
}

// Diagnose records an operational event. It never blocks. Callers pass fixed
// texts, or a fixed text and an upstream's answer with its URLs redacted
// (upstream.RedactText); every message is cut at MaxMessage bytes here.
func (r *Recorder) Diagnose(org store.OrgID, d store.Diagnostic) {
	if r == nil || r.store == nil {
		return
	}
	if d.Time.IsZero() {
		d.Time = r.o.Now()
	}
	// Messages can hold text an upstream server wrote. Every source is cut
	// here, so a server cannot fill the disk or a page through the log
	// (review of 2026-11-15, T-01).
	d.Message, d.Server, d.UserID = Clip(d.Message, MaxMessage), Clip(d.Server, 64), Clip(d.UserID, 64)
	r.send(item{org: org, diag: &d})
}

func (r *Recorder) send(it item) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		r.drop()
		return
	}
	select {
	case r.queue <- it:
	default:
		r.drop()
	}
}

func (r *Recorder) drop() {
	r.dropped.Add(1)
	r.o.Metrics.AddDropped()
}

// MaxMessage bounds a diagnostic message in bytes.
const MaxMessage = 512

// Clip cuts s to at most n bytes on a rune boundary, ending in "…" when it
// was cut.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const more = "…"
	cut := n - len(more)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + more
}

// Dropped is how many rows were not recorded since the start.
func (r *Recorder) Dropped() uint64 {
	if r == nil {
		return 0
	}
	return r.dropped.Load()
}

// Close writes what is queued and stops the recorder.
func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		close(r.queue)
		r.mu.Unlock()
		<-r.done
	})
}

// repeat is what the recorder remembers of one diagnostic it wrote.
type repeat struct {
	org     store.OrgID
	first   store.Diagnostic // the event as written
	written time.Time
	more    int // occurrences since, not yet written
}

func diagKey(org store.OrgID, d *store.Diagnostic) string {
	return string(org) + "\x00" + d.Level + "\x00" + d.Category + "\x00" + d.Server + "\x00" + d.UserID + "\x00" + d.Message
}

func (r *Recorder) run() {
	defer close(r.done)
	t := time.NewTicker(r.o.FlushInterval)
	defer t.Stop()
	calls := map[store.OrgID][]store.CallStat{}
	diags := map[store.OrgID][]store.Diagnostic{}
	repeats := map[string]*repeat{}
	pending := 0

	// settle writes the count of repeats whose window has passed, and forgets
	// the ones with nothing left to say.
	settle := func(now time.Time, all bool) {
		for k, rp := range repeats {
			if !all && now.Sub(rp.written) < r.o.RepeatWindow {
				continue
			}
			if rp.more > 0 {
				d := rp.first
				d.ID, d.Time, d.Repeats = ids.New(), now, rp.more
				diags[rp.org] = append(diags[rp.org], d)
				pending++
			}
			delete(repeats, k)
		}
	}
	flush := func() {
		for org, rows := range calls {
			r.write(func(ctx context.Context) error { return r.store.InsertCallStats(ctx, org, rows...) }, len(rows))
		}
		for org, rows := range diags {
			r.write(func(ctx context.Context) error { return r.store.InsertDiagnostics(ctx, org, rows...) }, len(rows))
		}
		clear(calls)
		clear(diags)
		pending = 0
	}
	for {
		select {
		case it, ok := <-r.queue:
			if !ok {
				settle(r.o.Now(), true)
				flush()
				return
			}
			switch {
			case it.call != nil:
				if it.call.ID == "" {
					it.call.ID = ids.New()
				}
				calls[it.org] = append(calls[it.org], *it.call)
				pending++
			case it.diag != nil:
				d := *it.diag
				k := diagKey(it.org, &d)
				if rp, ok := repeats[k]; ok && d.Time.Sub(rp.written) < r.o.RepeatWindow {
					rp.more++
					continue
				}
				if rp, ok := repeats[k]; ok && rp.more > 0 {
					// The window passed: write what piled up, then this one.
					old := rp.first
					old.ID, old.Time, old.Repeats = ids.New(), d.Time, rp.more
					diags[it.org] = append(diags[it.org], old)
					pending++
				}
				if d.ID == "" {
					d.ID = ids.New()
				}
				if d.Repeats < 1 {
					d.Repeats = 1
				}
				diags[it.org] = append(diags[it.org], d)
				pending++
				if _, known := repeats[k]; known || len(repeats) < r.o.MaxRepeatKeys {
					repeats[k] = &repeat{org: it.org, first: d, written: d.Time}
				}
			}
			if pending >= r.o.BatchSize {
				flush()
			}
		case <-t.C:
			settle(r.o.Now(), false)
			flush()
		}
	}
}

// write runs one insert with a few retries; rows it cannot write are counted
// as dropped and logged once.
func (r *Recorder) write(insert func(context.Context) error, n int) {
	var err error
	for attempt := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = insert(ctx)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond << attempt)
	}
	r.dropped.Add(uint64(n)) //nolint:gosec // n is a batch length
	r.o.Metrics.AddDroppedN(n)
	r.o.Logger.Warn("telemetry rows not written", "rows", n, "err", err)
}
