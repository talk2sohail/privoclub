package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"privo.club/privo/internal/buildinfo"
	"privo.club/privo/internal/health"
)

func start(t *testing.T, opts Options) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(opts).Serve(ctx, ln) }()
	return "http://" + ln.Addr().String(), cancel, done
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx // test helper
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestEndpoints(t *testing.T) {
	base, cancel, done := start(t, Options{})
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()

	if code, _ := get(t, base+"/livez"); code != http.StatusOK {
		t.Fatalf("/livez = %d", code)
	}
	if code, _ := get(t, base+"/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz = %d", code)
	}
	code, body := get(t, base+"/version")
	var v map[string]string
	if code != http.StatusOK || json.Unmarshal([]byte(body), &v) != nil || v["version"] != buildinfo.Version {
		t.Fatalf("/version = %d %q, want version %q", code, body, buildinfo.Version)
	}
	if _, hash := v["commit"]; hash || len(v) != 2 {
		t.Fatalf("/version = %q, want only version and go_version", body)
	}
	if code, _ := get(t, base+"/unknown"); code != http.StatusNotFound {
		t.Fatalf("/unknown = %d", code)
	}
}

func TestGracefulShutdownFinishesInFlightRequests(t *testing.T) {
	reg := health.NewRegistry(0)
	release := make(chan struct{})
	entered := make(chan struct{})
	base, cancel, done := start(t, Options{
		Health:          reg,
		ShutdownTimeout: 5 * time.Second,
		Routes: func(mux *http.ServeMux) {
			mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
				close(entered)
				<-release
				_, _ = w.Write([]byte("finished"))
			})
		},
	})

	result := make(chan string, 1)
	go func() { _, body := get(t, base+"/slow"); result <- body }()
	<-entered

	cancel()
	// Readiness flips to draining before the in-flight request completes.
	deadline := time.Now().Add(2 * time.Second)
	for reg.Check(context.Background()).Status != "draining" {
		if time.Now().After(deadline) {
			t.Fatal("registry never reported draining")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)

	if body := <-result; body != "finished" {
		t.Fatalf("in-flight request got %q", body)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func TestShutdownTimeoutForcesClose(t *testing.T) {
	entered := make(chan struct{})
	base, cancel, done := start(t, Options{
		ShutdownTimeout: 50 * time.Millisecond,
		Routes: func(mux *http.ServeMux) {
			mux.HandleFunc("GET /stuck", func(_ http.ResponseWriter, r *http.Request) {
				close(entered)
				<-r.Context().Done()
			})
		},
	})
	go func() {
		resp, err := http.Get(base + "/stuck")
		if err == nil {
			_ = resp.Body.Close()
		}
	}() //nolint:noctx // test
	<-entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a deadline error from a forced shutdown")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not respect the timeout")
	}
}
