//go:build acceptance

// Package acceptance runs black-box tests against the built privo binary.
//
// The six scenarios are ported from the TypeScript prototype and updated to the
// accepted ADRs (secrets encrypted at rest, per-tool pins reviewed before use,
// SSRF protection, versioned admin API). They define the contract that feature
// F02 must satisfy; see docs/todo.md.
//
// Contract assumed by these tests (to be documented in docs/api when built):
//
//	privo token create --name N --role owner   prints a personal token (break-glass, self-hosted)
//	POST   /api/v1/servers                     register a server
//	GET    /api/v1/servers                     list servers with status
//	PATCH  /api/v1/servers/{alias}             {"enabled": bool}
//	DELETE /api/v1/servers/{alias}
//	POST   /api/v1/servers/{alias}/pins:accept accept every pending pin (review UI shortcut)
//	POST   /api/v1/tokens, GET /api/v1/tokens, DELETE /api/v1/tokens/{id}
//	GET    /api/v1/audit/events[?kind=admin]
//	PATCH  /api/v1/settings/audit              {"record_arguments": bool, "record_results": bool}
//	POST   /mcp                                MCP Streamable HTTP endpoint
package acceptance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	privoBin string
	echoBin  string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "privo-acceptance-")
	if err != nil {
		panic(err)
	}
	privoBin = filepath.Join(dir, "privo")
	echoBin = filepath.Join(dir, "echoserver")
	for _, b := range [][2]string{{privoBin, "../../cmd/privo"}, {echoBin, "./testdata/echoserver"}} {
		args := []string{"build", "-tags", "acceptance", "-o", b[0]}
		if os.Getenv("PRIVO_ACCEPTANCE_RACE") == "1" {
			args = append(args, "-race") // build the gateway with the race detector
		}
		cmd := exec.Command("go", append(args, b[1])...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n", b[1], err)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type gateway struct {
	t       *testing.T
	base    string
	dataDir string
	env     []string
	owner   string // control token: admin API
	agent   string // gateway token for the same person: MCP
	cmd     *exec.Cmd
	logs    *bytes.Buffer
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// startGateway boots privo with a fresh data directory and an owner token.
func startGateway(t *testing.T, extraEnv ...string) *gateway {
	t.Helper()
	g := newGateway(t, extraEnv...)
	g.issueTokens()
	g.start()
	return g
}

// newGateway prepares a fresh data directory and environment without
// creating tokens or starting the server.
func newGateway(t *testing.T, extraEnv ...string) *gateway {
	t.Helper()
	dir := t.TempDir()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	keyFile := filepath.Join(dir, "master.key")
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	g := &gateway{
		t:       t,
		base:    fmt.Sprintf("http://127.0.0.1:%d", port),
		dataDir: filepath.Join(dir, "data"),
		logs:    &bytes.Buffer{},
	}
	g.env = append(os.Environ(),
		fmt.Sprintf("PRIVO_LISTEN=127.0.0.1:%d", port),
		"PRIVO_DATA_DIR="+g.dataDir,
		"PRIVO_MASTER_KEY_FILE="+keyFile,
		"PRIVO_PUBLIC_URL="+g.base,
		"PRIVO_RUNNER=local", // in-process runner, development and tests only
		"PRIVO_INSECURE_LOCAL_RUNNER=i-understand",
		"PRIVO_UPSTREAM_RETRY_BASE=80ms",
		"PRIVO_UPSTREAM_RETRY_MAX=160ms",
		"PRIVO_LOG_FORMAT=text",
		"PRIVO_APPROVAL_HOLD=300ms", // tests that wait for a decision override this
	)
	g.env = append(g.env, extraEnv...)
	t.Cleanup(g.stop)
	return g
}

// issueTokens creates the owner control token and the owner's gateway token
// with the command line (break-glass), before the server starts.
func (g *gateway) issueTokens() {
	g.t.Helper()
	out, err := g.command("token", "create", "--name", "owner", "--role", "owner", "--kind", "control").Output()
	if err != nil {
		g.t.Fatalf("token create: %v", err)
	}
	g.owner = strings.TrimSpace(string(out))
	if !strings.HasPrefix(g.owner, "privo_pat_") {
		g.t.Fatalf("owner token %q does not use the privo_pat_ format (ADR 0003)", g.owner)
	}
	out, err = g.command("token", "create", "--name", "owner-agent", "--role", "owner", "--kind", "gateway", "--person", "owner").Output()
	if err != nil {
		g.t.Fatalf("token create (gateway): %v", err)
	}
	g.agent = strings.TrimSpace(string(out))
}

// start runs the server and waits until it is ready. It can be called again
// after stop to restart on the same data directory.
func (g *gateway) start() {
	g.t.Helper()
	g.cmd = g.command("serve")
	g.cmd.Stdout, g.cmd.Stderr = g.logs, g.logs
	if err := g.cmd.Start(); err != nil {
		g.t.Fatal(err)
	}
	g.eventually("gateway ready", 10*time.Second, func() bool {
		code, _ := g.raw(http.MethodGet, "/readyz", "", nil)
		return code == http.StatusOK
	})
}

func (g *gateway) command(args ...string) *exec.Cmd {
	cmd := exec.Command(privoBin, args...)
	cmd.Env = g.env
	return cmd
}

func (g *gateway) stop() {
	if g.cmd == nil || g.cmd.Process == nil {
		return
	}
	cmd := g.cmd
	g.cmd = nil // a second stop (explicit, then cleanup) is a no-op
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			g.t.Errorf("gateway exited with %v after SIGTERM, want a clean exit", err)
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		g.t.Errorf("gateway did not stop within 15s")
	}
	if g.t.Failed() {
		g.t.Logf("gateway logs:\n%s", g.logs.String())
	}
}

// raw sends a request and returns the status and body.
func (g *gateway) raw(method, path, token string, body any) (int, []byte) {
	g.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			g.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, r)
	if err != nil {
		g.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// api sends a request, checks the status and decodes a JSON object.
func (g *gateway) api(method, path, token string, body any, want int) map[string]any {
	g.t.Helper()
	code, b := g.raw(method, path, token, body)
	if code != want {
		g.t.Fatalf("%s %s: status %d, want %d: %s", method, path, code, want, b)
	}
	out := map[string]any{}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &out); err != nil {
			g.t.Fatalf("%s %s: decode %q: %v", method, path, b, err)
		}
	}
	return out
}

