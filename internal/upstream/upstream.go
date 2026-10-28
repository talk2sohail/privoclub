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
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/buildinfo"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/runner"
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
}

// Manager supervises upstream connections for every org.
type Manager struct {
	opts Options

	syncMu  sync.Mutex // serialises Sync so a stale snapshot never wins
	mu      sync.Mutex
	entries map[string]*entry // by server id
}

type entry struct {
	org     store.OrgID
	srv     store.Server
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	state   ServerState
	session *mcp.ClientSession

	loading atomic.Bool // a refresh is running
	dirty   atomic.Bool // another refresh was requested meanwhile
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
	if o.Logger == nil {
		o.Logger = slog.Default()
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
		e := &entry{org: org, srv: s, done: make(chan struct{})}
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
		session, err := m.connect(ctx, e)
		if err == nil {
			attempt = 0
			_ = session.Wait() // returns when the connection ends
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
		}
	}
}

func (e *entry) setDown(msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state = ServerState{Status: StatusDown, Error: msg}
	e.session = nil
}

func (m *Manager) connect(ctx context.Context, e *entry) (*mcp.ClientSession, error) {
	env, headers, err := m.secrets(ctx, e)
	if err != nil {
		return nil, err
	}
	transport, err := m.transport(ctx, e.srv, env, headers)
	if err != nil {
		return nil, err
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
	cctx, cancel := context.WithTimeout(ctx, m.opts.ConnectTimeout)
	defer cancel()
	session, err := client.Connect(cctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := m.load(cctx, e, session); err != nil {
		_ = session.Close()
		return nil, err
	}
	live.Store(session)
	return session, nil
}

// scheduleLoad coalesces list-changed notifications: at most one refresh runs
// per server, and a burst of notifications costs at most one extra refresh.
func (m *Manager) scheduleLoad(ctx context.Context, e *entry, s *mcp.ClientSession) {
	e.dirty.Store(true)
	if !e.loading.CompareAndSwap(false, true) {
		return
	}
	go func() {
		for {
			for e.dirty.Swap(false) {
				if err := m.load(ctx, e, s); err != nil {
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

// load lists everything the server advertises, records it for pin review and
// publishes it.
func (m *Manager) load(ctx context.Context, e *entry, session *mcp.ClientSession) error {
	caps := &mcp.ServerCapabilities{}
	if r := session.InitializeResult(); r != nil && r.Capabilities != nil {
		caps = r.Capabilities
	}
	var items []Item
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
		sum := sha256.Sum256(b)
		it := Item{ServerID: e.srv.ID, Alias: e.srv.Alias, Kind: kind, Name: name, Hash: hex.EncodeToString(sum[:])}
		set(&it)
		items = append(items, it)
		return nil
	}
	if caps.Tools != nil {
		for t, err := range session.Tools(ctx, nil) {
			if err != nil {
				return fmt.Errorf("tools/list: %w", err)
			}
			if t == nil { // the SDK client drops these itself today; do not depend on that
				return errNullEntry("tools/list")
			}
			if err := add(KindTool, t.Name, t, func(i *Item) { i.Tool = t }); err != nil {
				return err
			}
		}
	}
	if caps.Prompts != nil {
		for p, err := range session.Prompts(ctx, nil) {
			if err != nil {
				return fmt.Errorf("prompts/list: %w", err)
			}
			if p == nil {
				return errNullEntry("prompts/list")
			}
			if err := add(KindPrompt, p.Name, p, func(i *Item) { i.Prompt = p }); err != nil {
				return err
			}
		}
	}
	if caps.Resources != nil {
		for r, err := range session.Resources(ctx, nil) {
			if err != nil {
				return fmt.Errorf("resources/list: %w", err)
			}
			if r == nil {
				return errNullEntry("resources/list")
			}
			if err := add(KindResource, r.URI, r, func(i *Item) { i.Resource = r }); err != nil {
				return err
			}
		}
	}
	pins := make([]store.Pin, 0, len(items))
	for _, it := range items {
		def, _ := json.Marshal(firstNonNil(it))
		pins = append(pins, store.Pin{ServerID: it.ServerID, Kind: it.Kind, Name: it.Name, Hash: it.Hash, Definition: string(def)})
	}
	if err := m.opts.Store.RecordSeen(ctx, e.org, pins); err != nil {
		return fmt.Errorf("recording definitions: %w", err)
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
		client, err := originLocked(m.opts.HTTPClient, srv.Config.URL, headers)
		if err != nil {
			return nil, err
		}
		if srv.Transport == "sse" {
			return &mcp.SSEClientTransport{Endpoint: srv.Config.URL, HTTPClient: client}, nil
		}
		return &mcp.StreamableClientTransport{Endpoint: srv.Config.URL, HTTPClient: client, DisableStandaloneSSE: true, MaxRetries: -1}, nil
	}
	return nil, fmt.Errorf("unknown transport %q", srv.Transport)
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

func redactError(msg, rawURL string) string {
	if rawURL == "" {
		return msg
	}
	return strings.ReplaceAll(msg, rawURL, RedactURL(rawURL))
}
