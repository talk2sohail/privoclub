package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/buildinfo"
	"privo.club/privo/internal/grants"
	"privo.club/privo/internal/safe"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
)

// Per-person sessions for servers whose calls run with each person's own OAuth
// grant (ADR 0017). The server's catalog is still loaded once, on the
// supervised session, with the catalog grant; calls use a session that carries
// the caller's grant, kept in a pool.

// Pool bounds (ADR 0017 point 6).
const (
	DefaultPoolIdle = 5 * time.Minute
	DefaultPoolCap  = 500
)

// GrantSource hands out access tokens of upstream grants (grants.Service).
type GrantSource interface {
	AccessToken(ctx context.Context, org store.OrgID, srv store.Server, userID string) (string, error)
	CatalogAccessToken(ctx context.Context, org store.OrgID, srv store.Server) (userID, token string, err error)
	RefreshAfterReject(ctx context.Context, org store.OrgID, srv store.Server, userID, rejected string) (string, error)
}

// catalogPasser is the optional part of a GrantSource that moves a server's
// catalog to another administrator's key (grants.Service).
type catalogPasser interface {
	PassCatalog(ctx context.Context, org store.OrgID, srv store.Server, userID string) (bool, error)
}

// PerPerson reports whether calls to the server run with each person's grant.
func (m *Manager) PerPerson(serverID string) bool {
	m.mu.Lock()
	e, ok := m.entries[serverID]
	m.mu.Unlock()
	return ok && e.srv.Config.PerPerson()
}

// Server returns a copy of the registered server configuration for serverID in
// org when known to the manager.
func (m *Manager) Server(org store.OrgID, serverID string) (store.Server, bool) {
	m.mu.Lock()
	e, ok := m.entries[serverID]
	m.mu.Unlock()
	if !ok || e.org != org {
		return store.Server{}, false
	}
	return e.srv, true
}

// CallSecret returns the active access token or personal API key of userID for
// serverID in org, for redacting echoes from that caller's own call output.
// It reads only the caller's own grant and never falls back to another person.
func (m *Manager) CallSecret(ctx context.Context, org store.OrgID, serverID, userID string) (string, error) {
	if userID == "" || m.opts.Grants == nil {
		return "", nil
	}
	m.mu.Lock()
	e, ok := m.entries[serverID]
	m.mu.Unlock()
	if !ok || e.org != org || !e.srv.Config.PerPerson() {
		return "", nil
	}
	return m.opts.Grants.AccessToken(ctx, org, e.srv, userID)
}

type poolKey struct{ serverID, userID string }

type pooled struct {
	session  *mcp.ClientSession
	cancel   context.CancelFunc
	lastUsed atomic.Int64
	// version is the server's updated_at when the session was made: a
	// session to a server since edited (another address, ADR 0029) is
	// never used again.
	version time.Time
}

type pending struct {
	done chan struct{}
	p    *pooled
	err  error
}

