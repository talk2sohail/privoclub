package upstream

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/runner"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

type slowInput struct {
	Ms int `json:"ms"`
}

type echoInput struct {
	Text string `json:"text"`
}

// TestHelperStdioMCPServer is re-exec'd as a stdio MCP subprocess by the cancel
// tests below using runner.Local and mcp.CommandTransport.
func TestHelperStdioMCPServer(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	marker := os.Getenv("CANCEL_MARKER")
	var mu sync.Mutex
	record := func(line string) {
		if marker == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if f, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.WriteString(line + "\n")
			_ = f.Close()
		}
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "stdio-cancel-probe", Version: "0.0.1"}, nil)
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "notifications/cancelled" {
				record("notify:cancelled")
			}
			return next(ctx, method, req)
		}
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "slow", Description: "Wait ms or until cancelled."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in slowInput) (*mcp.CallToolResult, any, error) {
			record("slow:started")
			d := time.Duration(in.Ms) * time.Millisecond
			if d <= 0 {
				d = 10 * time.Second
			}
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				record("slow:cancelled")
				return nil, nil, ctx.Err()
			case <-timer.C:
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil, nil
			}
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Echo text."},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, nil, nil
		})

	_ = srv.Run(context.Background(), &mcp.StdioTransport{})
	os.Exit(0)
}

func setupStdioCancelManager(t *testing.T) (*Manager, store.OrgID, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, err := st.EnsureOrg(ctx, "default", "Default")
	if err != nil {
		t.Fatal(err)
	}
	kek, _ := vault.NewKey()
	kr := keyring.New(st, vault.StaticKeyProvider(kek, nil))
	if err := kr.EnsureOrgKey(ctx, org.ID); err != nil {
		t.Fatal(err)
	}
	ver, dek, err := kr.Current(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := runner.FileDigest(exe)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "cancel.marker")
	seal := func(id, name, val string) store.Credential {
		purpose := "env:" + name
		ct, err := vault.Seal(dek, []byte(val), vault.Binding{
			OrgID: string(org.ID), CredentialID: id, ServerID: "srv-stdio", Purpose: purpose, KeyVersion: ver, RowVersion: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return store.Credential{ID: id, ServerID: "srv-stdio", Purpose: purpose, KeyVersion: ver, RowVersion: 1, Ciphertext: ct}
	}
	srv := store.Server{
		OrgID: org.ID, ID: "srv-stdio", Alias: "stdio", Transport: "stdio", Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		Config: store.ServerConfig{
			Path:     exe,
			SHA256:   digest,
			Args:     []string{"-test.run=^TestHelperStdioMCPServer$"},
			EnvNames: []string{"GO_WANT_HELPER_PROCESS", "CANCEL_MARKER"},
		},
	}
	if err := st.CreateServer(ctx, srv, []store.Credential{
		seal("env-1", "GO_WANT_HELPER_PROCESS", "1"),
		seal("env-2", "CANCEL_MARKER", marker),
	}); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := New(Options{
		Store:     st,
		Keys:      kr,
		Runner:    runner.Local{},
		RetryBase: 20 * time.Millisecond,
		RetryMax:  50 * time.Millisecond,
		Logger:    logger,
	})
	t.Cleanup(m.Close)
	if err := m.Sync(ctx, org.ID); err != nil {
		t.Fatal(err)
	}
	m.WaitSettled(ctx, "srv-stdio", 5*time.Second)
	if st, ok := m.State("srv-stdio"); !ok || st.Status != StatusUp {
		t.Fatalf("stdio server state = %+v (ok=%v), want up", st, ok)
	}
	return m, org.ID, marker
}

func waitForMarker(t *testing.T, marker, substr string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(marker)
		if strings.Contains(string(b), substr) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	b, _ := os.ReadFile(marker)
	t.Fatalf("timed out waiting for %q in marker %q (got %q)", substr, marker, string(b))
}

func TestClientCancelSendsCancelledNotificationToStdio(t *testing.T) {
	m, org, marker := setupStdioCancelManager(t)
	sess, err := m.Session(org, "srv-stdio")
	if err != nil {
		t.Fatal(err)
	}

	callCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := sess.CallTool(callCtx, &mcp.CallToolParams{Name: "slow", Arguments: map[string]any{"ms": 10000}})
		done <- err
	}()

	waitForMarker(t, marker, "slow:started")
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CallTool err = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CallTool did not return promptly on cancel")
	}

	waitForMarker(t, marker, "notify:cancelled")
	waitForMarker(t, marker, "slow:cancelled")

	// Session must remain open and usable after cancellation.
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "still-alive"}})
	if err != nil || res.IsError || res.Content[0].(*mcp.TextContent).Text != "still-alive" {
		t.Fatalf("echo after cancel: res=%+v err=%v", res, err)
	}
	if st, _ := m.State("srv-stdio"); st.Status != StatusUp {
		t.Fatalf("server state after cancel = %+v, want up", st)
	}
}

// pipeListener lets Streamable HTTP cancellation tests run without OS TCP sockets.
type pipeListener struct {
	conns  chan net.Conn
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn, 16), done: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() {
		l.mu.Lock()
		l.closed = true
		close(l.done)
		l.mu.Unlock()
	})
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

