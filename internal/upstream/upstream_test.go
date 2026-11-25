package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/store"
)

// Regression tests for the milestone 1 security review.

func loopClient() *http.Client {
	return egress.NewClient(egress.Options{Policy: egress.Policy{Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}})
}

func locked(t *testing.T, endpoint string, headers map[string]string) *http.Client {
	t.Helper()
	c, err := originLocked(loopClient(), endpoint, headers)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRedirectDoesNotLeakCredential(t *testing.T) {
	got := make(chan string, 1)
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.WriteHeader(500)
	}))
	defer evil.Close()
	evilURL := strings.Replace(evil.URL, "127.0.0.1", "localhost", 1) // a different host
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evilURL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer up.Close()

	tr := &mcp.StreamableClientTransport{Endpoint: up.URL, HTTPClient: locked(t, up.URL, map[string]string{"Authorization": "Bearer UPSTREAM-SECRET"}),
		DisableStandaloneSSE: true, MaxRetries: -1}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(ctx, tr, nil)
	select {
	case h := <-got:
		t.Fatalf("redirect target received a request (Authorization=%q)", h)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestSSEEndpointOnOtherHostIsRefused(t *testing.T) {
	got := make(chan string, 1)
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.Header.Get("X-Api-Key"):
		default:
		}
		w.WriteHeader(202)
	}))
	defer evil.Close()
	evilURL := strings.Replace(evil.URL, "127.0.0.1", "localhost", 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: endpoint\ndata: %s/collect\n\n", evilURL)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer up.Close()
	tr := &mcp.SSEClientTransport{Endpoint: up.URL, HTTPClient: locked(t, up.URL, map[string]string{"X-Api-Key": "UPSTREAM-SECRET"})}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _, _ = mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(ctx, tr, nil) }()
	select {
	case h := <-got:
		t.Fatalf("foreign SSE endpoint received a request (X-Api-Key=%q)", h)
	case <-time.After(800 * time.Millisecond):
	}
}

func TestOriginLockAllowsSameOrigin(t *testing.T) {
	got := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("X-Api-Key")
	}))
	defer up.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, up.URL+"/other/path", nil)
	resp, err := locked(t, up.URL+"/mcp", map[string]string{"X-Api-Key": "k"}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if h := <-got; h != "k" {
		t.Fatalf("header %q not injected for the registered origin", h)
	}
}

type fakeStore struct {
	mu      sync.Mutex
	servers []store.Server
	delay   time.Duration
	prunes  []prune

	acceptSeen bool // PinStatuses reports every recorded definition as accepted
	seen       []store.Pin
}

// prune is one PrunePending call.
type prune struct {
	org      store.OrgID
	serverID string
	keep     []store.PinKey
}

func (f *fakeStore) PrunePending(_ context.Context, org store.OrgID, serverID string, keep []store.PinKey) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prunes = append(f.prunes, prune{org, serverID, append([]store.PinKey(nil), keep...)})
	return 0, nil
}

func (f *fakeStore) pruned() []prune {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]prune(nil), f.prunes...)
}

func (f *fakeStore) ListServers(context.Context, store.OrgID) ([]store.Server, error) {
	f.mu.Lock()
	out := append([]store.Server(nil), f.servers...)
	d := f.delay
	f.mu.Unlock()
	time.Sleep(d)
	return out, nil
}

func (f *fakeStore) ServerCredentials(context.Context, store.OrgID, string) ([]store.Credential, error) {
	return nil, nil
}

func (f *fakeStore) RecordSeen(_ context.Context, _ store.OrgID, pins []store.Pin) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, pins...)
	return nil
}

func (f *fakeStore) PinStatuses(_ context.Context, _ store.OrgID, serverID string) (map[store.PinKey]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[store.PinKey]string{}
	for _, p := range f.seen {
		if p.ServerID == serverID {
			out[store.PinKey{Kind: p.Kind, Name: p.Name, Hash: p.Hash}] = store.PinPending
			if f.acceptSeen {
				out[store.PinKey{Kind: p.Kind, Name: p.Name, Hash: p.Hash}] = store.PinAccepted
			}
		}
	}
	return out, nil
}

func TestStaleSyncCannotReenableDisabledServer(t *testing.T) {
	t0 := time.Now()
	srv := func(enabled bool, at time.Time) []store.Server {
		return []store.Server{{OrgID: "o", ID: "s1", Alias: "aa", Transport: "http", Config: store.ServerConfig{URL: "http://127.0.0.1:1"}, Enabled: enabled, UpdatedAt: at}}
	}
	fs := &fakeStore{servers: srv(true, t0)}
	m := New(Options{Store: fs, HTTPClient: loopClient(), RetryBase: time.Hour})
	defer m.Close()
	_ = m.Sync(context.Background(), "o")

	fs.mu.Lock()
	fs.delay = 200 * time.Millisecond
	fs.mu.Unlock()
	done := make(chan struct{})
	go func() { _ = m.Sync(context.Background(), "o"); close(done) }()
	time.Sleep(50 * time.Millisecond)
	fs.mu.Lock()
	fs.servers = srv(false, t0.Add(time.Second))
	fs.delay = 0
	fs.mu.Unlock()
	_ = m.Sync(context.Background(), "o")
	<-done
	if st, _ := m.State("s1"); st.Status != StatusDisabled {
		t.Fatalf("disabled server is %q after racing syncs", st.Status)
	}
}

func TestRedaction(t *testing.T) {
	raw := "https://mcp.example.com/v1/mcp?api_key=SECRET123"
	if got := RedactURL(raw); strings.Contains(got, "SECRET123") || got != "https://mcp.example.com/v1/mcp" {
		t.Fatalf("RedactURL = %q", got)
	}
	msg := redactError(`Post "`+raw+`": dial tcp: refused`, raw)
	if strings.Contains(msg, "SECRET123") {
		t.Fatalf("error not redacted: %q", msg)
	}
}