// PersonSession returns a session to an oauth server that carries the person's
// own grant, connecting one when the pool has none. It returns the grant
// source's error (grants.ErrNotConnected) when the person has no grant.
func (m *Manager) PersonSession(ctx context.Context, org store.OrgID, serverID, userID string) (*mcp.ClientSession, error) {
	m.mu.Lock()
	e, ok := m.entries[serverID]
	m.mu.Unlock()
	if !ok || e.org != org || !e.srv.Config.PerPerson() || m.opts.Grants == nil {
		return nil, ErrUnavailable
	}
	// The grant is checked on every call, so a disconnected or disabled
	// person stops at once even while a pooled session is open.
	if _, err := m.opts.Grants.AccessToken(ctx, org, e.srv, userID); err != nil {
		return nil, err
	}
	key := poolKey{serverID, userID}
	m.poolMu.Lock()
	if p, ok := m.pool[key]; ok {
		if p.version.Equal(e.srv.UpdatedAt) {
			p.lastUsed.Store(time.Now().UnixNano())
			m.poolMu.Unlock()
			return p.session, nil
		}
		// Made for an older version of the server: never reuse it.
		delete(m.pool, key)
		_ = p.session.Close()
	}
	if w, ok := m.connecting[key]; ok {
		m.poolMu.Unlock()
		select {
		case <-w.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if w.err != nil {
			return nil, w.err
		}
		return w.p.session, nil
	}
	w := &pending{done: make(chan struct{})}
	if m.connecting == nil {
		m.connecting = map[poolKey]*pending{}
	}
	m.connecting[key] = w
	m.startReaper()
	m.poolMu.Unlock()

	w.p, w.err = m.connectPerson(e, userID) //nolint:contextcheck // the connection outlives this call and must not carry the caller's values

	// The server may have been edited while this connected: a session made
	// for the old settings is not pooled or used.
	m.mu.Lock()
	cur, ok := m.entries[serverID]
	m.mu.Unlock()
	if w.err == nil && (!ok || cur != e) {
		_ = w.p.session.Close()
		w.p.cancel()
		w.p, w.err = nil, ErrUnavailable
	}
	m.poolMu.Lock()
	delete(m.connecting, key)
	if w.err == nil {
		if m.pool == nil {
			m.pool = map[poolKey]*pooled{}
		}
		m.pool[key] = w.p
		m.evictLocked()
	}
	m.poolMu.Unlock()
	close(w.done)
	if w.err != nil {
		return nil, w.err
	}
	go func(p *pooled) {
		_ = p.session.Wait()
		m.poolMu.Lock()
		if m.pool[key] == p {
			delete(m.pool, key)
		}
		m.poolMu.Unlock()
		p.cancel()
	}(w.p)
	return w.p.session, nil
}

func (m *Manager) connectPerson(e *entry, userID string) (*pooled, error) {
	srv := e.srv
	org := e.org
	auth := bearer{
		header: credentialHeader(srv),
		key:    srv.Config.Auth == store.AuthKey,
		token:  func(c context.Context) (string, error) { return m.opts.Grants.AccessToken(c, org, srv, userID) },
		refresh: func(c context.Context, old string) (string, error) {
			return m.opts.Grants.RefreshAfterReject(c, org, srv, userID, old)
		},
	}
	transport, err := m.httpTransport(srv, nil, &auth)
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "privo", Version: buildinfo.Version}, nil)
	client.AddReceivingMiddleware(safe.Recover(m.opts.Logger, "upstream "+srv.Alias))
	// As in connect: the context of Connect must outlive the handshake (SSE
	// ties its stream to it), so a timer bounds the handshake instead. It is a
	// fresh context, not one derived from the caller's: the caller's is the AI
	// client's request, and the SDK reads the client's MCP protocol version
	// from it, so the upstream would be offered the client's version instead
	// of negotiating its own (found by the acceptance test: a 2026-07-28
	// client against a stateful 2025-11-25 server).
	runCtx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(m.opts.ConnectTimeout, cancel)
	session, err := client.Connect(runCtx, transport, nil) //nolint:contextcheck // a fresh context on purpose, see above
	if !timer.Stop() || err != nil {
		cancel()
		if session != nil {
			_ = session.Close()
		}
		if err == nil {
			err = context.Canceled
		}
		return nil, fmt.Errorf("connect: %w", err)
	}
	p := &pooled{session: session, cancel: cancel, version: srv.UpdatedAt}
	p.lastUsed.Store(time.Now().UnixNano())
	return p, nil
}

// evictLocked closes the least recently used sessions above the cap.
func (m *Manager) evictLocked() {
	limit := m.opts.PoolCap
	if len(m.pool) <= limit {
		return
	}
	type kv struct {
		k poolKey
		p *pooled
	}
	all := make([]kv, 0, len(m.pool))
	for k, p := range m.pool {
		all = append(all, kv{k, p})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].p.lastUsed.Load() < all[j].p.lastUsed.Load() })
	for _, x := range all[:len(all)-limit] {
		delete(m.pool, x.k)
		go func(p *pooled) { _ = p.session.Close(); p.cancel() }(x.p)
	}
}

// startReaper starts, once, the goroutine that closes idle sessions. Call with
// poolMu held.
func (m *Manager) startReaper() {
	if m.reaperStop != nil {
		return
	}
	stop := make(chan struct{})
	m.reaperStop = stop
	every := min(m.opts.PoolIdle/2, time.Minute)
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				m.reapIdle(time.Now())
			}
		}
	}()
}

func (m *Manager) reapIdle(now time.Time) {
	cutoff := now.Add(-m.opts.PoolIdle).UnixNano()
	var idle []*pooled
	m.poolMu.Lock()
	for k, p := range m.pool {
		if p.lastUsed.Load() <= cutoff {
			delete(m.pool, k)
			idle = append(idle, p)
		}
	}
	m.poolMu.Unlock()
	for _, p := range idle {
		_ = p.session.Close()
		p.cancel()
	}
}

// ClosePerson closes the pooled sessions of one person (disconnect), or of
// every person when userID is empty, for a server.
func (m *Manager) ClosePerson(serverID, userID string) {
	var closing []*pooled
	m.poolMu.Lock()
	for k, p := range m.pool {
		if k.serverID == serverID && (userID == "" || k.userID == userID) {
			delete(m.pool, k)
			closing = append(closing, p)
		}
	}
	m.poolMu.Unlock()
	for _, p := range closing {
		_ = p.session.Close()
		p.cancel()
	}
}

