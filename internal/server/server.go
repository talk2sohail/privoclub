// Package server runs Privo's HTTP server with graceful shutdown (ADR 0010).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"privo.club/privo/internal/buildinfo"
	"privo.club/privo/internal/health"
)

// Options configure a Server.
type Options struct {
	Logger          *slog.Logger
	Health          *health.Registry
	ShutdownTimeout time.Duration
	// Routes lets later packages (gateway, admin API) mount handlers.
	Routes func(mux *http.ServeMux)
}

// Server is Privo's HTTP server.
type Server struct {
	opts Options
	http *http.Server
}

// New builds a server. It does not start listening.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Health == nil {
		opts.Health = health.NewRegistry(0)
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 30 * time.Second
	}
	mux := http.NewServeMux()
	mux.Handle("GET /livez", health.LiveHandler())
	mux.Handle("GET /readyz", opts.Health.ReadyHandler())
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildinfo.Get())
	})
	if opts.Routes != nil {
		opts.Routes(mux)
	}
	return &Server{
		opts: opts,
		http: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20,
			ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelWarn),
		},
	}
}

// Serve accepts connections on ln until ctx is cancelled, then drains:
// readiness fails at once, in-flight requests get up to ShutdownTimeout.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	errc := make(chan error, 1)
	go func() { errc <- s.http.Serve(ln) }()
	s.opts.Logger.Info("listening", "addr", ln.Addr().String())

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	s.opts.Health.SetDraining(true)
	s.opts.Logger.Info("draining", "timeout", s.opts.ShutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.ShutdownTimeout)
	defer cancel()
	if err := s.http.Shutdown(shutdownCtx); err != nil {
		_ = s.http.Close()
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	s.opts.Logger.Info("stopped")
	return nil
}
