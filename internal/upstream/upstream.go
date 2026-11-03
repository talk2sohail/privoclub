// Package upstream keeps connections to registered MCP servers (ADR 0001, 0004).
//
// Each enabled server has a supervisor goroutine that connects, loads its
// tools, prompts and resources (only those the server advertises), records
// what it saw for pin review, and reconnects with capped exponential backoff
// when the connection drops.
package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/buildinfo"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/runner"
	"privo.club/privo/internal/safe"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// Status of a server connection.
const (
	StatusConnecting = "connecting"
	StatusUp         = "up"
	StatusDown       = "down"
	StatusDisabled   = "disabled"
)

// Item kinds.
const (
	KindTool     = "tool"
	KindPrompt   = "prompt"
	KindResource = "resource"
)

// Item is one tool, prompt or resource offered by an upstream server.
type Item struct {
	ServerID string
	Alias    string
	Kind     string
	Name     string // upstream name (for resources: the URI)
	Hash     string
	Tool     *mcp.Tool
	Prompt   *mcp.Prompt
	Resource *mcp.Resource
}

// ServerState is a snapshot of one server's connection.
type ServerState struct {
	Status string
	Error  string
	Items  []Item
}

// Store is the store subset the manager uses.
type Store interface {
	ListServers(ctx context.Context, org store.OrgID) ([]store.Server, error)
	ServerCredentials(ctx context.Context, org store.OrgID, serverID string) ([]store.Credential, error)
	RecordSeen(ctx context.Context, org store.OrgID, pins []store.Pin) error
	PrunePending(ctx context.Context, org store.OrgID, serverID string, keep []store.PinKey) (int, error)
}

// Options configure a Manager.
type Options struct {
	Store          Store
	Keys           *keyring.Keyring
	Runner         runner.Runner // nil: stdio servers are refused
	HTTPClient     *http.Client  // the egress client
	RetryBase      time.Duration
	RetryMax       time.Duration
	ConnectTimeout time.Duration
	Logger         *slog.Logger

	// Bounds on what an upstream can make the gateway hold or do (R-04). Zero
	// means the default.
	MaxResponseBytes int64         // largest JSON reply of an HTTP or SSE upstream: default 16 MiB
	MaxLoadBytes     int           // definitions of one load together: default 8 MiB
	MaxPages         int           // pages of one list: default 100
	RefreshGap       time.Duration // least time between two refreshes of one server: default 5 s

	// Grants hands out upstream OAuth tokens (ADR 0017); nil refuses oauth servers.
	Grants   GrantSource
	PoolIdle time.Duration // a per-person session closes after this long unused: default 5 min
	PoolCap  int           // per-person sessions kept at most: default 500
}

// Manager supervises upstream connections for every org.
type Manager struct {
	opts Options

	syncMu  sync.Mutex // serialises Sync so a stale snapshot never wins
	mu      sync.Mutex
	entries map[string]*entry // by server id

	poolMu     sync.Mutex
	pool       map[poolKey]*pooled
	connecting map[poolKey]*pending
	reaperStop chan struct{}
}

type entry struct {
	org     store.OrgID
	srv     store.Server
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	state   ServerState
	session *mcp.ClientSession
	kick    chan struct{} // ends the wait before the next connection attempt

	loading  atomic.Bool  // a refresh is running
	dirty    atomic.Bool  // another refresh was requested meanwhile
	lastLoad atomic.Int64 // when the last load ended, unix nanoseconds
}