// CloseUser closes every pooled session of one person (a removed member).
func (m *Manager) CloseUser(userID string) {
	var closing []*pooled
	m.poolMu.Lock()
	for k, p := range m.pool {
		if k.userID == userID {
			delete(m.pool, k)
			closing = append(closing, p)
		}
	}
	m.poolMu.Unlock()
	for _, p := range closing {
		_ = p.session.Close()
		p.cancel()
	}
}

// PoolSize is the number of pooled per-person sessions.
func (m *Manager) PoolSize() int {
	m.poolMu.Lock()
	defer m.poolMu.Unlock()
	return len(m.pool)
}

func (m *Manager) closePool() {
	m.poolMu.Lock()
	all := m.pool
	m.pool = nil
	if m.reaperStop != nil {
		close(m.reaperStop)
		m.reaperStop = nil
	}
	m.poolMu.Unlock()
	for _, p := range all {
		_ = p.session.Close()
		p.cancel()
	}
}

// Kick ends a server's wait before its next connection attempt: called when a
// catalog grant appears, so the server comes up without waiting for backoff.
func (m *Manager) Kick(serverID string) {
	m.mu.Lock()
	e, ok := m.entries[serverID]
	m.mu.Unlock()
	if !ok || e.kick == nil {
		return
	}
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// bearer adds a person's credential to each request: an OAuth access token,
// or their own API key in the header the server names (ADR 0023). When the
// server answers 401 it refreshes once and retries; a key cannot be
// refreshed, and the request fails with grants.ErrKeyRefused. It sits inside
// the origin lock, so the credential only reaches the server's own origin.
type bearer struct {
	header  header
	token   func(context.Context) (string, error)
	refresh func(ctx context.Context, rejected string) (string, error)
	// key says the credential is a person's own key: a server may answer 403
	// to a key it does not know, as API gateways do, so 403 is a refusal of
	// the key too (review of 2026-11-15, U-04).
	key bool
}

// header is where a credential goes: the header's name and what comes
// before the credential in it.
type header struct{ name, prefix string }

// credentialHeader is the header a server's per-person credential goes in:
// the one the admin named for an API key, else Authorization: Bearer.
func credentialHeader(srv store.Server) header {
	if srv.Config.Auth == store.AuthKey && srv.Config.Key != nil && srv.Config.Key.Header != "" {
		return header{name: srv.Config.Key.Header, prefix: srv.Config.Key.Prefix}
	}
	return header{name: "Authorization", prefix: "Bearer "}
}

type bearerRT struct {
	next http.RoundTripper
	auth *bearer
}

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	tok, err := b.auth.token(r.Context())
	if err != nil {
		return nil, err
	}
	resp, err := b.next.RoundTrip(b.with(r, tok))
	if err == nil && b.auth.key && resp.StatusCode == http.StatusForbidden {
		resp.StatusCode = http.StatusUnauthorized // a key is refused the same way either answer
	}
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	// The 401 goes back to the caller unless one refresh gives a new token and
	// the request can be sent again.
	if code, _ := upstreamauth.ParseBearerError(resp.Header); code != "" && code != "invalid_token" {
		return resp, nil
	}
	retry, fresh, rerr := b.retryable(r, tok)
	if rerr != nil {
		if errors.Is(rerr, grants.ErrKeyRefused) {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			return nil, rerr
		}
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	return b.next.RoundTrip(b.with(retry, fresh))
}

// errNoRetry: the request is not sent again (no new credential, or a body
// that cannot be replayed); the 401 goes back to the caller.
var errNoRetry = errors.New("upstream: no retry")

// retryable refreshes after a rejected credential and prepares the request
// again. It returns the refresh's own error when that is grants.ErrKeyRefused.
func (b bearerRT) retryable(r *http.Request, rejected string) (*http.Request, string, error) {
	if r.Body != nil && r.Body != http.NoBody && r.GetBody == nil {
		return nil, "", errNoRetry
	}
	fresh, ferr := b.auth.refresh(r.Context(), rejected)
	if errors.Is(ferr, grants.ErrKeyRefused) {
		return nil, "", ferr
	}
	if ferr != nil || fresh == rejected {
		return nil, "", errNoRetry
	}
	retry := r.Clone(r.Context())
	if r.GetBody != nil {
		body, berr := r.GetBody()
		if berr != nil {
			return nil, "", errNoRetry
		}
		retry.Body = body
	}
	return retry, fresh, nil
}

func (b bearerRT) with(r *http.Request, tok string) *http.Request {
	r2 := r.Clone(r.Context())
	r2.Body = r.Body
	r2.Header.Set(b.auth.header.name, b.auth.header.prefix+tok)
	return r2
}
