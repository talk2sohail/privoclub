// Command privo is the Privo MCP gateway.
//
// Usage:
//
//	privo serve         run the gateway (default)
//	privo version       print build information
//	privo healthcheck   exit 0 if the local server is live (for container health checks)
//	privo token create  issue a token from the command line (needs the data directory)
//	privo token generate  print a fresh token for PRIVO_BOOTSTRAP_TOKEN (touches nothing)
//	privo backup        write a consistent snapshot of the database (the gateway may be running)
//	privo restore       put a snapshot in place (the gateway must be stopped)
//	privo config show   print every setting with its effective value and where it came from
//	privo admin setup-link  print a one-time URL that makes the first person to sign in with GitHub the owner
//	privo admin invite      print a one-time URL that adds a person with a role (ADR 0017)
//	privo audit verify      check the audit chain: links, hashes and signatures (ADR 0028)
//
// Settings come from the environment and, optionally, a configuration file
// named by --config FILE (before the command) or PRIVO_CONFIG_FILE (ADR 0015).
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
	"strings"
	"time"

	"privo.club/privo/internal/buildinfo"
	"privo.club/privo/internal/config"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

const usage = "usage: privo [--config FILE] [serve|version|healthcheck|token create|token generate|backup|restore|config show|admin setup-link|admin invite|audit verify]"

// configCommands are the commands that read settings, and so the configuration
// file. version, help and token generate touch nothing and never read it.
var configCommands = map[string]bool{"serve": true, "healthcheck": true, "token": true, "backup": true, "restore": true, "config": true, "admin": true, "audit": true}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	path, args, err := configFlag(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n%s\n", err, usage)
		return 2
	}
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch {
	case cmd == "version":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(buildinfo.Get())
		return 0
	case cmd == "help" || cmd == "-h" || cmd == "--help":
		_, _ = fmt.Fprintln(stdout, usage)
		return 0
	case cmd == "token" && len(args) >= 2 && args[1] == "generate":
		return tokenGenerate(args[2:], stdout, stderr)
	case !configCommands[cmd]:
		_, _ = fmt.Fprintf(stderr, "privo: unknown command %q\n%s\n", cmd, usage)
		return 2
	}

	if path == "" {
		path = strings.TrimSpace(getenv("PRIVO_CONFIG_FILE"))
	}
	src, err := loadSource(path, getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: invalid configuration:\n%v\n", err)
		return 2
	}
	getenv = src.get

	switch cmd {
	case "serve":
		return serve(stderr, getenv)
	case "healthcheck":
		return healthcheck(stderr, getenv)
	case "token":
		if len(args) < 2 || args[1] != "create" {
			_, _ = fmt.Fprintln(stderr, "usage: privo token create --name NAME [--role owner|admin|auditor|approver|member] [--kind control|gateway] [--person NAME]\n       privo token generate")
			return 2
		}
		return tokenCreate(args[2:], stdout, stderr, getenv)
	case "backup":
		return backupCmd(args[1:], stdout, stderr, getenv)
	case "restore":
		return restoreCmd(args[1:], stdout, stderr, getenv)
	case "admin":
		return adminCmd(args[1:], stdout, stderr, getenv)
	case "audit":
		return auditCmd(args[1:], stdout, stderr, getenv)
	default: // config
		return configCmd(args[1:], stdout, stderr, src)
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
