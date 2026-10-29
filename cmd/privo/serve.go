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
	"syscall"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/config"
	"privo.club/privo/internal/control"
	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/gateway"
	"privo.club/privo/internal/health"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/runner"
	"privo.club/privo/internal/server"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstream"
	"privo.club/privo/internal/vault"
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
	if bs.token != "" {
		// Like the master key: gone from the environment, so child processes never inherit it.
		_ = os.Unsetenv("PRIVO_BOOTSTRAP_TOKEN")
	}
	logger := newLogger(stderr, cfg)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := runGateway(ctx, cfg, bs, logger); err != nil {
		logger.Error("privo stopped with error", "err", err)
		return 1
	}
	return 0
}

func runGateway(ctx context.Context, cfg config.Config, bs bootstrapInput, logger *slog.Logger) error {
	if os.Getenv("PRIVO_MASTER_KEY") != "" {
		logger.Warn("master key given as PRIVO_MASTER_KEY; prefer PRIVO_MASTER_KEY_FILE: an environment variable stays readable in /proc/<pid>/environ for the life of the process")
	}
	keys, err := vault.LoadEnvKeyProvider()
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
	if err := bootstrapAtStartup(ctx, st, org.ID, bs, logger); err != nil {
		return err
	}

	var rn runner.Runner
	if cfg.Runner == "local" {
		logger.Warn("PRIVO_RUNNER=local runs stdio servers on this host without isolation; use only for development and tests")
		rn = runner.Local{}
	}
	mgr := upstream.New(upstream.Options{Store: st, Keys: kr, Runner: rn, HTTPClient: egress.NewClient(egress.Options{Policy: policy, SameHostOnly: true}),
		RetryBase: cfg.RetryBase, RetryMax: cfg.RetryMax, RefreshGap: cfg.RefreshGap, MaxResponseBytes: cfg.MaxResponseBytes, Logger: logger})
	defer mgr.Close()
	if err := mgr.Sync(ctx, org.ID); err != nil {
		return err
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
		TokenTTL: cfg.TokenTTL}

	reg := health.NewRegistry(0)
	reg.Register("database", st.Ping)
	srv := server.New(server.Options{
		Logger: logger, Health: reg, ShutdownTimeout: cfg.ShutdownTimeout,
		Routes: func(mux *http.ServeMux) {
			mux.Handle("/mcp", gw.Handler())
			api.Routes(mux)
			mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"resource":                 base + "/mcp",
					"bearer_methods_supported": []string{"header"},
					"authorization_servers":    []string{}, // OAuth server arrives with F06
				})
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

// tokenCreate issues a personal token from the command line (break-glass and
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
