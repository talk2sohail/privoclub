// Package audit records what happened (ADR 0006).
//
// Intent records are written synchronously before an upstream call: if that
// write fails, the call does not run. Completion records go through a bounded
// queue and are written in batches; when the queue is full a request waits a
// short time and then is refused rather than the event being dropped.
package audit

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"privo.club/privo/internal/store"
)

// Store is the store subset the writer uses.
type Store interface {
	InsertAudit(ctx context.Context, org store.OrgID, events ...store.AuditEvent) error
}

// ErrBusy is returned when the completion queue stays full.
var ErrBusy = errors.New("audit: queue full")

type queued struct {
	org store.OrgID
	ev  store.AuditEvent
}

// Writer writes audit events.
type Writer struct {
	mu       sync.RWMutex // guards closed and sends on queue
	closed   bool
	store    Store
	log      *slog.Logger
	queue    chan queued
	wait     time.Duration
	interval time.Duration
	batch    int
	done     chan struct{}
	once     sync.Once
}

// Options configure a Writer.
type Options struct {
	QueueSize     int           // default 10000
	EnqueueWait   time.Duration // default 250ms
	FlushInterval time.Duration // default 50ms
	BatchSize     int           // default 500
	Logger        *slog.Logger
}

// NewWriter starts a writer. Call Close to flush and stop it.
func NewWriter(s Store, o Options) *Writer {
	if o.QueueSize <= 0 {
		o.QueueSize = 10000
	}
	if o.EnqueueWait <= 0 {
		o.EnqueueWait = 250 * time.Millisecond
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = 50 * time.Millisecond
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 500
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	w := &Writer{store: s, log: o.Logger, queue: make(chan queued, o.QueueSize), wait: o.EnqueueWait,
		interval: o.FlushInterval, batch: o.BatchSize, done: make(chan struct{})}
	go w.run()
	return w
}

// Intent writes an intent record synchronously.
func (w *Writer) Intent(ctx context.Context, org store.OrgID, ev store.AuditEvent) error {
	ev.Phase = "intent"
	return w.store.InsertAudit(ctx, org, ev)
}

// Admin writes an admin-action record synchronously.
func (w *Writer) Admin(ctx context.Context, org store.OrgID, ev store.AuditEvent) error {
	ev.Phase = "admin"
	return w.store.InsertAudit(ctx, org, ev)
}

// ErrClosed is returned after Close.
var ErrClosed = errors.New("audit: writer closed")

// Completion queues a completion record. A caller that disconnects does not
// cancel the record: the call already happened.
func (w *Writer) Completion(ctx context.Context, org store.OrgID, ev store.AuditEvent) error {
	ctx = context.WithoutCancel(ctx)
	ev.Phase = "completion"
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return ErrClosed
	}
	select {
	case w.queue <- queued{org, ev}:
		return nil
	default:
	}
	t := time.NewTimer(w.wait)
	defer t.Stop()
	select {
	case w.queue <- queued{org, ev}:
		return nil
	case <-t.C:
		return ErrBusy
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close flushes queued events and stops the writer. Sends racing with Close
// get ErrClosed instead of a panic.
func (w *Writer) Close() {
	w.once.Do(func() {
		w.mu.Lock()
		w.closed = true
		close(w.queue)
		w.mu.Unlock()
		<-w.done
	})
}

// flushAttempts and flushBackoff bound retries of a failed batch.
const (
	flushAttempts = 8
	flushBackoff  = 50 * time.Millisecond
)

func (w *Writer) run() {
	defer close(w.done)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	var pending []queued
	flush := func() {
		if len(pending) == 0 {
			return
		}
		byOrg := map[store.OrgID][]store.AuditEvent{}
		for _, q := range pending {
			byOrg[q.org] = append(byOrg[q.org], q.ev)
		}
		for org, evs := range byOrg {
			var err error
			for attempt := 0; attempt < flushAttempts; attempt++ {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err = w.store.InsertAudit(ctx, org, evs...)
				cancel()
				if err == nil {
					break
				}
				time.Sleep(flushBackoff << attempt)
			}
			if err != nil {
				// Loud and counted: an operator must see this. Metrics arrive with F16.
				w.log.Error("AUDIT EVENTS LOST after retries", "org", org, "events", len(evs), "err", err)
			}
		}
		pending = pending[:0]
	}
	for {
		select {
		case q, ok := <-w.queue:
			if !ok {
				flush()
				return
			}
			pending = append(pending, q)
			if len(pending) >= w.batch {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}
