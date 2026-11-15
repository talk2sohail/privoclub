package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/buildinfo"
	"privo.club/privo/internal/config"
	"privo.club/privo/internal/control"
	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/gateway"
	"privo.club/privo/internal/githubauth"
	"privo.club/privo/internal/grants"
	"privo.club/privo/internal/health"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/oauthserver"
	"privo.club/privo/internal/runner"
	"privo.club/privo/internal/server"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstream"
	"privo.club/privo/internal/upstreamauth"
	"privo.club/privo/internal/vault"
	"privo.club/privo/internal/web"
	webapp "privo.club/privo/web"
)

// DefaultOrgSlug names the organisation a single-node install starts with.
const DefaultOrgSlug = "default"

func serve(stderr io.Writer, getenv func(string) string) int {
	cfg, err := config.Load(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: invalid configuration:\n%v\n", err)
		return 2
	}
	// A value that is not a token stops the process here. A file that cannot be
	// read waits for the store, which knows whether the token is still needed.
	var bs bootstrapInput
	bs.token, bs.err = config.LoadBootstrapToken(getenv)
	if bs.err != nil && !errors.Is(bs.err, config.ErrBootstrapUnreadable) {
		_, _ = fmt.Fprintf(stderr, "privo: invalid configuration:\n%v\n", bs.err)
		return 2
	}
	bs.source = config.BootstrapTokenSource(getenv)
	// The admin site's settings are read here, and only here and by `admin
	// setup-link`, so a mistake in them cannot stop the healthcheck or the
	// break-glass `token create` (R-03).
	webCfg, err := config.LoadWeb(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: invalid configuration:\n%v\n", err)
		return 2
	}
	if bs.token != "" {
		// Removed from the environment so that child processes (stdio servers)
		// never inherit it. This does not remove it from /proc/<pid>/environ,
		// which keeps the block the process started with; the file form is the
		// one that keeps the token out of there (R-08), and bootstrapAtStartup
		// warns when the variable was used.
		_ = os.Unsetenv("PRIVO_BOOTSTRAP_TOKEN")
	}
	logger := newLogger(stderr, cfg)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := runGateway(ctx, cfg, webCfg, bs, getenv, logger); err != nil {
		logger.Error("privo stopped with error", "err", err)
		return 1
	}
	return 0
}

