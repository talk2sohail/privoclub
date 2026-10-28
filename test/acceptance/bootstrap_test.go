//go:build acceptance

package acceptance

import (
	"context"
	"io"
	"net/http"
	"os/exec"
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
