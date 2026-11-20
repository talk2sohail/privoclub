package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func do(t *testing.T, method, url string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestRootPage(t *testing.T) {
	base, cancel, done := start(t, Options{})
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()

	t.Run("serves a static page with hardening headers", func(t *testing.T) {
		resp, body := do(t, http.MethodGet, base+"/")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") || !strings.Contains(ct, "utf-8") {
			t.Fatalf("content type %q", ct)
		}
		want := map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "no-referrer",
		}
		for h, v := range want {
			if got := resp.Header.Get(h); got != v {
				t.Errorf("%s = %q, want %q", h, got, v)
			}
		}
		csp := resp.Header.Get("Content-Security-Policy")
		for _, d := range []string{"default-src 'none'", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'none'"} {
			if !strings.Contains(csp, d) {
				t.Errorf("CSP %q lacks %s", csp, d)
			}
		}
		if strings.Contains(csp, "unsafe-inline") {
			t.Errorf("CSP allows unsafe-inline: %s", csp)
		}
		if !strings.Contains(body, "Privo") {
			t.Errorf("page does not name the product")
		}
		lower := strings.ToLower(body)
		for _, banned := range []string{"<script", "version", "livez", "readyz", "/api/", "javascript:"} {
			if strings.Contains(lower, banned) {
				t.Errorf("page contains %q", banned)
			}
		}
		if resp.Header.Get("Cache-Control") == "" {
			t.Errorf("no Cache-Control")
		}
	})

	t.Run("the inline style is allowed by the CSP hash", func(t *testing.T) {
		resp, body := do(t, http.MethodGet, base+"/")
		m := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindStringSubmatch(body)
		if m == nil {
			t.Skip("page has no inline style")
		}
		sum := sha256.Sum256([]byte(m[1]))
		hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !strings.Contains(resp.Header.Get("Content-Security-Policy"), hash) {
			t.Fatalf("CSP %q does not carry %s", resp.Header.Get("Content-Security-Policy"), hash)
		}
	})

	t.Run("reflects nothing from the request", func(t *testing.T) {
		_, plain := do(t, http.MethodGet, base+"/")
		_, withQuery := do(t, http.MethodGet, base+"/?q=%3Cscript%3Ealert(1)%3C/script%3E&next=https://evil.example")
		if plain != withQuery {
			t.Fatal("response depends on the query string")
		}
	})

	t.Run("only the exact root path", func(t *testing.T) {
		for _, p := range []string{"/index.html", "/nope", "/admin", "/favicon.ico"} {
			if resp, _ := do(t, http.MethodGet, base+p); resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404", p, resp.StatusCode)
			}
		}
	})

	t.Run("read-only", func(t *testing.T) {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			resp, _ := do(t, m, base+"/")
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s / = %d, want 405", m, resp.StatusCode)
			}
		}
		resp, body := do(t, http.MethodHead, base+"/")
		if resp.StatusCode != http.StatusOK || body != "" {
			t.Errorf("HEAD / = %d with %d body bytes", resp.StatusCode, len(body))
		}
	})

	t.Run("health endpoints are untouched", func(t *testing.T) {
		for _, p := range []string{"/livez", "/readyz"} {
			if code, _ := get(t, base+p); code != http.StatusOK {
				t.Errorf("%s = %d", p, code)
			}
		}
	})
}

// The home page leads people to the admin site and tells AI clients where to
// connect; both come from configuration (owner's request, 2026-11-20).
func TestHomeOffersSignInAndTheMCPAddress(t *testing.T) {
	base, cancel, done := start(t, Options{Home: Home{SignIn: true, MCPURL: "https://app.privo.example/mcp"}})
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	resp, body := do(t, http.MethodGet, base+"/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{`<a class="button" href="/admin/">Sign in</a>`, "<code>https://app.privo.example/mcp</code>", "Start page"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Error("page has a script")
	}
}

func TestHomeWithoutTheAdminSite(t *testing.T) {
	base, cancel, done := start(t, Options{})
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	_, body := do(t, http.MethodGet, base+"/")
	if strings.Contains(body, `href="/admin/"`) || !strings.Contains(body, "not set up") || !strings.Contains(body, "/mcp</code> path of this address") {
		t.Errorf("page without sign-in:\n%s", body)
	}
}

func TestHomeEscapesItsConfiguration(t *testing.T) {
	if got := rootHTML(Home{MCPURL: `https://x.example/"><script>`}); strings.Contains(got, "<script>") {
		t.Fatal("the MCP address is not escaped")
	}
}
