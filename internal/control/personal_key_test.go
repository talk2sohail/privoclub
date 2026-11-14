package control

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/grants"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
)

// ADR 0023: adding a server whose people bring their own API key, and OAuth
// settings entered by hand.

func withPersonalServers(e *sessionEnv) {
	pub, _ := url.Parse("https://privo.example")
	e.api.PublicURL = pub
	e.api.Egress = egress.Policy{Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	e.api.Grants = &grants.Service{Store: e.st, Keys: e.api.Keys, PublicURL: pub}
}

func TestAddingAServerThatTakesEachPersonsKey(t *testing.T) {
	e := newSessionEnv(t)
	withPersonalServers(e)
	_, owner := e.user(t, "boss", store.RoleOwner)
	_, member := e.user(t, "ada", store.RoleMember)
	add := func(alias string, key map[string]any, extra map[string]any, want int) map[string]any {
		t.Helper()
		body := map[string]any{"alias": alias, "transport": "http", "url": "https://127.0.0.1:1/" + alias + "/mcp", "personal_key": key}
		for k, v := range extra {
			body[k] = v
		}
		return e.call(t, owner, http.MethodPost, "/api/v1/servers", body, want)
	}

	// Defaults: Authorization with Bearer.
	out := add("acme", map[string]any{}, nil, http.StatusCreated)
	srv, _ := out["server"].(map[string]any)
	if srv["auth"] != "key" || srv["key_header"] != "Authorization" || srv["connect_url"] != "https://privo.example/connect/acme" {
		t.Fatalf("added: %v", srv)
	}
	stored, _ := e.st.ServerByAlias(context.Background(), e.org, "acme")
	if stored.Config.Key == nil || stored.Config.Key.Prefix != "Bearer " || !stored.Config.PerPerson() {
		t.Fatalf("stored: %+v", stored.Config.Key)
	}
	// Another header: no prefix unless given; help and its link.
	add("beta", map[string]any{"header": "x-api-key", "help": "Settings, API keys.", "help_url": "https://beta.example/keys"}, nil, http.StatusCreated)
	stored, _ = e.st.ServerByAlias(context.Background(), e.org, "beta")
	if k := stored.Config.Key; k.Header != "X-Api-Key" || k.Prefix != "" || k.Help != "Settings, API keys." || k.HelpURL != "https://beta.example/keys" {
		t.Errorf("stored: %+v", k)
	}
	add("gamma", map[string]any{"header": "Authorization", "prefix": "Token "}, nil, http.StatusCreated)
	// A member sees that it takes their own key, not the header.
	for _, s := range e.call(t, member, http.MethodGet, "/api/v1/servers", nil, http.StatusOK)["servers"].([]any) {
		v := s.(map[string]any)
		if v["alias"] == "beta" && (v["auth"] != "key" || v["key_header"] != nil) {
			t.Errorf("a member's view: %v", v)
		}
	}

	for name, tc := range map[string]struct {
		key, extra map[string]any
	}{
		"a reserved header":     {map[string]any{"header": "Host"}, nil},
		"a cookie":              {map[string]any{"header": "Cookie"}, nil},
		"a header with a space": {map[string]any{"header": "X Key"}, nil},
		"an MCP header":         {map[string]any{"header": "Mcp-Session-Id"}, nil},
		"a prefix with a break": {map[string]any{"prefix": "Bearer\r\nX-Evil: 1"}, nil},
		"a long prefix":         {map[string]any{"prefix": strings.Repeat("p", 33)}, nil},
		"help with a control":   {map[string]any{"help": "a\x00b"}, nil},
		"long help":             {map[string]any{"help": strings.Repeat("h", 501)}, nil},
		"an http help link":     {map[string]any{"help_url": "http://acme.example/keys"}, nil},
		"shared headers too":    {map[string]any{}, map[string]any{"headers": map[string]string{"X-Org": "1"}}},
		"oauth too":             {map[string]any{}, map[string]any{"oauth": map[string]any{}}},
		"a stdio server":        {map[string]any{}, map[string]any{"transport": "stdio", "command": map[string]any{"path": "/bin/x", "sha256": strings.Repeat("a", 64)}}},
	} {
		code, raw := e.api.Serve(context.Background(), owner, http.MethodPost, "/api/v1/servers",
			func() map[string]any {
				b := map[string]any{"alias": "bad", "transport": "http", "url": "https://127.0.0.1:1/bad/mcp", "personal_key": tc.key}
				for k, v := range tc.extra {
					b[k] = v
				}
				return b
			}())
		if code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if _, err := e.st.ServerByAlias(context.Background(), e.org, "bad"); err == nil {
		t.Error("a refused server was stored")
	}
	// Without the admin site there is no page to enter a key on.
	e.api.Grants = nil
	if code, raw := e.api.Serve(context.Background(), owner, http.MethodPost, "/api/v1/servers",
		map[string]any{"alias": "delta", "transport": "http", "url": "https://127.0.0.1:1/delta/mcp", "personal_key": map[string]any{}}); code != http.StatusUnprocessableEntity {
		t.Errorf("without the admin site: %d %s", code, raw)
	}
}

func TestOAuthSettingsEnteredByHand(t *testing.T) {
	e := newSessionEnv(t)
	withPersonalServers(e)
	_, owner := e.user(t, "boss", store.RoleOwner)
	post := func(alias string, oauth map[string]any) (int, string) {
		code, raw := e.api.Serve(context.Background(), owner, http.MethodPost, "/api/v1/servers",
			map[string]any{"alias": alias, "transport": "http", "url": "https://127.0.0.1:1/" + alias + "/mcp", "oauth": oauth})
		return code, string(raw)
	}
	// Nothing is discovered; the client only has to exist.
	e.api.OAuth = &upstreamauth.Client{HTTP: egress.NewClient(egress.Options{Policy: e.api.Egress}), Timeout: time.Second}
	code, raw := post("hand", map[string]any{"authorization_endpoint": "https://127.0.0.1:2/authorize", "token_endpoint": "https://127.0.0.1:2/token",
		"client_id": "privo-at-hand", "client_secret": "s3cret", "scopes": []string{"read"}})
	if code != http.StatusCreated {
		t.Fatalf("hand-entered settings: %d %s", code, raw)
	}
	stored, _ := e.st.ServerByAlias(context.Background(), e.org, "hand")
	o := stored.Config.OAuth
	if o == nil || o.Registration != store.RegistrationManual || o.ClientID != "privo-at-hand" || !o.HasClientSecret || o.TokenAuthMethod != "client_secret_basic" ||
		o.Resource != "" || o.Issuer != "" || strings.Join(o.Scopes, " ") != "read" {
		t.Fatalf("stored: %+v", o)
	}
	if strings.Contains(raw, "s3cret") {
		t.Error("the answer holds the client secret")
	}
	for name, oauth := range map[string]map[string]any{
		// Every endpoint here passes the egress policy of the test (loopback)
		// unless the case is about it, so each case fails for its own reason.
		"no client id":         {"authorization_endpoint": "https://127.0.0.1:2/authorize", "token_endpoint": "https://127.0.0.1:2/token"},
		"no token endpoint":    {"authorization_endpoint": "https://127.0.0.1:2/authorize", "client_id": "c"},
		"plain http":           {"authorization_endpoint": "http://198.51.100.1/authorize", "token_endpoint": "https://127.0.0.1:2/token", "client_id": "c"},
		"credentials in a URL": {"authorization_endpoint": "https://u:p@127.0.0.1:2/authorize", "token_endpoint": "https://127.0.0.1:2/token", "client_id": "c"},
		"a private address":    {"authorization_endpoint": "https://10.0.0.1/authorize", "token_endpoint": "https://127.0.0.1:2/token", "client_id": "c"},
		"a bad resource":       {"authorization_endpoint": "https://127.0.0.1:2/authorize", "token_endpoint": "https://127.0.0.1:2/token", "client_id": "c", "resource": "ftp://x"},
	} {
		if code, raw := post("bad", oauth); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
}