func runGateway(ctx context.Context, cfg config.Config, webCfg config.Web, bs bootstrapInput, getenv func(string) string, logger *slog.Logger) error {
	if getenv("PRIVO_MASTER_KEY") != "" {
		logger.Warn("master key given as PRIVO_MASTER_KEY; prefer PRIVO_MASTER_KEY_FILE: an environment variable stays readable in /proc/<pid>/environ for the life of the process")
	}
	keys, err := vault.LoadKeyProvider(getenv)
	if err != nil {
		return err
	}
	allow, err := egress.ParseAllowList(cfg.EgressAllow)
	if err != nil {
		return err
	}
	policy := egress.Policy{Allow: allow}

	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	// Clean-up runs after ctx is cancelled by SIGTERM, so it must not use ctx.
	defer func() { _ = st.Close() }() //nolint:contextcheck // see comment above
	org, err := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
	if err != nil {
		return err
	}
	kr := keyring.New(st, keys)
	if err := kr.EnsureOrgKey(ctx, org.ID); err != nil {
		return err
	}
	// A database restored with the wrong master key would start and then fail on
	// every credential. Opening the org key once makes that a clear stop.
	if _, _, err := kr.Current(ctx, org.ID); err != nil {
		return fmt.Errorf("the master key cannot open this database (is PRIVO_MASTER_KEY_FILE the key it was created with, or a restored backup's key?): %w", err)
	}
	if err := bootstrapAtStartup(ctx, st, org.ID, bs, logger); err != nil {
		return err
	}

	var rn runner.Runner
	if cfg.Runner == "local" {
		logger.Warn("PRIVO_RUNNER=local runs stdio servers on this host without isolation; use only for development and tests")
		rn = runner.Local{}
	}
	// Servers where each person uses their own account (ADR 0017) need the
	// admin site: people connect their accounts there.
	var oauthClient *upstreamauth.Client
	var grantSvc *grants.Service
	var grantSource upstream.GrantSource
	if len(webCfg.Missing(cfg.PublicURL)) == 0 && cfg.PublicURL.Path == "" {
		oauthClient = &upstreamauth.Client{HTTP: egress.NewClient(egress.Options{Policy: policy, SameHostOnly: true, MaxRedirects: 1}), UserAgent: "privo/" + buildinfo.Version}
		grantSvc = &grants.Service{Store: st, Keys: kr, Auth: oauthClient, PublicURL: cfg.PublicURL, Logger: logger}
		grantSource = grantSvc
	}
	mgr := upstream.New(upstream.Options{Store: st, Keys: kr, Runner: rn, HTTPClient: egress.NewClient(egress.Options{Policy: policy, SameHostOnly: true}),
		RetryBase: cfg.RetryBase, RetryMax: cfg.RetryMax, RefreshGap: cfg.RefreshGap, MaxResponseBytes: cfg.MaxResponseBytes, Logger: logger,
		Grants: grantSource})
	defer mgr.Close()
	if err := mgr.Sync(ctx, org.ID); err != nil {
		return err
	}
	if cfg.BackupInterval > 0 {
		job := &backupJob{st: st, org: org.ID, dataDir: cfg.DataDir, dir: cfg.BackupDir, interval: cfg.BackupInterval, keep: cfg.BackupKeep, logger: logger, now: time.Now}
		jobCtx, stopJob := context.WithCancel(ctx)
		jobDone := make(chan struct{})
		go func() { job.run(jobCtx); close(jobDone) }()
		defer func() { stopJob(); <-jobDone }() // before the store is closed
		logger.Info("scheduled backups on", "every", cfg.BackupInterval.String(), "keep", cfg.BackupKeep)
	}
	aw := audit.NewWriter(st, audit.Options{Logger: logger}) //nolint:contextcheck // the writer must outlive request contexts to flush on shutdown
	defer aw.Close()
	authn := auth.New(st)

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	base := publicBase(cfg, ln)
	prm := base + "/.well-known/oauth-protected-resource/mcp"
	approvals := &approval.Service{Store: st, Keys: kr, Hold: cfg.ApprovalHold}
	gw := &gateway.Gateway{Store: st, Upstream: mgr, Keys: kr, Audit: aw, Auth: authn, Approval: approvals, Logger: logger,
		ResourceMetadataURL: prm, BaseURL: base, CallTimeout: cfg.CallTimeout}
	api := &control.API{Store: st, Keys: kr, Auth: authn, Upstream: mgr, Audit: aw, Approval: approvals, Egress: policy, Logger: logger,
		TokenTTL: cfg.TokenTTL, OAuth: oauthClient, Grants: grantSvc}
	if grantSvc != nil {
		api.PublicURL = cfg.PublicURL
	}

	// Privo's own authorization server (ADR 0022): AI clients sign in to the
	// gateway. On only when it is switched on and web sign-in is set up,
	// because a person signs in to allow a client.
	var authServer *oauthserver.Server
	if webCfg.OAuthServer && cfg.PublicURL != nil && cfg.PublicURL.Path == "" && len(webCfg.Missing(cfg.PublicURL)) == 0 {
		// Client documents are public by definition: they are fetched with no
		// private range allowed, whatever PRIVO_EGRESS_ALLOW_CIDRS lets the
		// upstream connections reach (review of 2026-11-15, S-05).
		clients := &oauthserver.Clients{Org: org.ID, Store: st, Policy: webCfg.OAuthClients, HTTP: oauthserver.NewDocumentClient(egress.Policy{})}
		authServer, err = oauthserver.New(ctx, oauthserver.Options{Org: org.ID, Store: st, Keys: kr, Audit: aw, Clients: clients,
			Issuer: strings.TrimSuffix(cfg.PublicURL.String(), "/"), Registration: webCfg.OAuthRegistration,
			TrustedProxies: cfg.TrustedProxies, Logger: logger})
		if err != nil {
			return err
		}
		authn.AcceptOAuth(authServer)
		gw.Scopes = []string{auth.ScopeGateway}
		api.AuthServer, api.Clients = authServer, clients
		logger.Info("authorization server on", "issuer", authServer.Issuer(), "clients", webCfg.OAuthClients, "registration", webCfg.OAuthRegistration)
	} else if webCfg.OAuthServer {
		logger.Warn("PRIVO_OAUTH_SERVER is on but web sign-in is not set up; the authorization server stays off")
	}

	site := adminSite(webCfg, cfg, policy, st, org.ID, aw, logger, siteConnections{grants: grantSvc, oauth: oauthClient, upstream: mgr, api: api, keys: kr, authServer: authServer})
	siteCtx, stopSite := context.WithCancel(ctx)
	siteDone := make(chan struct{})
	go func() { site.Run(siteCtx, 10*time.Minute); close(siteDone) }()
	if authServer != nil {
		go pruneEvery(siteCtx, 10*time.Minute, logger, "authorization server", authServer.Prune)
	}
	defer func() { stopSite(); <-siteDone }() // before the store is closed

	reg := health.NewRegistry(0)
	reg.Register("database", st.Ping)
	srv := server.New(server.Options{
		Logger: logger, Health: reg, ShutdownTimeout: cfg.ShutdownTimeout,
		Routes: func(mux *http.ServeMux) {
			mux.Handle("/mcp", gw.Handler())
			api.Routes(mux)
			site.Routes(mux)
			// With the authorization server off the metadata names none and a
			// client is told nothing about OAuth: personal tokens only.
			prmDoc := map[string]any{"resource": base + "/mcp", "bearer_methods_supported": []string{"header"}, "authorization_servers": []string{}}
			if authServer != nil {
				authServer.Routes(mux)
				prmDoc["authorization_servers"] = []string{authServer.Issuer()}
				prmDoc["scopes_supported"] = []string{auth.ScopeGateway}
			}
			mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(prmDoc)
			})
		},
	})
	return srv.Serve(ctx, ln)
}

