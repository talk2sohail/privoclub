//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/token"
)

// generateToken runs `privo token generate`, as the operator does on a laptop.
func generateToken(t *testing.T) string {
	t.Helper()
	out, err := exec.Command(privoBin, "token", "generate").Output()
	if err != nil {
		t.Fatalf("token generate: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func tokenNames(t *testing.T, listed map[string]any) []string {
	t.Helper()
	var names []string
	raw, _ := listed["tokens"].([]any)
	for _, r := range raw {
		m, _ := r.(map[string]any)
		name, _ := m["name"].(string)
		names = append(names, name)
	}
	return names
}

// A fresh Coolify deploy has no shell, so the first admin token arrives as an
// environment variable (F24). It works once, is a control token, is never
// logged or stored in plain text, and does not return after it is revoked.
func TestBootstrapTokenFirstRun(t *testing.T) {
	plain := generateToken(t)
	id, secret, err := token.Parse(plain)
	if err != nil {
		t.Fatal(err)
	}
	g := newGateway(t, "PRIVO_BOOTSTRAP_TOKEN="+plain)
	g.start()

	listed := g.api(http.MethodGet, "/api/v1/tokens", plain, nil, http.StatusOK)
	if names := tokenNames(t, listed); len(names) != 1 || names[0] != "bootstrap" {
		t.Fatalf("tokens after first boot = %v, want [bootstrap]", names)
	}

	// A control token never reaches the gateway surface.
	if code, _ := g.raw(http.MethodPost, "/mcp", plain, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}); code != http.StatusUnauthorized {
		t.Fatalf("bootstrap token on /mcp: status %d, want 401", code)
	}

	// The owner replaces it with a token of their own and revokes the bootstrap token.
	created := g.api(http.MethodPost, "/api/v1/tokens", plain, map[string]any{"name": "owner", "role": "owner", "kind": "control"}, http.StatusCreated)
	mine, _ := created["token"].(string)
	g.api(http.MethodDelete, "/api/v1/tokens/"+id, mine, nil, http.StatusOK)
	// Revocation takes effect within the verification cache lifetime (ADR 0003).
	g.eventually("revoked bootstrap token to be refused", 5*time.Second, func() bool {
		code, _ := g.raw(http.MethodGet, "/api/v1/tokens", plain, nil)
		return code == http.StatusUnauthorized
	})

	// Restart with the variable still set, as Coolify does: it must not come back.
	g.stop()
	g.start()
	if code, _ := g.raw(http.MethodGet, "/api/v1/tokens", plain, nil); code != http.StatusUnauthorized {
		t.Fatalf("bootstrap token after restart: status %d, want 401", code)
	}
	g.api(http.MethodGet, "/api/v1/tokens", mine, nil, http.StatusOK)
	if !strings.Contains(g.logs.String(), "PRIVO_BOOTSTRAP_TOKEN") {
		t.Errorf("second boot should tell the operator to remove the variable")
	}

	if strings.Contains(g.logs.String(), secret) || strings.Contains(g.logs.String(), plain) {
		t.Errorf("the bootstrap secret appears in the logs")
	}
	g.assertNotOnDisk(secret)
}

// An org that has already been administered ignores the variable.
func TestBootstrapTokenIgnoredWhenAlreadyAdministered(t *testing.T) {
	plain := generateToken(t)
	g := startGateway(t, "PRIVO_BOOTSTRAP_TOKEN="+plain) // creates an owner token with the CLI first
	if code, _ := g.raw(http.MethodGet, "/api/v1/tokens", plain, nil); code != http.StatusUnauthorized {
		t.Fatalf("bootstrap token on an administered org: status %d, want 401", code)
	}
	g.api(http.MethodGet, "/api/v1/tokens", g.owner, nil, http.StatusOK)
}

func TestRootPage(t *testing.T) {
	g := startGateway(t)
	get := func(path string) (*http.Response, string) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, g.base+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	resp, body := get("/")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") || !strings.Contains(body, "Privo") {
		t.Fatalf("GET / = %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if resp, _ := get("/does-not-exist"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404", resp.StatusCode)
	}
}

