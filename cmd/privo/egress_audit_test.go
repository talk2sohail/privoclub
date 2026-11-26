package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/control"
	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/oauthserver"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
	"privo.club/privo/internal/upstream"
	"privo.club/privo/internal/upstreamauth"
	"privo.club/privo/internal/vault"
)

type egressTestEnv struct {
	st          *store.Store
	org         store.Org
	keys        vault.KeyProvider
	kr          *keyring.Keyring
	aw          *audit.Writer
	rec         *telemetry.Recorder
	logs        *syncBuffer
	logger      *slog.Logger
	egressRec   *egressRecorder
	policy      egress.Policy
	api         *control.API
	mux         *http.ServeMux
	ownerToken  string
	ownerP      auth.Principal
	mgr         *upstream.Manager
	oauthClient *upstreamauth.Client
	clients     *oauthserver.Clients
	now         time.Time
	nowMu       sync.Mutex
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newEgressTestEnv(t *testing.T) *egressTestEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, err := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
	if err != nil {
		t.Fatal(err)
	}
	kek, _ := vault.NewKey()
	keys := vault.StaticKeyProvider(kek, nil)
	kr := keyring.New(st, keys)
	if err := kr.EnsureOrgKey(ctx, org.ID); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	rec := telemetry.New(st, telemetry.Options{FlushInterval: 10 * time.Millisecond, Logger: logger})
	t.Cleanup(rec.Close)
	aw := audit.NewWriter(st, audit.Options{Logger: logger})
	t.Cleanup(aw.Close)

	env := &egressTestEnv{
		st:     st,
		org:    org,
		keys:   keys,
		kr:     kr,
		aw:     aw,
		rec:    rec,
		logs:   logs,
		logger: logger,
		now:    time.Now(),
	}
	clock := func() time.Time {
		env.nowMu.Lock()
		defer env.nowMu.Unlock()
		return env.now
	}
	env.egressRec = &egressRecorder{
		Audit:       aw,
		Diagnostics: rec,
		Logger:      logger,
		DefaultOrg:  org.ID,
		Now:         clock,
	}
	env.policy = egress.Policy{OnBlocked: env.egressRec.Record}

	env.oauthClient = &upstreamauth.Client{
		HTTP: egress.NewClient(egress.Options{
			Policy:       env.policy,
			SameHostOnly: true,
			MaxRedirects: 1,
			OnBlocked:    env.egressRec.Record,
		}),
		Timeout: time.Second,
	}
	env.mgr = upstream.New(upstream.Options{
		Store: st,
		Keys:  kr,
		HTTPClient: egress.NewClient(egress.Options{
			Policy:       env.policy,
			SameHostOnly: true,
			OnBlocked:    env.egressRec.Record,
		}),
		RetryBase:   time.Hour,
		Logger:      logger,
		Diagnostics: rec,
	})
	t.Cleanup(env.mgr.Close)

	authn := auth.New(st)
	env.api = &control.API{
		Store:     st,
		Keys:      kr,
		AuditKeys: keys,
		Auth:      authn,
		Upstream:  env.mgr,
		Audit:     aw,
		Approval:  &approval.Service{Store: st, Keys: kr},
		Egress:    env.policy,
		Logger:    logger,
		TokenTTL:  24 * time.Hour,
		OAuth:     env.oauthClient,
		Telemetry: rec,
	}
	env.mux = http.NewServeMux()
	env.api.Routes(env.mux)

	plain, tok, err := control.IssueToken(ctx, st, org.ID, control.TokenSpec{
		Name:   "boss",
		Role:   store.RoleOwner,
		Kind:   store.KindControl,
		Person: "boss",
		TTL:    time.Hour,
		Event:  store.AuditEvent{Actor: "cli", ActorID: "cli"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env.ownerToken = plain
	env.ownerP = auth.Principal{OrgID: org.ID, TokenID: tok.ID, Name: "boss", Roles: []string{store.RoleOwner}, Kind: store.KindControl}

	env.clients = &oauthserver.Clients{
		Org:    org.ID,
		Store:  st,
		Policy: oauthserver.PolicyAny,
		HTTP:   oauthserver.NewDocumentClient(egress.Policy{OnBlocked: env.egressRec.Record}),
		Now:    clock,
	}
	return env
}

func (e *egressTestEnv) apiCall(t *testing.T, method, path string, body any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+e.ownerToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec.Code
}

func (e *egressTestEnv) advance(d time.Duration) {
	e.nowMu.Lock()
	defer e.nowMu.Unlock()
	e.now = e.now.Add(d)
}

func TestBlockedEgressIsAuditedWithoutQueryOrSecrets(t *testing.T) {
	e := newEgressTestEnv(t)
	ctx := context.Background()

	queryAndAuthSecrets := []string{
		"USERSECRET", "PASSSECRET",
		"TOPSECRET-REG", "frag-reg", "api_key",
		"TOPSECRET-EDIT", "frag-edit",
		"TOPSECRET-UP", "frag-up",
		"TOPSECRET-OAUTH",
		"TOPSECRET-META", "client_secret",
	}
	pathSecrets := []string{
		"secret-reg-token",
		"secret-edit-token",
		"secret-up-token",
		"secret-oauth-token",
		"secret-meta-token",
	}

	// 1. Server registration (control.API.Egress)
	code := e.apiCall(t, http.MethodPost, "/api/v1/servers", map[string]any{
		"alias":     "badreg",
		"transport": "http",
		"url":       "http://127.0.0.1:9991/secret-reg-token?api_key=TOPSECRET-REG#frag-reg",
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("registration status = %d, want 422", code)
	}

	// 2. Server edit (control.API.Egress via PATCH)
	seed := store.Server{
		OrgID:     e.org.ID,
		ID:        "srv-edit",
		Alias:     "edittest",
		Transport: "http",
		Enabled:   false,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Config:    store.ServerConfig{URL: "https://example.com/mcp"},
	}
	if err := e.st.CreateServer(ctx, seed, nil); err != nil {
		t.Fatal(err)
	}
	code = e.apiCall(t, http.MethodPatch, "/api/v1/servers/edittest", map[string]any{
		"url": "https://USERSECRET:PASSSECRET@169.254.169.254/secret-edit-token?token=TOPSECRET-EDIT#frag-edit",
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("edit status = %d, want 422", code)
	}

	// 3. Upstream Manager client dial block
	upSrv := store.Server{
		OrgID:     e.org.ID,
		ID:        "srv-up",
		Alias:     "upssrf",
		Transport: "http",
		Enabled:   true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Config:    store.ServerConfig{URL: "https://10.0.0.5:8443/secret-up-token?api_key=TOPSECRET-UP#frag-up"},
	}
	if err := e.st.CreateServer(ctx, upSrv, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.Sync(ctx, e.org.ID); err != nil {
		t.Fatal(err)
	}
	e.mgr.WaitSettled(ctx, upSrv.ID, 5*time.Second)

	// 4. Upstream OAuth discovery dial block
	_, err := e.oauthClient.Discover(auth.WithPrincipal(ctx, e.ownerP), "https://192.168.1.10:8443/secret-oauth-token?code=TOPSECRET-OAUTH", "http", "")
	if !errors.Is(err, egress.ErrBlocked) {
		t.Fatalf("oauth Discover err = %v, want ErrBlocked", err)
	}

	// 5. OAuth client metadata fetch dial block (anonymous)
	if _, err := e.clients.Lookup(ctx, "https://172.16.0.5/secret-meta-token?client_secret=TOPSECRET-META"); err == nil {
		t.Fatal("blocked metadata fetch succeeded")
	}

	// Flush telemetry diagnostics and close writer/manager before checking stores.
	e.mgr.Close()
	e.aw.Close()
	e.rec.Close()

	// Verify audit chain integrity passes with all blocked egress events included.
	if _, err := (&audit.Sealer{Store: e.st, Keys: e.keys, Org: e.org.ID, Logger: e.logger}).SealOnce(ctx); err != nil {
		t.Fatalf("SealOnce: %v", err)
	}
	trusted, err := audit.KeysFrom(e.keys, e.org.ID)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := audit.Verify(ctx, e.st, e.org.ID, trusted)
	if err != nil || !rep.OK {
		t.Fatalf("audit.Verify: %+v, err=%v", rep, err)
	}

	// Verify RecentAdminEvents and SearchAudit(Kind: "admin") both find the events.
	recent, err := e.st.RecentAdminEvents(ctx, e.org.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	searched, _, err := e.st.SearchAudit(ctx, e.org.ID, store.AuditQuery{Kind: "admin", Action: "egress.blocked:"})
	if err != nil {
		t.Fatal(err)
	}

	type wantEv struct {
		actor  string
		server string
		action string
	}
	expected := []wantEv{
		{actor: "boss", server: "http://127.0.0.1:9991", action: "egress.blocked:loopback"},
		{actor: "boss", server: "https://169.254.169.254", action: "egress.blocked:credentials in URL"},
		{actor: "system", server: "10.0.0.5", action: "egress.blocked:private network"},
		{actor: "boss", server: "192.168.1.10", action: "egress.blocked:private network"},
		{actor: "system", server: "172.16.0.5", action: "egress.blocked:private network"},
	}
	if len(searched) != len(expected) {
		t.Fatalf("SearchAudit found %d egress.blocked events, want %d: %+v", len(searched), len(expected), searched)
	}
	for _, w := range expected {
		foundRecent := slices.ContainsFunc(recent, func(ev store.AuditEvent) bool {
			return ev.Actor == w.actor && ev.Server == w.server && ev.Action == w.action &&
				ev.Decision == store.ActionDeny && ev.Outcome == store.OutcomeRefused
		})
		if !foundRecent {
			t.Errorf("RecentAdminEvents missing %+v in %+v", w, recent)
		}
		foundSearch := slices.ContainsFunc(searched, func(r store.CallRecord) bool {
			ev := r.Intent
			return ev.Actor == w.actor && ev.Server == w.server && ev.Action == w.action &&
				ev.Decision == store.ActionDeny && ev.Outcome == store.OutcomeRefused
		})
		if !foundSearch {
			t.Errorf("SearchAudit missing %+v in %+v", w, searched)
		}
	}

	// Verify CategoryUpstream diagnostics were recorded.
	diags, err := e.st.Diagnostics(ctx, e.org.ID, store.DiagnosticQuery{Category: store.CategoryUpstream})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range expected {
		if !slices.ContainsFunc(diags, func(d store.Diagnostic) bool {
			return d.Level == store.LevelWarn && d.Category == store.CategoryUpstream &&
				strings.HasPrefix(d.Message, "egress blocked to "+w.server+": ")
		}) {
			t.Errorf("Diagnostics missing entry for %+v in %+v", w, diags)
		}
	}

	// Verify NONE of the query params, userinfo, or fragments leaked anywhere,
	// and blocked-egress audit/diagnostic/log lines never contain URL paths.
	logText := e.logs.String()
	for _, s := range queryAndAuthSecrets {
		if strings.Contains(logText, s) {
			t.Errorf("slog output leaked %q:\n%s", s, logText)
		}
		for _, ev := range recent {
			if strings.Contains(ev.Server, s) || strings.Contains(ev.Action, s) || strings.Contains(ev.Actor, s) {
				t.Errorf("audit event leaked %q: %+v", s, ev)
			}
		}
		for _, d := range diags {
			if strings.Contains(d.Server, s) || strings.Contains(d.Message, s) {
				t.Errorf("diagnostic leaked %q: %+v", s, d)
			}
		}
	}
	for _, s := range pathSecrets {
		for _, line := range strings.Split(logText, "\n") {
			if strings.Contains(line, "egress blocked") && strings.Contains(line, s) {
				t.Errorf("egress blocked log line leaked path %q: %s", s, line)
			}
		}
		for _, ev := range recent {
			if strings.Contains(ev.Server, s) || strings.Contains(ev.Action, s) {
				t.Errorf("audit event leaked path %q: %+v", s, ev)
			}
		}
		for _, d := range diags {
			if strings.HasPrefix(d.Message, "egress blocked") && (strings.Contains(d.Server, s) || strings.Contains(d.Message, s)) {
				t.Errorf("egress blocked diagnostic leaked path %q: %+v", s, d)
			}
		}
	}

	// Verify fail-closed behavior even if audit store fails.
	failAw := audit.NewWriter(failingAuditStore{}, audit.Options{Logger: e.logger})
	t.Cleanup(failAw.Close)
	failRec := &egressRecorder{Audit: failAw, Diagnostics: e.rec, Logger: e.logger, DefaultOrg: e.org.ID}
	failPolicy := egress.Policy{OnBlocked: failRec.Record}
	if _, err := failPolicy.CheckURL(ctx, "http://127.0.0.1/path?secret=x", nil); !errors.Is(err, egress.ErrBlocked) {
		t.Fatalf("CheckURL with failing audit returned %v, want ErrBlocked", err)
	}
}

type failingAuditStore struct{}

func (failingAuditStore) InsertAudit(context.Context, store.OrgID, ...store.AuditEvent) error {
	return errors.New("audit db down")
}

func TestAnonymousBlockedMetadataFetchIsRateLimitedInAudit(t *testing.T) {
	e := newEgressTestEnv(t)
	ctx := context.Background()

	// Repeated anonymous metadata fetches against the same blocked host with
	// different paths & query strings (bypassing Clients' per-URL doc cache)
	// must emit only 1 audit row within the 1-minute window.
	for i := 0; i < 20; i++ {
		u := fmt.Sprintf("https://127.0.0.1:9999/path-%d/client.json?cachebust=%d&secret=TOPSECRET-%d", i, i, i)
		if _, err := e.clients.Lookup(ctx, u); err == nil {
			t.Fatalf("attempt %d succeeded unexpectedly", i)
		}
	}

	// Advance past the 1-minute window; next blocked attempt is audited again.
	e.advance(61 * time.Second)
	if _, err := e.clients.Lookup(ctx, "https://127.0.0.1:9999/after-window/client.json?secret=TOPSECRET-AFTER"); err == nil {
		t.Fatal("attempt after window succeeded unexpectedly")
	}

	// Verify bounded map cap on egressRecorder: even with distinct targets, the
	// map never grows past MaxKeys and never panics or drops fail-closed behavior.
	bounded := &egressRecorder{
		Audit:       e.aw,
		Diagnostics: e.rec,
		Logger:      e.logger,
		DefaultOrg:  e.org.ID,
		Window:      time.Minute,
		MaxKeys:     3,
		Now:         e.egressRec.Now,
	}
	client := egress.NewClient(egress.Options{OnBlocked: bounded.Record})
	for i := 1; i <= 10; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.1.%d:8080/c.json?secret=S%d", i, i), nil)
		if _, err := client.Do(req); !errors.Is(err, egress.ErrBlocked) {
			t.Fatalf("target %d err = %v, want ErrBlocked", i, err)
		}
	}
	bounded.mu.Lock()
	mapLen := len(bounded.seen)
	bounded.mu.Unlock()
	if mapLen != 3 {
		t.Fatalf("bounded map size = %d, want 3", mapLen)
	}

	e.aw.Close()
	e.rec.Close()

	evs, _, err := e.st.SearchAudit(ctx, e.org.ID, store.AuditQuery{Kind: "admin", Action: "egress.blocked:"})
	if err != nil {
		t.Fatal(err)
	}
	loopbackCount := 0
	for _, r := range evs {
		if r.Intent.Server == "127.0.0.1" && r.Intent.Actor == "system" {
			loopbackCount++
		}
		if strings.Contains(r.Intent.Server, "TOPSECRET") || strings.Contains(r.Intent.Action, "TOPSECRET") {
			t.Fatalf("audit row leaked secret: %+v", r.Intent)
		}
	}
	if loopbackCount != 2 {
		t.Fatalf("anonymous blocked metadata audit count = %d, want 2 (1 before + 1 after 1m window)", loopbackCount)
	}
	if strings.Contains(e.logs.String(), "TOPSECRET") {
		t.Fatalf("logs leaked secret:\n%s", e.logs.String())
	}
}