// New returns a manager.
func New(o Options) *Manager {
	if o.RetryBase <= 0 {
		o.RetryBase = 2 * time.Second
	}
	if o.RetryMax <= 0 {
		o.RetryMax = 30 * time.Second
	}
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = 20 * time.Second
	}
	if o.MaxResponseBytes <= 0 {
		o.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if o.MaxLoadBytes <= 0 {
		o.MaxLoadBytes = DefaultMaxLoadBytes
	}
	if o.MaxPages <= 0 {
		o.MaxPages = DefaultMaxPages
	}
	if o.RefreshGap <= 0 {
		o.RefreshGap = DefaultRefreshGap
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.PoolIdle <= 0 {
		o.PoolIdle = DefaultPoolIdle
	}
	if o.PoolCap <= 0 {
		o.PoolCap = DefaultPoolCap
	}
	return &Manager{opts: o, entries: map[string]*entry{}}
}

// Sync reconciles running connections with the stored servers of org.
// Calls are serialised, so a snapshot read before a change can never undo it.
func (m *Manager) Sync(ctx context.Context, org store.OrgID) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	servers, err := m.opts.Store.ListServers(ctx, org)
	if err != nil {
		return err
	}
	want := map[string]store.Server{}
	for _, s := range servers {
		want[s.ID] = s
	}

	m.mu.Lock()
	var stop []*entry
	for id, e := range m.entries {
		if e.org != org {
			continue
		}
		s, ok := want[id]
		if !ok || s.UpdatedAt.After(e.srv.UpdatedAt) {
			stop = append(stop, e)
			delete(m.entries, id)
		}
	}
	for id, s := range want {
		if _, ok := m.entries[id]; ok {
			continue
		}
		e := &entry{org: org, srv: s, done: make(chan struct{}), kick: make(chan struct{}, 1)}
		m.entries[id] = e
		if !s.Enabled {
			e.state.Status = StatusDisabled
			close(e.done)
			continue
		}
		e.state.Status = StatusConnecting
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		e.cancel = cancel // set under the lock: stop() must never see a nil cancel
		go m.supervise(runCtx, e)
	}
	m.mu.Unlock()

	for _, e := range stop {
		e.stop()
		m.ClosePerson(e.srv.ID, "")
	}
	return nil
}

// Close stops every connection.
func (m *Manager) Close() {
	m.mu.Lock()
	all := make([]*entry, 0, len(m.entries))
	for _, e := range m.entries {
		all = append(all, e)
	}
	m.entries = map[string]*entry{}
	m.mu.Unlock()
	for _, e := range all {
		e.stop()
	}
	m.closePool()
}

func (e *entry) stop() {
	if e.cancel != nil {
		e.cancel()
	}
	e.mu.Lock()
	if e.session != nil {
		_ = e.session.Close()
	}
	e.mu.Unlock()
	<-e.done
}

