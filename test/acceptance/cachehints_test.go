//go:build acceptance

package acceptance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// Protocol versions the raw-JSON tests speak. From 2026-07-28 the version
// travels in every request's _meta; before that there was an initialize
// handshake, which a stateless gateway does not need to see.
const (
	legacyProtocol = "2025-06-18"
	newProtocol    = "2026-07-28"
)

// rpc sends one JSON-RPC request to /mcp as a plain HTTP client speaking the
// legacy protocol and returns the decoded "result" object, without any SDK in
// between.
func (g *gateway) rpc(token, method string, params map[string]any) map[string]any {
	g.t.Helper()
	return g.rpcAt(legacyProtocol, token, method, params)
}

// rpcAt is rpc for a given protocol version. Strict clients such as Claude Code
// validate the raw JSON, so the wire format is what matters.
func (g *gateway) rpcAt(version, token, method string, params map[string]any) map[string]any {
	g.t.Helper()
	if version >= newProtocol {
		withMeta := map[string]any{}
		for k, v := range params {
			withMeta[k] = v
		}
		withMeta["_meta"] = map[string]any{
			"io.modelcontextprotocol/protocolVersion":    version,
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "privo-acceptance-raw", "version": "0.0.0"},
		}
		params = withMeta
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	if err != nil {
		g.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.base+"/mcp", bytes.NewReader(body))
	if err != nil {
		g.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", version)
	if version >= newProtocol {
		// 2026-07-28 mirrors the method, and the name of what is called, in
		// headers so proxies can route without reading the body.
		req.Header.Set("Mcp-Method", method)
		switch method {
		case "tools/call", "prompts/get":
			req.Header.Set("Mcp-Name", params["name"].(string))
		case "resources/read":
			req.Header.Set("Mcp-Name", params["uri"].(string))
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		g.t.Fatalf("%s: status %d: %s", method, resp.StatusCode, raw)
	}
	payload := raw
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		payload = nil
		sc := bufio.NewScanner(bytes.NewReader(raw))
		sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
				payload = []byte(strings.TrimSpace(data))
				break
			}
		}
	}
	var msg struct {
		Result map[string]any `json:"result"`
		Error  any            `json:"error"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		g.t.Fatalf("%s: decode %q: %v", method, payload, err)
	}
	if msg.Error != nil || msg.Result == nil {
		g.t.Fatalf("%s: error %v, result %v", method, msg.Error, msg.Result)
	}
	return msg.Result
}

// Found with Claude Code on 2026-10-29: its schema requires cacheScope to be
// "public" or "private", and an empty value made it drop the whole tool list
// ("Connected, tools fetch failed"). Our Go client does not check the field, so
// only a raw-JSON test sees it. Results differ per person, so they are private
// (ADR 0001); lists stay fresh for a few seconds, reads are never reused.
func TestListAndReadResultsCarryValidCacheHints(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("echo", nil, nil)
	g.acceptPins("echo")
	g.eventually("echo tools listed", 5*time.Second, func() bool {
		tools, _ := g.rpc(g.agent, "tools/list", map[string]any{})["tools"].([]any)
		for _, tl := range tools {
			if m, _ := tl.(map[string]any); m["name"] == "echo__echo" {
				return true
			}
		}
		return false
	})

	check := func(method string, result map[string]any, wantTTL float64) {
		t.Helper()
		if scope, _ := result["cacheScope"].(string); scope != "private" {
			t.Errorf("%s: cacheScope = %#v, want \"private\"", method, result["cacheScope"])
		}
		ttl, ok := result["ttlMs"].(float64)
		if !ok || ttl != wantTTL {
			t.Errorf("%s: ttlMs = %#v, want %v", method, result["ttlMs"], wantTTL)
		}
	}
	for _, version := range []string{legacyProtocol, newProtocol} {
		for _, method := range []string{"tools/list", "prompts/list", "resources/list", "resources/templates/list"} {
			check(version+" "+method, g.rpcAt(version, g.agent, method, map[string]any{}), 5000)
		}
	}

	listed, _ := g.rpc(g.agent, "resources/list", map[string]any{})["resources"].([]any)
	if len(listed) == 0 {
		t.Fatal("no resources listed")
	}
	uri, _ := listed[0].(map[string]any)["uri"].(string)
	if !slices.ContainsFunc([]string{uri}, func(u string) bool { return strings.HasPrefix(u, "privo://echo/") }) {
		t.Fatalf("resource uri %q", uri)
	}
	for _, version := range []string{legacyProtocol, newProtocol} {
		check(version+" resources/read", g.rpcAt(version, g.agent, "resources/read", map[string]any{"uri": uri}), 0)
	}
}