func (g *gateway) eventually(what string, within time.Duration, ok func() bool) {
	g.t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			g.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// connect opens an MCP client session to the gateway as the holder of token.
func (g *gateway) connect(token string) *mcp.ClientSession {
	g.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "privo-acceptance", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             g.base + "/mcp",
		HTTPClient:           &http.Client{Transport: bearer{token, http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		g.t.Fatalf("connect: %v", err)
	}
	g.t.Cleanup(func() { _ = session.Close() })
	return session
}

func digest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// registerEcho registers the test server as a pinned local executable (ADR 0007).
func (g *gateway) registerEcho(alias string, args []string, env map[string]string) map[string]any {
	g.t.Helper()
	return g.api(http.MethodPost, "/api/v1/servers", g.owner, map[string]any{
		"alias":     alias,
		"transport": "stdio",
		"command":   map[string]any{"path": echoBin, "sha256": digest(g.t, echoBin), "args": args},
		"env":       env,
	}, http.StatusCreated)
}

// acceptPins reviews a server's pending definitions and accepts exactly those.
func (g *gateway) acceptPins(alias string) {
	g.t.Helper()
	listed := g.api(http.MethodGet, "/api/v1/servers/"+alias+"/pins", g.owner, nil, http.StatusOK)
	raw, _ := listed["pins"].([]any)
	var pending []map[string]any
	for _, p := range raw {
		m, _ := p.(map[string]any)
		if m["status"] == "pending" {
			pending = append(pending, map[string]any{"kind": m["kind"], "name": m["name"], "hash": m["hash"]})
		}
	}
	if len(pending) == 0 {
		g.t.Fatalf("server %s has nothing awaiting review", alias)
	}
	res := g.api(http.MethodPost, "/api/v1/servers/"+alias+"/pins:accept", g.owner, map[string]any{"pins": pending}, http.StatusOK)
	if res["accepted"] != float64(len(pending)) {
		g.t.Fatalf("accepted %v of %d pins", res["accepted"], len(pending))
	}
}

func (g *gateway) serverStatus(alias string) string {
	g.t.Helper()
	list := g.api(http.MethodGet, "/api/v1/servers", g.owner, nil, http.StatusOK)
	servers, _ := list["servers"].([]any)
	for _, s := range servers {
		m, _ := s.(map[string]any)
		if m["alias"] == alias {
			status, _ := m["status"].(string)
			return status
		}
	}
	return ""
}

// assertNotOnDisk fails if needle appears in plain text anywhere under the data directory.
func (g *gateway) assertNotOnDisk(needle string) {
	g.t.Helper()
	err := filepath.WalkDir(g.dataDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, []byte(needle)) {
			g.t.Errorf("%q found in plain text in %s", needle, path)
		}
		return nil
	})
	if err != nil {
		g.t.Fatalf("scanning data directory: %v", err)
	}
}

func toolNames(t *testing.T, s *mcp.ClientSession) []string {
	t.Helper()
	res, err := s.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// callEcho calls echo__echo through the gateway and returns its text.
func callEcho(t *testing.T, s *mcp.ClientSession, input string) string {
	t.Helper()
	const name = "echo__echo"
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"text": input}})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("CallTool %s: error result %+v", name, res)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("CallTool %s: first content is %T", name, res.Content[0])
	}
	return text.Text
}

// call invokes a tool and returns its text and whether it was an error result.
func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if len(res.Content) == 0 {
		return "", res.IsError
	}
	text, _ := res.Content[0].(*mcp.TextContent)
	if text == nil {
		return "", res.IsError
	}
	return text.Text, res.IsError
}

// issue creates a gateway token (for an AI client) and returns its secret.
func (g *gateway) issue(name, role string) string {
	g.t.Helper()
	res := g.api(http.MethodPost, "/api/v1/tokens", g.owner, map[string]any{"name": name, "role": role, "kind": "gateway"}, http.StatusCreated)
	tok, _ := res["token"].(string)
	return tok
}

// issueControl creates a control token (for a person on the admin API).
func (g *gateway) issueControl(name, role, person string) string {
	g.t.Helper()
	res := g.api(http.MethodPost, "/api/v1/tokens", g.owner, map[string]any{"name": name, "role": role, "kind": "control", "person": person}, http.StatusCreated)
	tok, _ := res["token"].(string)
	return tok
}

// pendingApprovals lists pending approvals as seen by an approver.
func (g *gateway) pendingApprovals(token string) []map[string]any {
	g.t.Helper()
	res := g.api(http.MethodGet, "/api/v1/approvals?status=pending", token, nil, http.StatusOK)
	raw, _ := res["approvals"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, a := range raw {
		if m, ok := a.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