// R-03 (independent review, 2026-10-29): the bootstrap variables were checked
// by the configuration loader that every subcommand shares. The operator who
// mounts the token as a file, uses it, and deletes the file but not the
// variable (the natural clean-up) then had a container that the healthcheck
// marked unhealthy, a restart that exited, and a break-glass `token create`
// that exited too.
func TestBootstrapFileRemovedAfterUse(t *testing.T) {
	plain := generateToken(t)
	path := filepath.Join(t.TempDir(), "bootstrap-token")
	if err := os.WriteFile(path, []byte(plain+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := newGateway(t, "PRIVO_BOOTSTRAP_TOKEN_FILE="+path)
	g.start()
	g.api(http.MethodGet, "/api/v1/tokens", plain, nil, http.StatusOK)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if out, err := g.command("healthcheck").CombinedOutput(); err != nil {
		t.Errorf("healthcheck of the running gateway after the file was removed: %v: %s", err, out)
	}
	if out, err := g.command("token", "create", "--name", "break-glass", "--role", "owner", "--kind", "control").Output(); err != nil || !strings.HasPrefix(string(out), "privo_pat_") {
		t.Errorf("token create after the file was removed: %v: %q", err, out)
	}
	g.stop()
	g.start() // fatal if the gateway does not come up
	if !strings.Contains(g.logs.String(), "PRIVO_BOOTSTRAP_TOKEN_FILE") {
		t.Errorf("the restart should tell the operator to remove the variable:\n%s", g.logs.String())
	}
}

// The other half: a first boot that was told to install a token and cannot
// read it must still refuse to start (fail closed), and say why.
func TestBootstrapFileMissingOnFirstBootRefusesToStart(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-file")
	g := newGateway(t, "PRIVO_BOOTSTRAP_TOKEN_FILE="+missing)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, privoBin, "serve")
	cmd.Env = g.env
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("serve kept running with an unreadable bootstrap file:\n%s", out)
	}
	if err == nil || !strings.Contains(string(out), "PRIVO_BOOTSTRAP_TOKEN_FILE") {
		t.Fatalf("serve: err %v, output %q; want a failure that names PRIVO_BOOTSTRAP_TOKEN_FILE", err, out)
	}
}

// R-09 (independent review, 2026-10-29): the bootstrap install is audited as an
// admin event, and on a deploy without a shell the owner has no other way to
// read it. Admin events come back from the audit endpoint with kind=admin; the
// default list stays the list of calls.
func TestAdminAuditEventsAreReadable(t *testing.T) {
	plain := generateToken(t)
	id, secret, err := token.Parse(plain)
	if err != nil {
		t.Fatal(err)
	}
	g := newGateway(t, "PRIVO_BOOTSTRAP_TOKEN="+plain)
	g.start()

	created := g.api(http.MethodPost, "/api/v1/tokens", plain, map[string]any{"name": "ada", "role": "member", "kind": "gateway"}, http.StatusCreated)
	member, _ := created["token"].(string)

	admin := g.api(http.MethodGet, "/api/v1/audit/events?kind=admin", plain, nil, http.StatusOK)
	events, _ := admin["events"].([]any)
	byAction := map[string]map[string]any{}
	for _, e := range events {
		m, _ := e.(map[string]any)
		action, _ := m["action"].(string)
		byAction[action] = m
	}
	install := byAction["bootstrap.token.install:"+id]
	if install == nil {
		t.Fatalf("no bootstrap install event in %v", events)
	}
	if install["actor"] != "system" || install["decision"] != "allow" || install["outcome"] != "ok" || install["time"] == nil {
		t.Errorf("install event = %v", install)
	}
	if created := byAction["token.create:gateway:member:ada"]; created == nil || created["actor"] != "bootstrap" {
		t.Errorf("token create event = %v (all: %v)", created, byAction)
	}
	if strings.Contains(g.logs.String()+toJSON(t, admin), secret) {
		t.Errorf("the bootstrap secret appears in the admin events or the logs")
	}

	// The default is still the list of calls, and calls are not admin events.
	calls := g.api(http.MethodGet, "/api/v1/audit/events", plain, nil, http.StatusOK)
	if list, _ := calls["events"].([]any); len(list) != 0 {
		t.Errorf("the call list holds admin events: %v", list)
	}
	g.api(http.MethodGet, "/api/v1/audit/events?kind=calls", plain, nil, http.StatusOK)
	g.api(http.MethodGet, "/api/v1/audit/events?kind=bogus", plain, nil, http.StatusBadRequest)
	// Who may read the audit log may read this too (an auditor does), and an
	// agent's gateway token may not.
	watcher := g.api(http.MethodPost, "/api/v1/tokens", plain, map[string]any{"name": "watcher", "role": "auditor", "kind": "control"}, http.StatusCreated)
	g.api(http.MethodGet, "/api/v1/audit/events?kind=admin", watcher["token"].(string), nil, http.StatusOK)
	g.api(http.MethodGet, "/api/v1/audit/events?kind=admin", member, nil, http.StatusUnauthorized)
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
