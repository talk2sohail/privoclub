// Package health serves liveness and readiness endpoints (ADR 0010).
//
// Liveness only says the process is running. Readiness runs registered checks
// (database, migrations, master key, caches) and fails while draining.
// Upstream MCP servers are never readiness checks: an upstream outage must not
// make an orchestrator restart a healthy gateway.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// CheckFunc reports nil when the dependency is ready.
type CheckFunc func(ctx context.Context) error

// DefaultCheckTimeout bounds each readiness check.
const DefaultCheckTimeout = 2 * time.Second

// Registry holds readiness checks and the draining flag. Safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	checks   map[string]CheckFunc
	timeout  time.Duration
	draining atomic.Bool
}

// NewRegistry returns a registry whose checks each get timeout (DefaultCheckTimeout if zero).
func NewRegistry(timeout time.Duration) *Registry {
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	return &Registry{checks: map[string]CheckFunc{}, timeout: timeout}
}

// Register adds or replaces a named readiness check.
func (r *Registry) Register(name string, check CheckFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks[name] = check
}

// SetDraining makes readiness fail so load balancers stop sending traffic.
func (r *Registry) SetDraining(v bool) { r.draining.Store(v) }

// CheckResult is the outcome of one readiness check.
type CheckResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Report is the readiness response body.
type Report struct {
	Status string                 `json:"status"`
	Checks map[string]CheckResult `json:"checks"`
}

// Check runs every registered check concurrently and returns the report.
func (r *Registry) Check(ctx context.Context) Report {
	r.mu.RLock()
	names := make([]string, 0, len(r.checks))
	for name := range r.checks {
		names = append(names, name)
	}
	sort.Strings(names)
	checks := make([]CheckFunc, len(names))
	for i, name := range names {
		checks[i] = r.checks[name]
	}
	r.mu.RUnlock()

	results := make([]CheckResult, len(names))
	var wg sync.WaitGroup
	for i := range names {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = r.run(ctx, checks[i])
		}(i)
	}
	wg.Wait()

	report := Report{Status: "ok", Checks: make(map[string]CheckResult, len(names))}
	for i, name := range names {
		report.Checks[name] = results[i]
		if !results[i].OK {
			report.Status = "unavailable"
		}
	}
	if r.draining.Load() {
		report.Status = "draining"
	}
	return report
}

func (r *Registry) run(ctx context.Context, check CheckFunc) (res CheckResult) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- errPanic
			}
		}()
		done <- check(ctx)
	}()
	select {
	case err := <-done:
		if err != nil {
			return CheckResult{Error: err.Error()}
		}
		return CheckResult{OK: true}
	case <-ctx.Done():
		return CheckResult{Error: "timed out"}
	}
}

type panicError struct{}

func (panicError) Error() string { return "check panicked" }

var errPanic error = panicError{}

// LiveHandler always answers 200 while the process can serve HTTP.
func LiveHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("ok\n"))
	})
}

// ReadyHandler answers 200 when every check passes and the server is not draining, else 503.
func (r *Registry) ReadyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		report := r.Check(req.Context())
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if report.Status != "ok" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(report)
	})
}
