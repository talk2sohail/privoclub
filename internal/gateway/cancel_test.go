package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
	"privo.club/privo/internal/token"
	"privo.club/privo/internal/upstream"
	"privo.club/privo/internal/vault"
)

// pipeListener is an in-memory net.Listener backed by net.Pipe so tests can
// exercise real net/http servers and cancellation without opening TCP sockets.
type pipeListener struct {
	conns  chan net.Conn
	done   chan struct{}
	once   sync.Once
	closed bool
	mu     sync.Mutex
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
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
		_ = client.Close()
		_ = server.Close()
		return nil, net.ErrClosed
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		return nil, ctx.Err()
	}
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func serveInMemoryHTTP(t *testing.T, handler http.Handler) (string, *http.Client) {
	t.Helper()
	ln := newPipeListener()
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
	})
	tr := &http.Transport{DialContext: ln.DialContext}
	t.Cleanup(tr.CloseIdleConnections)
	return "http://pipe.local", &http.Client{Transport: tr}
}

type bearerRT struct {
	token string
	next  http.RoundTripper
}

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

type slowUpstreamProbe struct {
	started   chan struct{}
	cancelled chan struct{}
	handler   http.Handler
}

func newSlowHTTPUpstream(t *testing.T, stateless bool) *slowUpstreamProbe {
	t.Helper()
	p := &slowUpstreamProbe{
		started:   make(chan struct{}, 8),
		cancelled: make(chan struct{}, 8),
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "slow", Description: "Waits until cancelled."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			p.started <- struct{}{}
			select {
			case <-ctx.Done():
				p.cancelled <- struct{}{}
				return nil, nil, ctx.Err()
			case <-time.After(10 * time.Second):
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "timed-out-instead"}}}, nil, nil
			}
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "ping", Description: "Immediate pong."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
		})
	opts := &mcp.StreamableHTTPOptions{}
	if stateless {
		opts.Stateless = true
		opts.PropagateRequestCancellation = true
	}
	p.handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, opts)
	return p
}

type testGatewayHarness struct {
	st      *store.Store
	org     store.OrgID
	aw      *audit.Writer
	rec     *telemetry.Recorder
	mgr     *upstream.Manager
	app     *approval.Service
	gw      *Gateway
	tok     token.Token
	baseURL string
	client  *http.Client
}

func newCancelGatewayHarness(t *testing.T, upstreamStateless bool) (*testGatewayHarness, *slowUpstreamProbe) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
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

	probe := newSlowHTTPUpstream(t, upstreamStateless)
	upURL, upClient := serveInMemoryHTTP(t, probe.handler)

	now := time.Now()
	tok, err := token.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateToken(ctx, store.APIToken{
		OrgID: org.ID, ID: tok.ID, Name: "ada", Role: store.RoleMember,
		Kind: store.KindGateway, Person: "ada", SecretHash: tok.SecretHash,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	srv := store.Server{
		OrgID: org.ID, ID: "srv-1", Alias: "up", Transport: "http",
		Config: store.ServerConfig{URL: upURL + "/mcp"}, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateServer(ctx, srv, nil); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	aw := audit.NewWriter(st, audit.Options{FlushInterval: 10 * time.Millisecond, Logger: logger})
	t.Cleanup(aw.Close)
	rec := telemetry.New(st, telemetry.Options{FlushInterval: 10 * time.Millisecond, Logger: logger})
	t.Cleanup(rec.Close)

	mgr := upstream.New(upstream.Options{
		Store:      st,
		Keys:       kr,
		HTTPClient: upClient,
		RetryBase:  20 * time.Millisecond,
		RetryMax:   50 * time.Millisecond,
		Logger:     logger,
	})
	t.Cleanup(mgr.Close)
	if err := mgr.Sync(ctx, org.ID); err != nil {
		t.Fatal(err)
	}
	mgr.WaitSettled(ctx, "srv-1", 5*time.Second)

	pins, err := st.ListPins(ctx, org.ID, "srv-1")
	if err != nil || len(pins) == 0 {
		t.Fatalf("pins = %v err = %v", pins, err)
	}
	var accept []store.PinKey
	for _, p := range pins {
		accept = append(accept, store.PinKey{Kind: p.Kind, Name: p.Name, Hash: p.Hash})
	}
	if _, err := st.AcceptPins(ctx, org.ID, "srv-1", accept); err != nil {
		t.Fatal(err)
	}

	app := &approval.Service{Store: st, Keys: kr, Hold: 5 * time.Second, Poll: 15 * time.Millisecond}
	gw := &Gateway{
		Store:       st,
		Upstream:    mgr,
		Keys:        kr,
		Audit:       aw,
		Auth:        auth.New(st),
		Approval:    app,
		Telemetry:   rec,
		CallTimeout: 10 * time.Second,
		Logger:      logger,
	}
	baseURL, gwClient := serveInMemoryHTTP(t, gw.Handler())
	gwClient.Transport = bearerRT{token: tok.Plain, next: gwClient.Transport}
	return &testGatewayHarness{
		st: st, org: org.ID, aw: aw, rec: rec, mgr: mgr, app: app, gw: gw, tok: tok,
		baseURL: baseURL, client: gwClient,
	}, probe
}

func connectTestGatewayClient(t *testing.T, h *testGatewayHarness, protocolVersion string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	var opts *mcp.ClientSessionOptions
	if protocolVersion != "" {
		opts = &mcp.ClientSessionOptions{ProtocolVersion: protocolVersion}
	}
	s, err := c.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             h.baseURL + "/mcp",
		HTTPClient:           h.client,
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, opts)
	if err != nil {
		t.Fatalf("connect(%q): %v", protocolVersion, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestClientCancelPropagatesToUpstreamHTTP(t *testing.T) {
	cases := []struct {
		name              string
		clientProtocol    string
		upstreamStateless bool
	}{
		{name: "2026-07-28 client to stateful upstream", clientProtocol: "2026-07-28", upstreamStateless: false},
		{name: "2025-11-25 client to stateful upstream", clientProtocol: "2025-11-25", upstreamStateless: false},
		{name: "2026-07-28 client to stateless upstream", clientProtocol: "2026-07-28", upstreamStateless: true},
		{name: "2025-11-25 client to stateless upstream", clientProtocol: "2025-11-25", upstreamStateless: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, probe := newCancelGatewayHarness(t, tc.upstreamStateless)
			cs := connectTestGatewayClient(t, h, tc.clientProtocol)

			callCtx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				_, err := cs.CallTool(callCtx, &mcp.CallToolParams{Name: "up__slow"})
				done <- err
			}()

			select {
			case <-probe.started:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream slow tool never started")
			}
			cancel()

			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("CallTool err = %v, want context.Canceled", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("CallTool did not return after cancel")
			}

			select {
			case <-probe.cancelled:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream HTTP handler did not observe cancellation")
			}

			// Neither the client session nor the upstream session was dropped.
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "up__ping"})
			if err != nil || res.IsError || res.Content[0].(*mcp.TextContent).Text != "pong" {
				t.Fatalf("subsequent ping after cancel: res=%+v err=%v", res, err)
			}
		})
	}
}

