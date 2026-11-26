//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestBlockedEgressAuditedAndCompletionPoliced verifies Issue #52 (F19 / ADR
// 0008 and ADR 0001) end-to-end against the compiled privo binary:
//  1. Blocked SSRF / egress attempts (admin registration and anonymous OAuth
//     client metadata fetches) are audited with strictly redacted targets, logged
//     as upstream diagnostics, rate-limited against anonymous floods, never
//     leak query/userinfo/path secrets, and keep the audit chain valid.
//  2. completion/complete advertises capabilities, enforces accepted pins,
//     team scoping (anti-probing -32602), policy deny & fail-closed ask,
//     upstream reply validation, reserved _meta stripping, and full auditing.
func TestBlockedEgressAuditedAndCompletionPoliced(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh, "PRIVO_OAUTH_SERVER=on", "PRIVO_OAUTH_CLIENTS=any")
	boss := ownerOf(t, g, gh)
	_, adaToken := join(t, g, gh, boss, "ada", 2)

	// ---- Part A: blocked egress auditing + flood rate-limiting + redaction ----
	const (
		adminSecret = "ADMIN-SECRET-DO-NOT-AUDIT-719e"
		adminPass   = "SUPERPASS-DO-NOT-AUDIT-8a2b"
		anonSecret  = "ANON-SECRET-DO-NOT-AUDIT-3c4d"
	)

	// 1. Server registration targeting cloud metadata with userinfo, path, query, fragment.
	badURL := "https://user:" + adminPass + "@169.254.169.254/latest/meta-data/?api_key=" + adminSecret + "#frag"
	code, body := boss.app(http.MethodPost, "/v1/servers", map[string]any{
		"alias":     "ssrf",
		"transport": "http",
		"url":       badURL,
	}, nil)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("blocked server registration: status %d, want 422: %v", code, body)
	}
	if raw, _ := json.Marshal(body); strings.Contains(string(raw), adminSecret) || strings.Contains(string(raw), adminPass) {
		t.Fatalf("422 response leaked URL secret: %s", raw)
	}

	// 2. Repeated anonymous OAuth client metadata fetches targeting loopback (still
	// blocked even under PRIVO_EGRESS_ALLOW_CIDRS because oauthserver sets Allow: nil).
	// Flood must rate-limit to 1 audit entry per (org, caller, target, reason).
	clientIDBase := "https://127.0.0.1:9443/secret/client.json?token=" + anonSecret
	for i := range 3 {
		authzQ := url.Values{
			"client_id":             {clientIDBase + "&n=" + string(rune('a'+i))},
			"redirect_uri":          {"https://127.0.0.1:9443/cb"},
			"response_type":         {"code"},
			"code_challenge":        {strings.Repeat("a", 43)},
			"code_challenge_method": {"S256"},
		}
		authzURL := g.base + "/oauth/authorize?" + authzQ.Encode()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, authzURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := (&http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}).Do(req)
		if err != nil {
			t.Fatalf("attempt %d GET /oauth/authorize: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("attempt %d /oauth/authorize returned 200 for blocked loopback client_id", i)
		}
	}

	var adminEvents []map[string]any
	g.eventually("blocked egress audit events recorded", 5*time.Second, func() bool {
		_, out := boss.app(http.MethodGet, "/v1/audit/events?kind=admin&limit=100", nil, nil)
		adminEvents = list(out, "events")
		var sawAdmin, sawAnon bool
		for _, ev := range adminEvents {
			act, _ := ev["action"].(string)
			if !strings.HasPrefix(act, "egress.blocked:") {
				continue
			}
			if ev["server"] == "https://169.254.169.254" && ev["actor"] == "boss" {
				sawAdmin = true
			}
			if ev["server"] == "127.0.0.1" && ev["actor"] == "system" {
				sawAnon = true
			}
		}
		return sawAdmin && sawAnon
	})

	anonCount := 0
	for _, ev := range adminEvents {
		act, _ := ev["action"].(string)
		if !strings.HasPrefix(act, "egress.blocked:") {
			continue
		}
		if ev["decision"] != "deny" || ev["outcome"] != "refused" {
			t.Errorf("egress block event decision=%v outcome=%v, want deny/refused: %v", ev["decision"], ev["outcome"], ev)
		}
		if ev["server"] == "127.0.0.1" {
			anonCount++
		}
	}
	if anonCount != 1 {
		t.Fatalf("anonymous blocked metadata fetch audit rows = %d, want 1 (rate-limited across 3 floods)", anonCount)
	}

	rawAdmin, _ := json.Marshal(adminEvents)
	for _, forbidden := range []string{adminSecret, adminPass, anonSecret, "/latest/meta-data", "/secret/client.json"} {
		if strings.Contains(string(rawAdmin), forbidden) {
			t.Errorf("audit event leaked %q: %s", forbidden, rawAdmin)
		}
	}

	g.eventually("upstream diagnostic logged for blocked egress", 5*time.Second, func() bool {
		diags := list(g.api(http.MethodGet, "/api/v1/telemetry/logs?category=upstream&limit=50", g.owner, nil, http.StatusOK), "logs")
		for _, d := range diags {
			msg, _ := d["message"].(string)
			if strings.Contains(msg, "egress blocked to ") {
				return true
			}
		}
		return false
	})
	diags := list(g.api(http.MethodGet, "/api/v1/telemetry/logs?category=upstream&limit=50", g.owner, nil, http.StatusOK), "logs")
	rawDiags, _ := json.Marshal(diags)
	for _, forbidden := range []string{adminSecret, adminPass, anonSecret, "/latest/meta-data", "/secret/client.json"} {
		if strings.Contains(string(rawDiags), forbidden) {
			t.Errorf("upstream diagnostic leaked %q: %s", forbidden, rawDiags)
		}
	}
	for _, forbidden := range []string{adminSecret, adminPass, anonSecret} {
		if strings.Contains(g.logs.String(), forbidden) {
			t.Errorf("gateway logs leaked %q", forbidden)
		}
	}

	// ---- Part B: completion/complete policing ----
	g.registerEcho("echo", nil, nil)
	g.registerEcho("bad", []string{"-mode", "hostile", "-null", "values"}, nil)
	g.registerEcho("poison", []string{"-mode", "hostile", "-null", "poison"}, nil)

	ctx := context.Background()
	bossSess := g.connect(g.agent)
	initRes := bossSess.InitializeResult()
	if initRes == nil || initRes.Capabilities == nil || initRes.Capabilities.Completions == nil {
		t.Fatalf("gateway did not advertise Completions capability: %+v", initRes)
	}

	// Wait until upstream discovery has populated pending pins, then verify
	// unaccepted pins cannot be completed (-32602).
	g.eventually("echo pending pins appear", 10*time.Second, func() bool {
		listed := g.api(http.MethodGet, "/api/v1/servers/echo/pins", g.owner, nil, http.StatusOK)
		return len(list(listed, "pins")) >= 3
	})
	_, err := bossSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "echo__greet"},
		Argument: mcp.CompleteParamsArgument{Name: "name", Value: "A"},
	})
	assertAcceptRPCError(t, err, jsonrpc.CodeInvalidParams)

	g.acceptPins("echo")
	g.acceptPins("bad")
	g.acceptPins("poison")

	echoURI := resourceURIOf(t, g, "echo")

	// Happy path prompt and resource completion.
	resPrompt, err := bossSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "echo__greet"},
		Argument: mcp.CompleteParamsArgument{Name: "name", Value: "A"},
	})
	if err != nil {
		t.Fatalf("prompt completion: %v", err)
	}
	if !slices.Equal(resPrompt.Completion.Values, []string{"Ada", "Alan"}) {
		t.Fatalf("prompt completion values = %v, want [Ada Alan]", resPrompt.Completion.Values)
	}

	resResource, err := bossSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/resource", URI: echoURI},
		Argument: mcp.CompleteParamsArgument{Name: "id", Value: "n"},
	})
	if err != nil {
		t.Fatalf("resource completion: %v", err)
	}
	if !slices.Equal(resResource.Completion.Values, []string{"note-alpha", "note-beta"}) {
		t.Fatalf("resource completion values = %v, want [note-alpha note-beta]", resResource.Completion.Values)
	}

	adaSess := g.connect(adaToken)

	// Policy deny blocks role:member before upstream.
	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{
		"server":  "echo",
		"tool":    "greet",
		"subject": "role:member",
		"action":  "deny",
	}, http.StatusCreated)
	_, err = adaSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "echo__greet"},
		Argument: mcp.CompleteParamsArgument{Name: "name", Value: "A"},
	})
	assertAcceptRPCError(t, err, jsonrpc.CodeInvalidRequest)
	if !strings.Contains(err.Error(), "Denied") {
		t.Fatalf("policy deny err = %v, want Denied", err)
	}

	// Policy ask fails closed for completion/complete without creating approvals.
	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{
		"server":  "echo",
		"tool":    "echo://note",
		"subject": "role:member",
		"action":  "ask",
	}, http.StatusCreated)
	_, err = adaSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/resource", URI: echoURI},
		Argument: mcp.CompleteParamsArgument{Name: "id", Value: "n"},
	})
	assertAcceptRPCError(t, err, jsonrpc.CodeInvalidRequest)
	if !strings.Contains(err.Error(), "Denied") {
		t.Fatalf("policy ask fail-closed err = %v, want Denied", err)
	}
	if approvals := g.pendingApprovals(g.owner); len(approvals) != 0 {
		t.Fatalf("completion ask created pending approvals: %v", approvals)
	}

	// Team hiding returns -32602 (anti-probing) for caller outside the server's team.
	if code, out := boss.app(http.MethodPost, "/v1/teams", map[string]any{"name": "Engineering", "slug": "eng"}, nil); code != http.StatusCreated {
		t.Fatalf("create team: %d %v", code, out)
	}
	if code, out := boss.app(http.MethodPost, "/v1/teams/eng/members", map[string]any{"person": "boss"}, nil); code != http.StatusOK {
		t.Fatalf("add boss to eng: %d %v", code, out)
	}
	if code, out := boss.app(http.MethodPatch, "/v1/servers/echo", map[string]any{"teams": []string{"eng"}}, nil); code != http.StatusOK {
		t.Fatalf("scope echo to eng: %d %v", code, out)
	}
	g.eventually("ada no longer sees echo__echo", 8*time.Second, func() bool {
		return !slices.Contains(toolNames(t, adaSess), "echo__echo")
	})
	_, err = adaSess.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "echo__greet"},
		Argument: mcp.CompleteParamsArgument{Name: "name", Value: "A"},
	})
	assertAcceptRPCError(t, err, jsonrpc.CodeInvalidParams)
	// Restore echo to everyone so assertServing succeeds at the end.
	if code, out := boss.app(http.MethodPatch, "/v1/servers/echo", map[string]any{"teams": []string{"everyone"}}, nil); code != http.StatusOK {
		t.Fatalf("restore echo to everyone: %d %v", code, out)
	}

	// Malformed completion reply (values: null) is refused (-32603) and audited "error".
	result, rpcErr := g.rpcTry(newProtocol, g.agent, "completion/complete", map[string]any{
		"ref":      map[string]any{"type": "ref/prompt", "name": "bad__greet"},
		"argument": map[string]any{"name": "name", "value": "A"},
	})
	if rpcErr == nil {
		t.Fatalf("malformed completion was relayed: %v", result)
	}
	if c, _ := rpcErr["code"].(float64); c != -32603 || !strings.Contains(rpcMessage(rpcErr), "malformed") {
		t.Fatalf("malformed completion rpcErr = %v, want code -32603 malformed", rpcErr)
	}

	// Reserved _meta keys are stripped while upstream's own _meta survives.
	poisonRes := g.rpcAt(newProtocol, g.agent, "completion/complete", map[string]any{
		"ref":      map[string]any{"type": "ref/prompt", "name": "poison__greet"},
		"argument": map[string]any{"name": "name", "value": "A"},
	})
	if poisonRes["resultType"] != "complete" {
		t.Errorf("poison completion resultType = %v, want complete", poisonRes["resultType"])
	}
	meta, _ := poisonRes["_meta"].(map[string]any)
	if meta["com.example/keep"] != "yes" {
		t.Errorf("_meta = %v, custom key dropped", meta)
	}
	if _, bad := meta["io.modelcontextprotocol/related-task"]; bad {
		t.Errorf("_meta = %v, reserved related-task relayed", meta)
	}
	if info, _ := meta["io.modelcontextprotocol/serverInfo"].(map[string]any); info == nil || info["name"] != "privo" {
		t.Errorf("serverInfo = %v, want gateway serverInfo", info)
	}

	// Verify completion/complete audit events and audit chain integrity.
	g.eventually("completion/complete audit records settle", 5*time.Second, func() bool {
		var sawOK, sawDeny, sawAsk, sawHidden, sawErr bool
		for _, ev := range auditEvents(g) {
			if ev["action"] != "completion/complete" {
				continue
			}
			out, _ := ev["outcome"].(string)
			dec, _ := ev["decision"].(string)
			switch {
			case ev["server"] == "echo" && ev["item"] == "greet" && out == "ok":
				sawOK = true
			case ev["server"] == "echo" && ev["item"] == "greet" && dec == "deny" && ev["user"] == "ada":
				sawDeny = true
			case ev["server"] == "echo" && ev["item"] == "echo://note" && dec == "ask" && strings.HasPrefix(out, "denied:"):
				sawAsk = true
			case ev["server"] == "echo" && ev["item"] == "greet" && dec == "denied:not_in_team":
				sawHidden = true
			case ev["server"] == "bad" && ev["item"] == "greet" && out == "error":
				sawErr = true
			}
		}
		return sawOK && sawDeny && sawAsk && sawHidden && sawErr
	})

	integrityCode, integrity := boss.app(http.MethodGet, "/v1/audit/integrity", nil, nil)
	rep, _ := integrity["report"].(map[string]any)
	if integrityCode != http.StatusOK || rep["ok"] != true {
		t.Fatalf("audit integrity = %d %v, want ok=true", integrityCode, integrity)
	}

	assertServing(t, g)
	assertNoPanic(t, g)
}

func assertAcceptRPCError(t *testing.T, err error, wantCode int64) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected JSON-RPC error %d, got nil", wantCode)
	}
	var rpc *jsonrpc.Error
	if !errors.As(err, &rpc) || rpc.Code != wantCode {
		t.Fatalf("error = %#v, want JSON-RPC code %d", err, wantCode)
	}
}