func (l *pipeListener) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return nil, net.ErrClosed
	}
	c, s := net.Pipe()
	select {
	case l.conns <- s:
		return c, nil
	case <-l.done:
		_ = c.Close()
		_ = s.Close()
		return nil, net.ErrClosed
	case <-ctx.Done():
		_ = c.Close()
		_ = s.Close()
		return nil, ctx.Err()
	}
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func TestCancelOneCallDoesNotDropOtherCalls(t *testing.T) {
	t.Run("stdio", func(t *testing.T) {
		m, org, marker := setupStdioCancelManager(t)
		sess, err := m.Session(org, "srv-stdio")
		if err != nil {
			t.Fatal(err)
		}

		ctx1, cancel1 := context.WithCancel(context.Background())
		done1 := make(chan error, 1)
		go func() {
			_, err := sess.CallTool(ctx1, &mcp.CallToolParams{Name: "slow", Arguments: map[string]any{"ms": 10000}})
			done1 <- err
		}()
		waitForMarker(t, marker, "slow:started")

		type callOut struct {
			text string
			err  error
		}
		done2 := make(chan callOut, 1)
		go func() {
			res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "slow", Arguments: map[string]any{"ms": 120}})
			var txt string
			if err == nil && len(res.Content) > 0 {
				txt = res.Content[0].(*mcp.TextContent).Text
			}
			done2 <- callOut{txt, err}
		}()

		// Cancel call 1 while call 2 is concurrently running on the same session.
		cancel1()
		if err := <-done1; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled call err = %v, want context.Canceled", err)
		}
		waitForMarker(t, marker, "slow:cancelled")

		out2 := <-done2
		if out2.err != nil || out2.text != "done" {
			t.Fatalf("concurrent call failed: %+v", out2)
		}
		if st, _ := m.State("srv-stdio"); st.Status != StatusUp {
			t.Fatalf("server state = %+v, want up", st)
		}
	})

	t.Run("streamable_http", func(t *testing.T) {
		ctx := context.Background()
		started := make(chan string, 4)
		cancelled := make(chan struct{}, 4)

		srv := mcp.NewServer(&mcp.Implementation{Name: "http-cancel-probe", Version: "0.0.1"}, nil)
		mcp.AddTool(srv, &mcp.Tool{Name: "slow", Description: "Wait ms or until cancelled."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in slowInput) (*mcp.CallToolResult, any, error) {
				if in.Ms >= 5000 {
					started <- "long"
				} else {
					started <- "short"
				}
				timer := time.NewTimer(time.Duration(in.Ms) * time.Millisecond)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					if in.Ms >= 5000 {
						cancelled <- struct{}{}
					}
					return nil, nil, ctx.Err()
				case <-timer.C:
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil, nil
				}
			})

		ln := newPipeListener()
		httpSrv := &http.Server{Handler: mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)}
		go func() { _ = httpSrv.Serve(ln) }()
		t.Cleanup(func() { _ = httpSrv.Close(); _ = ln.Close() })
		tr := &http.Transport{DialContext: ln.DialContext}
		t.Cleanup(tr.CloseIdleConnections)

		fs := &fakeStore{servers: []store.Server{{
			OrgID: "o", ID: "srv-http", Alias: "http", Transport: "http",
			Config: store.ServerConfig{URL: "http://pipe.local/mcp"}, Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}}}
		m := New(Options{
			Store:      fs,
			HTTPClient: &http.Client{Transport: tr},
			RetryBase:  20 * time.Millisecond,
			RetryMax:   50 * time.Millisecond,
			Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		t.Cleanup(m.Close)
		if err := m.Sync(ctx, "o"); err != nil {
			t.Fatal(err)
		}
		m.WaitSettled(ctx, "srv-http", 5*time.Second)
		sess, err := m.Session("o", "srv-http")
		if err != nil {
			t.Fatal(err)
		}

		ctx1, cancel1 := context.WithCancel(ctx)
		done1 := make(chan error, 1)
		go func() {
			_, err := sess.CallTool(ctx1, &mcp.CallToolParams{Name: "slow", Arguments: map[string]any{"ms": 10000}})
			done1 <- err
		}()
		if got := <-started; got != "long" {
			t.Fatalf("started = %q, want long", got)
		}

		type callOut struct {
			text string
			err  error
		}
		done2 := make(chan callOut, 1)
		go func() {
			res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "slow", Arguments: map[string]any{"ms": 150}})
			var txt string
			if err == nil && len(res.Content) > 0 {
				txt = res.Content[0].(*mcp.TextContent).Text
			}
			done2 <- callOut{txt, err}
		}()
		if got := <-started; got != "short" {
			t.Fatalf("second started = %q, want short", got)
		}

		cancel1()
		if err := <-done1; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled call err = %v, want context.Canceled", err)
		}
		select {
		case <-cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("http upstream never observed cancel on long call")
		}
		out2 := <-done2
		if out2.err != nil || out2.text != "done" {
			t.Fatalf("concurrent short call failed: %+v", out2)
		}
		if st, _ := m.State("srv-http"); st.Status != StatusUp {
			t.Fatalf("http server state = %+v, want up", st)
		}
	})
}
