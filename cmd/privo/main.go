// Command privo is the Privo MCP gateway.
//
// Usage:
//
//	privo serve         run the gateway (default)
//	privo version       print build information
//	privo healthcheck   exit 0 if the local server is live (for container health checks)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"privo.club/privo/internal/buildinfo"
	"privo.club/privo/internal/config"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "serve":
		return serve(stderr, getenv)
	case "version":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(buildinfo.Get())
		return 0
	case "healthcheck":
		return healthcheck(stderr, getenv)
	case "token":
		if len(args) < 2 || args[1] != "create" {
			_, _ = fmt.Fprintln(stderr, "usage: privo token create --name NAME [--role owner|admin|auditor|approver|member] [--kind control|gateway] [--person NAME]")
			return 2
		}
		return tokenCreate(args[2:], stdout, stderr, getenv)
	case "help", "-h", "--help":
		_, _ = fmt.Fprintln(stdout, "usage: privo [serve|version|healthcheck|token create]")
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "privo: unknown command %q\nusage: privo [serve|version|healthcheck|token create]\n", cmd)
		return 2
	}
}

func newLogger(w io.Writer, cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var h slog.Handler = slog.NewJSONHandler(w, opts)
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h).With("service", "privo", "version", buildinfo.Version)
}

func healthcheck(stderr io.Writer, getenv func(string) string) int {
	cfg, err := config.Load(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: invalid configuration: %v\n", err)
		return 2
	}
	_, port, _ := net.SplitHostPort(cfg.ListenAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/livez", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: healthcheck: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(stderr, "privo: healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