func TestCancelledCallStillWritesAuditCompletion(t *testing.T) {
	t.Run("upstream call cancelled", func(t *testing.T) {
		h, probe := newCancelGatewayHarness(t, false)
		cs := connectTestGatewayClient(t, h, "2026-07-28")

		callCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := cs.CallTool(callCtx, &mcp.CallToolParams{Name: "up__slow"})
			done <- err
		}()
		select {
		case <-probe.started:
		case <-time.After(3 * time.Second):
			t.Fatal("upstream tool never started")
		}
		cancel()
		_ = <-done
		select {
		case <-probe.cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("upstream never observed cancellation")
		}

		// Flush audit & telemetry queues before querying the store.
		h.aw.Close()
		h.rec.Close()

		calls, err := h.st.RecentCalls(context.Background(), h.org, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || calls[0].Completion == nil || calls[0].Completion.Outcome != "cancelled" {
			t.Fatalf("audit calls = %+v, want 1 call with completion outcome \"cancelled\"", calls)
		}
		stats, _, err := h.st.CallStats(context.Background(), h.org, store.CallStatQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if len(stats) != 1 || stats[0].Outcome != store.OutcomeError || stats[0].ErrorClass != "cancelled" {
			t.Fatalf("call stats = %+v, want outcome=error error_class=cancelled", stats)
		}
	})

	t.Run("approval wait cancelled writes refusal and withdraw audits", func(t *testing.T) {
		h, _ := newCancelGatewayHarness(t, false)
		ctx := context.Background()
		if err := h.st.CreatePolicies(ctx, h.org, store.Policy{
			ID: "ask-up", Server: "up", Tool: "*", Subject: "*", Action: store.ActionAsk, CreatedBy: "test", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		cs := connectTestGatewayClient(t, h, "2026-07-28")

		callCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := cs.CallTool(callCtx, &mcp.CallToolParams{Name: "up__ping"})
			done <- err
		}()

		var pendingID string
		deadline := time.Now().Add(3 * time.Second)
		for pendingID == "" && time.Now().Before(deadline) {
			list, _ := h.st.ListApprovals(ctx, h.org, store.ApprovalPending, 10)
			if len(list) == 1 {
				pendingID = list[0].ID
			}
			time.Sleep(10 * time.Millisecond)
		}
		if pendingID == "" {
			t.Fatal("pending approval never created")
		}
		cancel()
		_ = <-done

		// Wait briefly for the gateway handler's post-cancel withdrawal to finish.
		deadline = time.Now().Add(3 * time.Second)
		var ap store.Approval
		for time.Now().Before(deadline) {
			ap, _ = h.st.GetApproval(ctx, h.org, pendingID)
			if ap.Status == store.ApprovalWithdrawn {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if ap.Status != store.ApprovalWithdrawn || ap.DecidedBy != "caller cancelled" || len(ap.Args) != 0 {
			t.Fatalf("approval row = %+v, want withdrawn with empty args", ap)
		}

		h.aw.Close()
		h.rec.Close()

		calls, err := h.st.RecentCalls(ctx, h.org, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || calls[0].Intent.Decision != store.ActionAsk || calls[0].Completion == nil || calls[0].Completion.Outcome != "cancelled_while_pending" {
			t.Fatalf("refusal audit calls = %+v", calls)
		}
		admins, err := h.st.RecentAdminEvents(ctx, h.org, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(admins) != 1 || admins[0].Action != "approval.withdraw:"+pendingID || admins[0].ActorID != h.tok.ID {
			t.Fatalf("admin audit = %+v, want approval.withdraw:%s", admins, pendingID)
		}
		stats, _, err := h.st.CallStats(ctx, h.org, store.CallStatQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if len(stats) != 1 || stats[0].Outcome != store.OutcomeRefused || stats[0].ErrorClass != "cancelled" {
			t.Fatalf("call stats = %+v, want outcome=refused error_class=cancelled", stats)
		}
	})
}