func publicBase(cfg config.Config, ln net.Listener) string {
	if cfg.PublicURL != nil {
		return cfg.PublicURL.String()
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return "http://localhost:" + port
}

// tokenCreate issues a token of nobody from the command line: --person is a
// label and gives no member's teams or accounts (ADR 0021). Break-glass and
// first setup). It works while the server runs.
func tokenCreate(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	name, role, kind, person := "", store.RoleOwner, store.KindControl, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			i++
			if i < len(args) {
				name = args[i]
			}
		case "--role":
			i++
			if i < len(args) {
				role = args[i]
			}
		case "--kind":
			i++
			if i < len(args) {
				kind = args[i]
			}
		case "--person":
			i++
			if i < len(args) {
				person = args[i]
			}
		default:
			_, _ = fmt.Fprintf(stderr, "privo token create: unknown flag %q\nusage: privo token create --name NAME [--role owner|admin|auditor|approver|member] [--kind control|gateway] [--person NAME]\n", args[i])
			return 2
		}
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: invalid configuration:\n%v\n", err)
		return 2
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		st, err = store.OpenShared(ctx, cfg.DataDir)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	org, err := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n", err)
		return 1
	}
	plain, _, err := control.IssueToken(ctx, st, org.ID, control.TokenSpec{Name: name, Role: role, Kind: kind, Person: person, TTL: cfg.TokenTTL})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, plain)
	_, _ = fmt.Fprintln(stderr, "Store this token now; it cannot be shown again.")
	return 0
}

// siteConnections is what the admin site needs for people's upstream
// connections; with grants nil the connect pages are not served.
type siteConnections struct {
	grants   *grants.Service
	oauth    *upstreamauth.Client
	upstream *upstream.Manager
	api      *control.API     // the admin app calls it in process (ADR 0019 point 4)
	keys     *keyring.Keyring // seals the cookie of a sign-in in flight (ADR 0016 amendment 1)
	// authServer is Privo's own authorization server; nil while it is off.
	authServer *oauthserver.Server
}

// pruneEvery runs a clean-up now and then until ctx ends. Correctness never
// depends on it.
func pruneEvery(ctx context.Context, every time.Duration, logger *slog.Logger, what string, prune func(context.Context) error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := prune(ctx); err != nil && ctx.Err() == nil {
			logger.Error("prune", "what", what, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// adminSite builds the admin website (ADR 0016). With anything missing it is
// still returned, off: it answers 503 and says which settings to add.
func adminSite(webCfg config.Web, cfg config.Config, policy egress.Policy, st *store.Store, org store.OrgID, aw *audit.Writer, logger *slog.Logger, conn siteConnections) *web.Server {
	opts := web.Options{Store: st, Org: org, Audit: aw, Logger: logger, PublicURL: cfg.PublicURL, App: webapp.Dist(),
		IdleTimeout: webCfg.IdleTimeout, MaxAge: webCfg.MaxAge, Missing: webCfg.Missing(cfg.PublicURL)}
	if conn.grants != nil && conn.oauth != nil && conn.upstream != nil {
		opts.Grants, opts.OAuth, opts.Upstream = conn.grants, conn.oauth, conn.upstream
	}
	if conn.api != nil {
		opts.API = conn.api
	}
	if conn.keys != nil {
		opts.Keys = conn.keys
	}
	if conn.authServer != nil {
		opts.AuthServer = conn.authServer
	}
	if len(opts.Missing) == 0 {
		opts.GitHub = &githubauth.Client{ClientID: webCfg.ClientID, ClientSecret: webCfg.ClientSecret.Reveal(), BaseURL: webCfg.BaseURL, APIURL: webCfg.APIURL,
			HTTP: egress.NewClient(egress.Options{Policy: policy, SameHostOnly: true, MaxRedirects: 1})}
	}
	site := web.New(opts)
	if len(opts.Missing) > 0 {
		logger.Info("admin site is off: sign-in with GitHub is not set up", "missing", opts.Missing)
	} else {
		logger.Info("admin site is on", "sign_in", webCfg.BaseURL.Host, "session_idle", webCfg.IdleTimeout.String(), "session_max_age", webCfg.MaxAge.String())
	}
	return site
}
