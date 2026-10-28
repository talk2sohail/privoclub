package audit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"privo.club/privo/internal/store"
)

type memStore struct {
	mu     sync.Mutex
	events []store.AuditEvent
	block  chan struct{}
}

func (m *memStore) InsertAudit(_ context.Context, _ store.OrgID, evs ...store.AuditEvent) error {
	if m.block != nil {
		<-m.block
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, evs...)
	return nil
}

func (m *memStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

func TestIntentIsSynchronousAndCompletionIsFlushed(t *testing.T) {
	s := &memStore{}
	w := NewWriter(s, Options{FlushInterval: time.Hour})
	ctx := context.Background()
	if err := w.Intent(ctx, "o", store.AuditEvent{ID: "1"}); err != nil {
		t.Fatal(err)
	}
	if s.count() != 1 || s.events[0].Phase != "intent" {
		t.Fatal("intent not written synchronously")
	}
	for i := 0; i < 3; i++ {
		if err := w.Completion(ctx, "o", store.AuditEvent{ID: "c"}); err != nil {
			t.Fatal(err)
		}
	}
	w.Close() // must flush despite the long interval
	if s.count() != 4 {
		t.Fatalf("events after Close: %d, want 4", s.count())
	}
}

func TestFullQueueRefusesInsteadOfDropping(t *testing.T) {
	s := &memStore{block: make(chan struct{})}
	w := NewWriter(s, Options{QueueSize: 1, BatchSize: 1, EnqueueWait: 20 * time.Millisecond, FlushInterval: time.Millisecond})
	ctx := context.Background()
	// First event is taken by the flusher, which then blocks in the store.
	_ = w.Completion(ctx, "o", store.AuditEvent{ID: "a"})
	time.Sleep(20 * time.Millisecond)
	_ = w.Completion(ctx, "o", store.AuditEvent{ID: "b"}) // fills the queue
	if err := w.Completion(ctx, "o", store.AuditEvent{ID: "c"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	close(s.block)
	w.Close()
	if s.count() != 2 {
		t.Fatalf("stored %d events, want the 2 accepted ones", s.count())
	}
}

type flakyStore struct {
	memStore
	fails int
}

func (f *flakyStore) InsertAudit(ctx context.Context, org store.OrgID, evs ...store.AuditEvent) error {
	f.mu.Lock()
	if f.fails > 0 {
		f.fails--
		f.mu.Unlock()
		return errors.New("database is locked")
	}
	f.mu.Unlock()
	return f.memStore.InsertAudit(ctx, org, evs...)
}

func TestFlushRetriesInsteadOfDropping(t *testing.T) {
	s := &flakyStore{fails: 3}
	w := NewWriter(s, Options{FlushInterval: time.Millisecond})
	_ = w.Completion(context.Background(), "o", store.AuditEvent{ID: "a"})
	w.Close()
	if s.count() != 1 {
		t.Fatalf("event lost after transient failures (stored %d)", s.count())
	}
}

func TestCancelledCallerStillRecorded(t *testing.T) {
	s := &memStore{}
	w := NewWriter(s, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Completion(ctx, "o", store.AuditEvent{ID: "a"}); err != nil {
		t.Fatalf("completion dropped for a cancelled caller: %v", err)
	}
	w.Close()
	if s.count() != 1 {
		t.Fatal("event not stored")
	}
}

func TestSendAfterCloseDoesNotPanic(t *testing.T) {
	w := NewWriter(&memStore{}, Options{})
	w.Close()
	if err := w.Completion(context.Background(), "o", store.AuditEvent{ID: "a"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}
