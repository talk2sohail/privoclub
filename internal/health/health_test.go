package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func ready(t *testing.T, r *Registry) (int, Report) {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ReadyHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var rep Report
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rec.Code, rep
}

func TestLive(t *testing.T) {
	rec := httptest.NewRecorder()
	LiveHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestReadyWithNoChecks(t *testing.T) {
	code, rep := ready(t, NewRegistry(0))
	if code != http.StatusOK || rep.Status != "ok" {
		t.Fatalf("code=%d rep=%+v", code, rep)
	}
}

func TestReadyFailsWhenACheckFails(t *testing.T) {
	r := NewRegistry(0)
	r.Register("db", func(context.Context) error { return nil })
	r.Register("key", func(context.Context) error { return errors.New("master key not loaded") })
	code, rep := ready(t, r)
	if code != http.StatusServiceUnavailable || rep.Status != "unavailable" {
		t.Fatalf("code=%d rep=%+v", code, rep)
	}
	if !rep.Checks["db"].OK || rep.Checks["key"].OK || rep.Checks["key"].Error == "" {
		t.Fatalf("checks = %+v", rep.Checks)
	}
}

func TestReadyTimesOutSlowCheck(t *testing.T) {
	r := NewRegistry(20 * time.Millisecond)
	r.Register("slow", func(ctx context.Context) error {
		select {
		case <-time.After(time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	start := time.Now()
	code, rep := ready(t, r)
	if code != http.StatusServiceUnavailable || rep.Checks["slow"].OK {
		t.Fatalf("code=%d rep=%+v", code, rep)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("slow check was not cut off")
	}
}

func TestReadyRecoversPanickingCheck(t *testing.T) {
	r := NewRegistry(0)
	r.Register("bad", func(context.Context) error { panic("boom") })
	code, rep := ready(t, r)
	if code != http.StatusServiceUnavailable || rep.Checks["bad"].Error != "check panicked" {
		t.Fatalf("code=%d rep=%+v", code, rep)
	}
}

func TestReadyFailsWhileDraining(t *testing.T) {
	r := NewRegistry(0)
	r.SetDraining(true)
	code, rep := ready(t, r)
	if code != http.StatusServiceUnavailable || rep.Status != "draining" {
		t.Fatalf("code=%d rep=%+v", code, rep)
	}
}
