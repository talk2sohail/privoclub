package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/grants"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
	"privo.club/privo/internal/upstream"
)

// ADR 0024: every call that reaches the decision stage leaves one statistic.

type statStore struct {
	Store
	def string // the policy default
}

func (s *statStore) ListPolicies(context.Context, store.OrgID) ([]store.Policy, error) {
	return nil, nil
}
func (s *statStore) Setting(_ context.Context, _ store.OrgID, key, def string) (string, error) {
	if key == SettingPolicyDefault {
		return s.def, nil
	}
	return def, nil
}
func (s *statStore) AccessFor(context.Context, store.OrgID, string) (store.Access, error) {
	return store.Access{Teams: []string{store.TeamEveryone}}, nil
}

type memSink struct {
	mu     sync.Mutex
	stats  []store.CallStat
	diags  []store.Diagnostic
	audits []store.AuditEvent
	fail   bool
	slow   time.Duration // each audit write takes this long
}

func (m *memSink) InsertCallStats(_ context.Context, _ store.OrgID, rows ...store.CallStat) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stats = append(m.stats, rows...)
	return nil
}

func (m *memSink) InsertDiagnostics(_ context.Context, _ store.OrgID, rows ...store.Diagnostic) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.diags = append(m.diags, rows...)
	return nil
}

func (m *memSink) InsertAudit(_ context.Context, _ store.OrgID, events ...store.AuditEvent) error {
	time.Sleep(m.slow)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("disk full")
	}
	m.audits = append(m.audits, events...)
	return nil
}

// statGateway runs one call through run with the policy default def and
// returns the statistic it left.
func statGateway(t *testing.T, def string, auditFails bool) (store.CallStat, *memSink, error) {
	t.Helper()
	sink := &memSink{fail: auditFails, slow: 3 * time.Millisecond}
	rec := telemetry.New(sink, telemetry.Options{})
	aw := audit.NewWriter(sink, audit.Options{})
	mgr := upstream.New(upstream.Options{})
	g := &Gateway{Store: &statStore{def: def}, Upstream: mgr, Audit: aw, Telemetry: rec, Logger: slog.New(slog.DiscardHandler)}
	c := call{p: auth.Principal{OrgID: "o", TokenID: "tok", UserID: "u-ada", Client: "claude", Role: store.RoleMember},
		item: upstream.Item{Alias: "github", ServerID: "srv-1", Name: "search"}, action: "tools/call", args: []byte(`{"q":"x"}`)}
	err := g.run(context.Background(), c, func(context.Context, *mcp.ClientSession) (string, any, error) {
		t.Fatal("the upstream was called")
		return "", nil, nil
	})
	rec.Close()
	aw.Close()
	mgr.Close()
	if len(sink.stats) != 1 {
		t.Fatalf("%d statistics, want 1", len(sink.stats))
	}
	s := sink.stats[0]
	if s.Server != "github" || s.Item != "search" || s.Method != "tools/call" || s.UserID != "u-ada" || s.TokenID != "tok" ||
		s.Client != "claude" || s.ArgsBytes != int64(len(`{"q":"x"}`)) || s.ID == "" || s.Time.IsZero() {
		t.Fatalf("who and what: %+v", s)
	}
	if s.TotalUS < s.DecideUS+s.AuditUS+s.ConnectUS+s.UpstreamUS {
		t.Fatalf("the phases add up to more than the total: %+v", s)
	}
	return s, sink, err
}

func TestRefusedCallsAreCounted(t *testing.T) {
	s, sink, err := statGateway(t, store.ActionDeny, false)
	var r *refusal
	if !errors.As(err, &r) {
		t.Fatalf("err %v, want a refusal", err)
	}
	if s.Outcome != store.OutcomeRefused || s.ErrorClass != "denied" || s.AuditUS != 0 || s.ConnectUS != 0 {
		t.Fatalf("refused call: %+v", s)
	}
	if len(sink.audits) != 2 {
		t.Fatalf("the audit of a refusal changed: %d events", len(sink.audits))
	}
}

func TestCallStatisticsRecordPhases(t *testing.T) {
	// No upstream connection: the call runs through decide and audit and fails
	// to get a session.
	s, sink, err := statGateway(t, store.ActionAllow, false)
	if !errors.Is(err, upstream.ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
	if s.Outcome != store.OutcomeError || s.ErrorClass != "unavailable" || s.AuditUS < 3000 || s.UpstreamUS != 0 || s.ResultBytes != 0 {
		t.Fatalf("statistic: %+v", s)
	}
	if len(sink.audits) != 2 || sink.audits[0].Phase != "intent" || sink.audits[1].Phase != "completion" || sink.audits[0].CallID != s.ID {
		t.Fatalf("the audit and the statistic share the call id: %+v", sink.audits)
	}
}

func TestAuditFailureIsCountedAndLogged(t *testing.T) {
	s, sink, err := statGateway(t, store.ActionAllow, true)
	if err == nil {
		t.Fatal("a call ran without its audit record")
	}
	if s.Outcome != store.OutcomeError || s.ErrorClass != "audit_unavailable" {
		t.Fatalf("statistic: %+v", s)
	}
	if len(sink.diags) != 1 || sink.diags[0].Category != store.CategoryAudit || sink.diags[0].Level != store.LevelError {
		t.Fatalf("diagnostics: %+v", sink.diags)
	}
}

func TestErrorClasses(t *testing.T) {
	g := &Gateway{BaseURL: "https://privo.example"}
	for want, err := range map[string]error{
		"":               nil,
		"not_connected":  g.connectFirst("github"),
		"no_account":     g.noAccount("github"),
		"key_refused":    g.keyRefused("github"),
		"timeout":        fmt.Errorf("%w (limit)", errCallTimeout),
		"unavailable":    upstream.ErrUnavailable,
		"malformed":      fmt.Errorf("%w: content 2", errMalformedReply),
		"upstream_error": &jsonrpc.Error{Code: -32602, Message: "bad"},
		"transport":      &jsonrpc.Error{Code: -32005, Message: "Post https://x?key=secret: refused"},
	} {
		if got := errorClass(err); got != want {
			t.Errorf("errorClass(%v) = %q, want %q", err, got, want)
		}
	}
	if got := errorClass(fmt.Errorf("wrapped: %w", grants.ErrKeyRefused)); got != "key_refused" {
		t.Errorf("a raw key refusal is %q", got)
	}
	// The class never carries the error's text.
	if got := errorClass(errors.New("Post https://x?key=secret: refused")); got != "transport" {
		t.Errorf("an unknown error is %q", got)
	}
}

func TestEncodedSizeIsTheRelayedJSON(t *testing.T) {
	res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hello <world> & ünïcode"}}}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if got := encodedSize(res); got != int64(len(b)) {
		t.Fatalf("encodedSize %d, json.Marshal %d", got, len(b))
	}
}