// State returns a snapshot of a server's connection, by server id.
func (m *Manager) State(serverID string) (ServerState, bool) {
	m.mu.Lock()
	e, ok := m.entries[serverID]
	m.mu.Unlock()
	if !ok {
		return ServerState{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.state
	st.Items = append([]Item(nil), e.state.Items...)
	return st, true
}

// Items returns every item of org's connected servers.
func (m *Manager) Items(org store.OrgID) []Item {
	m.mu.Lock()
	var es []*entry
	for _, e := range m.entries {
		if e.org == org {
			es = append(es, e)
		}
	}
	m.mu.Unlock()
	var out []Item
	for _, e := range es {
		e.mu.Lock()
		if e.state.Status == StatusUp {
			out = append(out, e.state.Items...)
		}
		e.mu.Unlock()
	}
	return out
}

// WaitSettled waits until a server's first connection attempt has finished
// (up, down or disabled) or the timeout passes.
func (m *Manager) WaitSettled(ctx context.Context, serverID string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st, ok := m.State(serverID); !ok || st.Status != StatusConnecting {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// ErrUnavailable means the server is not connected.
var ErrUnavailable = errors.New("upstream: server unavailable")

// Session returns the live session of a server in org.
func (m *Manager) Session(org store.OrgID, serverID string) (*mcp.ClientSession, error) {
	m.mu.Lock()
	e, ok := m.entries[serverID]
	m.mu.Unlock()
	if !ok || e.org != org {
		return nil, ErrUnavailable
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Status != StatusUp || e.session == nil {
		return nil, ErrUnavailable
	}
	return e.session, nil
}

func (m *Manager) supervise(ctx context.Context, e *entry) {
	defer close(e.done)
	attempt := 0
	for {
		session, release, err := m.connect(ctx, e)
		if err == nil {
			attempt = 0
			_ = session.Wait() // returns when the connection ends
			release()
			if ctx.Err() != nil {
				return
			}
			e.setDown("connection closed")
			m.opts.Logger.Warn("upstream disconnected", "server", e.srv.Alias)
		} else {
			if ctx.Err() != nil {
				return
			}
			msg := redactError(err.Error(), e.srv.Config.URL)
			e.setDown(msg)
			m.opts.Logger.Warn("upstream connect failed", "server", e.srv.Alias, "err", msg)
		}
		delay := m.opts.RetryBase << min(attempt, 10)
		if delay > m.opts.RetryMax || delay <= 0 {
			delay = m.opts.RetryMax
		}
		attempt++
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		case <-e.kick:
			attempt = 0
		}
	}
}

func (e *entry) setDown(msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state = ServerState{Status: StatusDown, Error: msg}
	e.session = nil
}

// connect opens the connection and loads what the server offers. The returned
// release function ends the context of the connection; call it when the
// connection has ended.
func (m *Manager) connect(ctx context.Context, e *entry) (*mcp.ClientSession, context.CancelFunc, error) {
	env, headers, err := m.secrets(ctx, e)
	if err != nil {
		return nil, nil, err
	}
	var transport mcp.Transport
	if e.srv.Config.Auth == store.AuthOAuth {
		transport, err = m.catalogTransport(ctx, e)
	} else {
		transport, err = m.transport(ctx, e.srv, env, headers)
	}
	if err != nil {
		return nil, nil, err
	}
	var live atomic.Pointer[mcp.ClientSession]
	refresh := func(context.Context) {
		if s := live.Load(); s != nil {
			m.scheduleLoad(ctx, e, s)
		}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "privo", Version: buildinfo.Version}, &mcp.ClientOptions{
		ToolListChangedHandler:     func(c context.Context, _ *mcp.ToolListChangedRequest) { refresh(c) },
		PromptListChangedHandler:   func(c context.Context, _ *mcp.PromptListChangedRequest) { refresh(c) },
		ResourceListChangedHandler: func(c context.Context, _ *mcp.ResourceListChangedRequest) { refresh(c) },
	})
	// The SDK recovers from no panic in the goroutines that handle what the
	// upstream sends, and one message without "params" was enough to end the
	// process for every tenant (R-01).
	client.AddReceivingMiddleware(safe.Recover(m.opts.Logger, "upstream "+e.srv.Alias))
	// ConnectTimeout bounds the handshake and the first load, but the context
	// given to Connect has to live as long as the connection: the legacy SSE
	// transport ties its event stream to it, and with a context that ended when
	// connect returned an SSE server was dropped at once and reconnected, over
	// and over. So a timer cancels it, and is stopped once the connection is up.
	cctx, release := context.WithCancel(ctx)
	timer := time.AfterFunc(m.opts.ConnectTimeout, release)
	fail := func(err error) (*mcp.ClientSession, context.CancelFunc, error) {
		timer.Stop()
		release()
		if ctx.Err() == nil && errors.Is(err, context.Canceled) {
			err = fmt.Errorf("%w (no answer within the connect timeout of %s)", err, m.opts.ConnectTimeout)
		}
		return nil, nil, err
	}
	session, err := client.Connect(cctx, transport, nil)
	if err != nil {
		return fail(fmt.Errorf("connect: %w", err))
	}
	if err := m.load(cctx, e, session); err != nil {
		_ = session.Close()
		return fail(err)
	}
	if !timer.Stop() { // the timeout fired just now: the connection is ending
		_ = session.Close()
		return fail(context.Canceled)
	}
	live.Store(session)
	return session, release, nil
}

// scheduleLoad coalesces list-changed notifications: at most one refresh runs
// per server, a burst of notifications costs at most one extra refresh, and a
// server gets one refresh per RefreshGap at most however often it announces a
// change. Every refresh lists everything and writes to the store, and an
// upstream is untrusted (R-04). A refresh ends at the latest after the connect
// timeout; one that hung used to keep the slot for good, and no later change of
// the server's list was ever read.
func (m *Manager) scheduleLoad(ctx context.Context, e *entry, s *mcp.ClientSession) {
	e.dirty.Store(true)
	if !e.loading.CompareAndSwap(false, true) {
		return
	}
	go func() {
		for {
			for e.dirty.Load() {
				if wait := m.opts.RefreshGap - time.Since(time.Unix(0, e.lastLoad.Load())); wait > 0 {
					t := time.NewTimer(wait)
					select {
					case <-ctx.Done():
						t.Stop()
						e.loading.Store(false)
						return
					case <-t.C:
					}
				}
				e.dirty.Store(false) // a change announced from here on needs one more refresh
				lctx, cancel := context.WithTimeout(ctx, m.opts.ConnectTimeout)
				err := m.load(lctx, e, s)
				cancel()
				if err != nil {
					m.opts.Logger.Warn("upstream refresh failed", "server", e.srv.Alias, "err", redactError(err.Error(), e.srv.Config.URL))
				}
			}
			e.loading.Store(false)
			if !e.dirty.Load() || !e.loading.CompareAndSwap(false, true) {
				return
			}
		}
	}()
}

// Limits on what an upstream can make Privo store.
const (
	MaxItems           = 2000
	MaxDefinitionBytes = 64 << 10
)

// Defaults of the limits in Options (R-04).
const (
	DefaultMaxResponseBytes = 16 << 20
	DefaultMaxLoadBytes     = 8 << 20
	DefaultMaxPages         = 100
	DefaultRefreshGap       = 5 * time.Second
)

// walk reads a list page by page. The SDK's own iterator follows the cursors a
// server sends without any limit, so a server that always had a next page kept
// the load running until the connect timeout (R-04). walk stops after maxPages
// pages, and when a cursor comes back that it has already used. Errors of
// fetch carry the method; errors of each are the caller's own.
func walk[T any](method string, maxPages int, fetch func(cursor string) (items []T, next string, err error), each func(T) error) error {
	seen := map[[sha256.Size]byte]struct{}{} // hashes: a cursor is up to the server
	cursor := ""
	for n := 1; ; n++ {
		items, next, err := fetch(cursor)
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		for _, it := range items {
			if err := each(it); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		if n >= maxPages {
			return fmt.Errorf("%s: the list has more than %d pages", method, maxPages)
		}
		h := sha256.Sum256([]byte(next))
		if _, dup := seen[h]; dup {
			return fmt.Errorf("%s: the server sent the same page cursor twice", method)
		}
		seen[h] = struct{}{}
		cursor = next
	}
}

var errEmptyReply = errors.New("empty reply")

// load lists everything the server advertises, records it for pin review and
// publishes it.
func (m *Manager) load(ctx context.Context, e *entry, session *mcp.ClientSession) error {
	defer func() { e.lastLoad.Store(time.Now().UnixNano()) }()
	caps := &mcp.ServerCapabilities{}
	if r := session.InitializeResult(); r != nil && r.Capabilities != nil {
		caps = r.Capabilities
	}
	var items []Item
	budget := 0
	add := func(kind, name string, def any, set func(*Item)) error {
		b, err := json.Marshal(def)
		if err != nil {
			return err
		}
		if len(b) > MaxDefinitionBytes {
			return fmt.Errorf("%s %q definition is %d bytes; the limit is %d", kind, name, len(b), MaxDefinitionBytes)
		}
		if len(items) >= MaxItems {
			return fmt.Errorf("server offers more than %d items", MaxItems)
		}
		if budget += len(b); budget > m.opts.MaxLoadBytes {
			return fmt.Errorf("the definitions of this server pass %d bytes together", m.opts.MaxLoadBytes)
		}
		sum := sha256.Sum256(b)
		it := Item{ServerID: e.srv.ID, Alias: e.srv.Alias, Kind: kind, Name: name, Hash: hex.EncodeToString(sum[:])}
		set(&it)
		items = append(items, it)
		return nil
	}
	if caps.Tools != nil {
		err := walk("tools/list", m.opts.MaxPages, func(cursor string) ([]*mcp.Tool, string, error) {
			r, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			if r == nil {
				return nil, "", errEmptyReply
			}
			return r.Tools, r.NextCursor, nil
		}, func(t *mcp.Tool) error {
			if t == nil { // the SDK client drops these itself today; do not depend on that
				return errNullEntry("tools/list")
			}
			return add(KindTool, t.Name, t, func(i *Item) { i.Tool = t })
		})
		if err != nil {
			return err
		}
	}
	if caps.Prompts != nil {
		err := walk("prompts/list", m.opts.MaxPages, func(cursor string) ([]*mcp.Prompt, string, error) {
			r, err := session.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			if r == nil {
				return nil, "", errEmptyReply
			}
			return r.Prompts, r.NextCursor, nil
		}, func(p *mcp.Prompt) error {
			if p == nil {
				return errNullEntry("prompts/list")
			}
			return add(KindPrompt, p.Name, p, func(i *Item) { i.Prompt = p })
		})
		if err != nil {
			return err
		}
	}
	if caps.Resources != nil {
		err := walk("resources/list", m.opts.MaxPages, func(cursor string) ([]*mcp.Resource, string, error) {
			r, err := session.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
			if err != nil {
				return nil, "", err
			}
			if r == nil {
				return nil, "", errEmptyReply
			}
			return r.Resources, r.NextCursor, nil
		}, func(r *mcp.Resource) error {
			if r == nil {
				return errNullEntry("resources/list")
			}
			return add(KindResource, r.URI, r, func(i *Item) { i.Resource = r })
		})
		if err != nil {
			return err
		}
	}
	pins := make([]store.Pin, 0, len(items))
	keep := make([]store.PinKey, 0, len(items))
	for _, it := range items {
		def, _ := json.Marshal(firstNonNil(it))
		pins = append(pins, store.Pin{ServerID: it.ServerID, Kind: it.Kind, Name: it.Name, Hash: it.Hash, Definition: string(def)})
		keep = append(keep, store.PinKey{Kind: it.Kind, Name: it.Name, Hash: it.Hash})
	}
	if err := m.opts.Store.RecordSeen(ctx, e.org, pins); err != nil {
		return fmt.Errorf("recording definitions: %w", err)
	}
	// Definitions that the server offered once and no longer offers are not
	// kept for review. Only here, after a load that listed everything: a
	// partial list must not delete anything. This is housekeeping, so a failure
	// does not put the server down.
	if _, err := m.opts.Store.PrunePending(ctx, e.org, e.srv.ID, keep); err != nil {
		m.opts.Logger.Warn("upstream: pruning unreviewed definitions failed", "server", e.srv.Alias, "err", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != nil && e.session != session {
		return nil // a newer connection replaced this one; drop stale results
	}
	e.state = ServerState{Status: StatusUp, Items: items}
	e.session = session
	return nil
}

// errNullEntry reports a list that has a JSON null among its items. The upstream
// is untrusted input and one process serves every tenant, so this puts that
// server down with a reason; dereferencing the null would end the process.
func errNullEntry(method string) error {
	return fmt.Errorf("%s: server returned a null entry", method)
}

func firstNonNil(it Item) any {
	switch {
	case it.Tool != nil:
		return it.Tool
	case it.Prompt != nil:
		return it.Prompt
	default:
		return it.Resource
	}
}

// secrets decrypts a server's credentials into environment variables and headers.
func (m *Manager) secrets(ctx context.Context, e *entry) (map[string]string, map[string]string, error) {
	creds, err := m.opts.Store.ServerCredentials(ctx, e.org, e.srv.ID)
	if err != nil {
		return nil, nil, err
	}
	env, headers := map[string]string{}, map[string]string{}
	for _, c := range creds {
		// Only static secrets: people's grants and client secrets are not
		// decrypted here (ADR 0017).
		if !strings.HasPrefix(c.Purpose, "env:") && !strings.HasPrefix(c.Purpose, "header:") {
			continue
		}
		dek, err := m.opts.Keys.DEK(ctx, e.org, c.KeyVersion)
		if err != nil {
			return nil, nil, err
		}
		pt, err := vault.Open(dek, c.Ciphertext, vault.Binding{
			OrgID: string(e.org), CredentialID: c.ID, OwnerID: c.OwnerID, ServerID: e.srv.ID,
			Purpose: c.Purpose, KeyVersion: c.KeyVersion, RowVersion: c.RowVersion,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("credential %s: %w", c.Purpose, err)
		}
		kind, name, _ := strings.Cut(c.Purpose, ":")
		switch kind {
		case "env":
			env[name] = string(pt)
		case "header":
			headers[name] = string(pt)
		}
	}
	return env, headers, nil
}

func (m *Manager) transport(ctx context.Context, srv store.Server, env, headers map[string]string) (mcp.Transport, error) {
	switch srv.Transport {
	case "stdio":
		if m.opts.Runner == nil {
			return nil, errors.New("no runner configured for stdio servers")
		}
		cmd, err := m.opts.Runner.Command(ctx, runner.Spec{Path: srv.Config.Path, SHA256: srv.Config.SHA256, Args: srv.Config.Args, Env: env})
		if err != nil {
			return nil, err
		}
		return &mcp.CommandTransport{Command: cmd}, nil
	case "http", "sse":
		return m.httpTransport(srv, headers, nil)
	}
	return nil, fmt.Errorf("unknown transport %q", srv.Transport)
}

// httpTransport is the transport of a remote server: origin-locked, bounded,
// and with a bearer token from auth when it is set.
func (m *Manager) httpTransport(srv store.Server, headers map[string]string, auth *bearer) (mcp.Transport, error) {
	if srv.Transport != "http" && srv.Transport != "sse" {
		return nil, fmt.Errorf("transport %q cannot use OAuth", srv.Transport)
	}
	base := m.opts.HTTPClient
	if base == nil {
		base = http.DefaultClient
	}
	if auth != nil {
		c := *base
		next := c.Transport
		if next == nil {
			next = http.DefaultTransport
		}
		c.Transport = bearerRT{next: next, auth: auth}
		base = &c
	}
	client, err := originLocked(base, srv.Config.URL, headers)
	if err != nil {
		return nil, err
	}
	client = limitBodies(client, m.opts.MaxResponseBytes)
	if srv.Transport == "sse" {
		return &mcp.SSEClientTransport{Endpoint: srv.Config.URL, HTTPClient: client}, nil
	}
	return &mcp.StreamableClientTransport{Endpoint: srv.Config.URL, HTTPClient: client, DisableStandaloneSSE: true, MaxRetries: -1}, nil
}

// catalogTransport is the transport an oauth server's catalog is loaded with:
// the catalog grant's token, refreshed as needed.
func (m *Manager) catalogTransport(ctx context.Context, e *entry) (mcp.Transport, error) {
	if m.opts.Grants == nil {
		return nil, errors.New("upstream OAuth is not available in this process")
	}
	if _, _, err := m.opts.Grants.CatalogAccessToken(ctx, e.org, e.srv); err != nil {
		return nil, fmt.Errorf("the tool list needs an administrator's connected account (/connect/%s on the admin site): %w", e.srv.Alias, err)
	}
	var owner atomic.Value
	org, srv := e.org, e.srv
	auth := &bearer{
		token: func(c context.Context) (string, error) {
			uid, tok, err := m.opts.Grants.CatalogAccessToken(c, org, srv)
			if err == nil {
				owner.Store(uid)
			}
			return tok, err
		},
		refresh: func(c context.Context, old string) (string, error) {
			uid, _ := owner.Load().(string)
			if uid == "" {
				return "", errors.New("no catalog grant")
			}
			return m.opts.Grants.RefreshAfterReject(c, org, srv, uid, old)
		},
	}
	return m.httpTransport(e.srv, nil, auth)
}

// originLocked returns a client that only talks to the registered server's
// origin (scheme and host) and adds its secret headers there. Requests to any
// other origin are refused: this stops a redirect or an SSE "endpoint" event
// from sending credentials or tool arguments elsewhere.
func originLocked(base *http.Client, rawURL string, headers map[string]string) (*http.Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if base == nil {
		base = http.DefaultClient
	}
	c := *base
	next := c.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	c.Transport = originRT{next: next, headers: headers, origin: origin(u)}
	return &c, nil
}

func origin(u *url.URL) string { return strings.ToLower(u.Scheme + "://" + u.Host) }

type originRT struct {
	next    http.RoundTripper
	headers map[string]string
	origin  string
}

// ErrOtherOrigin is returned for requests outside the registered origin.
var ErrOtherOrigin = errors.New("upstream: request to a different origin than the registered server refused")

func (o originRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if origin(r.URL) != o.origin {
		return nil, ErrOtherOrigin
	}
	if len(o.headers) == 0 {
		return o.next.RoundTrip(r)
	}
	r = r.Clone(r.Context())
	for k, v := range o.headers {
		r.Header.Set(k, v)
	}
	return o.next.RoundTrip(r)
}

// RedactURL keeps scheme, host and path and drops anything else (query
// strings often carry API keys).
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid URL)"
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

var urlInText = regexp.MustCompile(`https?://[^\s"'<>]+`)

// RedactText removes the credentials from every http(s) URL in free text such
// as an error message: user info, query and fragment. URLs often carry the API
// key in the query. It works without knowing which URL to look for, so it also
// covers redirects and error texts that quote a URL the caller never saw.
func RedactText(s string) string {
	return urlInText.ReplaceAllStringFunc(s, func(m string) string {
		u, err := url.Parse(m)
		if err != nil || u.Host == "" {
			return "(redacted URL)"
		}
		return u.Scheme + "://" + u.Host + u.EscapedPath()
	})
}

// ErrResponseTooLarge is returned when a reply of an upstream passes the limit.
var ErrResponseTooLarge = errors.New("upstream: reply is larger than the limit")

// limitBodies returns a client whose replies fail with ErrResponseTooLarge once
// they pass limit bytes. The SDK reads a JSON reply with io.ReadAll, so without
// this an upstream decided how much memory one reply took (R-04). A JSON body
// is counted as the SDK reads it, after decompression, and a reply that
// announces more than limit is refused before its body is read. An event stream
// lasts as long as the call does, so it is counted per event (an event ends at
// an empty line); the SDK's own cap is 16 MiB per event. limit <= 0 turns the
// limit off.
func limitBodies(c *http.Client, limit int64) *http.Client {
	if limit <= 0 {
		return c
	}
	cc := *c
	next := cc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	cc.Transport = limitRT{next: next, max: limit}
	return &cc
}

type limitRT struct {
	next http.RoundTripper
	max  int64
}

func (l limitRT) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := l.next.RoundTrip(r)
	if err != nil || resp == nil {
		return resp, err
	}
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		resp.Body = &eventCapBody{rc: resp.Body, max: l.max, atLineStart: true}
		return resp, nil
	}
	if resp.ContentLength > l.max {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: it announces %d bytes, the limit is %d", ErrResponseTooLarge, resp.ContentLength, l.max)
	}
	resp.Body = &capBody{rc: resp.Body, left: l.max}
	return resp, nil
}

// capBody passes on at most left more bytes, then fails.
type capBody struct {
	rc   io.ReadCloser
	left int64 // negative: the limit was passed
}

func (b *capBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, ErrResponseTooLarge
	}
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1] // one byte more than allowed is enough to know
	}
	n, err := b.rc.Read(p)
	if int64(n) > b.left {
		n = int(b.left)
		b.left = -1
		return n, ErrResponseTooLarge
	}
	b.left -= int64(n)
	return n, err
}

func (b *capBody) Close() error { return b.rc.Close() }

// eventCapBody fails once a single server-sent event passes max bytes. An event
// ends at an empty line, and a line ends with \n, \r\n or \r. The bytes before
// the first one over the limit are passed on.
type eventCapBody struct {
	rc          io.ReadCloser
	max         int64
	run         int64 // bytes of the current event so far
	prev        byte
	atLineStart bool
	over        bool
}

func (b *eventCapBody) Read(p []byte) (int, error) {
	if b.over {
		return 0, ErrResponseTooLarge
	}
	n, err := b.rc.Read(p)
	for i := 0; i < n; i++ {
		c := p[i]
		switch {
		case c == '\n' && b.prev == '\r':
			// the second half of \r\n; the line ended at the \r
		case c == '\r' || c == '\n':
			if b.atLineStart {
				b.run = 0 // an empty line: the event is complete
			} else {
				b.atLineStart = true
				b.run++
			}
		default:
			b.atLineStart = false
			b.run++
		}
		b.prev = c
		if b.run > b.max {
			b.over = true
			return i, ErrResponseTooLarge
		}
	}
	return n, err
}

func (b *eventCapBody) Close() error { return b.rc.Close() }

func redactError(msg, rawURL string) string {
	if rawURL == "" {
		return msg
	}
	return strings.ReplaceAll(msg, rawURL, RedactURL(rawURL))
}
