package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/ids"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func TestOpenMigratesAndSecuresFile(t *testing.T) {
	s, dir := open(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"privo.db", "privo.db-wal", "privo.db-shm"} {
		info, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s mode %o, want 600", f, perm)
		}
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Fatalf("data dir mode %o, want 700", info.Mode().Perm())
	}
}

func TestEveryTableCarriesOrgID(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	rows, err := s.r.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'goose_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	_ = rows.Close()
	exempt := map[string]bool{"orgs": true, "leases": true} // documented in the migration and store.go
	for _, table := range tables {
		if exempt[table] {
			continue
		}
		var n int
		if err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name = 'org_id'`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s has no org_id column (ADR 0002)", table)
		}
	}
	if len(tables) < 8 {
		t.Fatalf("only %d tables found: %v", len(tables), tables)
	}
}

func TestLeasePreventsSecondWriter(t *testing.T) {
	s, dir := open(t)
	if _, err := Open(context.Background(), dir); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second Open: err = %v, want ErrLeaseHeld", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	_ = s2.Close()
}

func twoOrgs(t *testing.T, s *Store) (OrgID, OrgID) {
	t.Helper()
	ctx := context.Background()
	a, err := s.EnsureOrg(ctx, "a", "A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.EnsureOrg(ctx, "b", "B")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := s.EnsureOrg(ctx, "a", "ignored")
	if again.ID != a.ID {
		t.Fatal("EnsureOrg created a duplicate")
	}
	return a.ID, b.ID
}

func TestServersAreIsolatedPerOrg(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, b := twoOrgs(t, s)
	srv := Server{OrgID: a, ID: ids.New(), Alias: "echo", Transport: "stdio", Enabled: true, CreatedAt: time.Now(),
		Config: ServerConfig{Path: "/bin/echo", SHA256: "abc", EnvNames: []string{"SECRET"}}}
	cred := Credential{ID: ids.New(), Purpose: "env:SECRET", KeyVersion: 1, RowVersion: 1, Ciphertext: []byte{1, 2, 3}}
	if err := s.CreateServer(ctx, srv, []Credential{cred}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateServer(ctx, Server{OrgID: a, ID: ids.New(), Alias: "echo", Transport: "stdio", CreatedAt: time.Now()}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate alias: err = %v, want ErrConflict", err)
	}
	// Same alias in another org is fine.
	if err := s.CreateServer(ctx, Server{OrgID: b, ID: ids.New(), Alias: "echo", Transport: "stdio", CreatedAt: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}

	if list, _ := s.ListServers(ctx, b); len(list) != 1 || list[0].ID == srv.ID {
		t.Fatalf("org b sees %+v", list)
	}
	if _, err := s.ServerByAlias(ctx, b, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing server not reported")
	}
	if creds, _ := s.ServerCredentials(ctx, b, srv.ID); len(creds) != 0 {
		t.Fatal("org b read org a's credentials")
	}
	if err := s.SetServerEnabled(ctx, b, srv.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("org b changed org a's server")
	}
	if err := s.DeleteServer(ctx, b, srv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("org b deleted org a's server")
	}

	got, err := s.ServerByAlias(ctx, a, "echo")
	if err != nil || got.Config.Path != "/bin/echo" || !got.Enabled {
		t.Fatalf("got %+v %v", got, err)
	}
	creds, _ := s.ServerCredentials(ctx, a, srv.ID)
	if len(creds) != 1 || string(creds[0].Ciphertext) != "\x01\x02\x03" {
		t.Fatalf("creds %+v", creds)
	}
	if err := s.SetServerEnabled(ctx, a, srv.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ServerByAlias(ctx, a, "echo"); got.Enabled {
		t.Fatal("server still enabled")
	}
	if err := s.DeleteServer(ctx, a, srv.ID); err != nil {
		t.Fatal(err)
	}
	if creds, _ := s.ServerCredentials(ctx, a, srv.ID); len(creds) != 0 {
		t.Fatal("credentials outlived their server")
	}
}

func TestTokens(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, b := twoOrgs(t, s)
	tok := APIToken{OrgID: a, ID: "tid1", Name: "ada", Role: RoleMember, SecretHash: "h", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.CreateToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	dup := tok
	dup.ID = "tid2"
	if err := s.CreateToken(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate active name: %v", err)
	}
	got, err := s.TokenByID(ctx, "tid1")
	if err != nil || got.OrgID != a || got.Role != RoleMember || got.RevokedAt != nil {
		t.Fatalf("got %+v %v", got, err)
	}
	if err := s.RevokeToken(ctx, b, "tid1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("org b revoked org a's token")
	}
	if err := s.RevokeToken(ctx, a, "tid1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.TokenByID(ctx, "tid1"); got.RevokedAt == nil {
		t.Fatal("token not revoked")
	}
	if list, _ := s.ListTokens(ctx, a); len(list) != 0 {
		t.Fatal("revoked token still listed")
	}
	if err := s.CreateToken(ctx, dup); err != nil {
		t.Fatalf("name reuse after revoke: %v", err)
	}
}

func TestPins(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, b := twoOrgs(t, s)
	p := Pin{ServerID: "srv", Kind: "tool", Name: "echo", Hash: "h1", Definition: "{}"}
	if err := s.RecordSeen(ctx, a, []Pin{p, p}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.PinStatuses(ctx, a, "srv")
	if st[PinKey{"tool", "echo", "h1"}] != PinPending {
		t.Fatalf("statuses %v", st)
	}
	key := PinKey{"tool", "echo", "h1"}
	if got, _ := s.AcceptPins(ctx, b, "srv", []PinKey{key}); len(got) != 0 {
		t.Fatal("org b accepted org a's pins")
	}
	if got, _ := s.AcceptPins(ctx, a, "srv", []PinKey{{"tool", "echo", "other-hash"}}); len(got) != 0 {
		t.Fatal("a pin with an unreviewed hash was accepted")
	}
	if got, _ := s.AcceptPins(ctx, a, "srv", []PinKey{key}); len(got) != 1 {
		t.Fatalf("accepted %v", got)
	}
	if list, _ := s.ListPins(ctx, a, "srv"); len(list) != 1 || list[0].Status != PinAccepted || list[0].Definition != "{}" {
		t.Fatalf("ListPins %+v", list)
	}
	changed := p
	changed.Hash = "h2"
	_ = s.RecordSeen(ctx, a, []Pin{p, changed})
	st, _ = s.PinStatuses(ctx, a, "srv")
	if st[PinKey{"tool", "echo", "h1"}] != PinAccepted || st[PinKey{"tool", "echo", "h2"}] != PinPending {
		t.Fatalf("a changed definition must be pending; statuses %v", st)
	}
}

func TestAuditAndSettings(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, b := twoOrgs(t, s)
	now := time.Now()
	call := ids.New()
	if err := s.InsertAudit(ctx, a, AuditEvent{ID: ids.New(), CallID: call, Phase: "intent", Time: now, Actor: "ada", Server: "echo", Item: "echo", Action: "tools/call", Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	calls, _ := s.RecentCalls(ctx, a, 10)
	if len(calls) != 1 || calls[0].Completion != nil {
		t.Fatalf("calls %+v", calls)
	}
	_ = s.InsertAudit(ctx, a, AuditEvent{ID: ids.New(), CallID: call, Phase: "completion", Time: now.Add(time.Millisecond), Actor: "ada", Outcome: "ok", LatencyMS: 3})
	calls, _ = s.RecentCalls(ctx, a, 10)
	if len(calls) != 1 || calls[0].Completion == nil || calls[0].Completion.Outcome != "ok" {
		t.Fatalf("calls %+v", calls)
	}
	if other, _ := s.RecentCalls(ctx, b, 10); len(other) != 0 {
		t.Fatal("org b sees org a's audit")
	}

	if v, _ := s.Setting(ctx, a, "audit.record_arguments", "false"); v != "false" {
		t.Fatalf("default %q", v)
	}
	_ = s.SetSetting(ctx, a, "audit.record_arguments", "true")
	_ = s.SetSetting(ctx, a, "audit.record_arguments", "true")
	if v, _ := s.Setting(ctx, a, "audit.record_arguments", "false"); v != "true" {
		t.Fatalf("value %q", v)
	}
	if v, _ := s.Setting(ctx, b, "audit.record_arguments", "false"); v != "false" {
		t.Fatal("setting leaked across orgs")
	}
}

func TestOrgKeys(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, _ := twoOrgs(t, s)
	if _, err := s.CurrentOrgKey(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Fatal("expected no key")
	}
	k := OrgKey{Version: 1, Provider: "local", KeyRef: "master", WrappedDEK: []byte("w")}
	if err := s.InsertOrgKey(ctx, a, k); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertOrgKey(ctx, a, k); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate key version accepted")
	}
	got, err := s.CurrentOrgKey(ctx, a)
	if err != nil || got.Provider != "local" || !strings.EqualFold(string(got.WrappedDEK), "w") {
		t.Fatalf("got %+v %v", got, err)
	}
}

// F11: a reviewer blocks a definition; it leaves review, stays a decision,
// and can still be accepted later.
func TestBlockPins(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	org, _ := s.EnsureOrg(ctx, "a", "A")
	other, _ := s.EnsureOrg(ctx, "b", "B")
	p := Pin{ServerID: "srv", Kind: "tool", Name: "echo", Hash: "h1", Definition: "{}"}
	if err := s.RecordSeen(ctx, org.ID, []Pin{p}); err != nil {
		t.Fatal(err)
	}
	key := PinKey{"tool", "echo", "h1"}
	if got, _ := s.BlockPins(ctx, other.ID, "srv", []PinKey{key}); len(got) != 0 {
		t.Fatal("blocked another org's pin")
	}
	if got, err := s.BlockPins(ctx, org.ID, "srv", []PinKey{key, {"tool", "echo", "nope"}}); err != nil || len(got) != 1 {
		t.Fatalf("BlockPins = %v, %v", got, err)
	}
	if st, _ := s.PinStatuses(ctx, org.ID, "srv"); st[key] != PinBlocked {
		t.Fatalf("status %q", st[key])
	}
	if n, _ := s.PrunePending(ctx, org.ID, "srv", nil); n != 0 {
		t.Fatal("pruning deleted a blocked pin")
	}
	if got, _ := s.BlockPins(ctx, org.ID, "srv", []PinKey{key}); len(got) != 0 {
		t.Fatal("blocked twice")
	}
	if got, _ := s.AcceptPins(ctx, org.ID, "srv", []PinKey{key}); len(got) != 1 {
		t.Fatal("a blocked pin cannot be accepted")
	}
	if got, _ := s.BlockPins(ctx, org.ID, "srv", []PinKey{key}); len(got) != 0 {
		t.Fatal("an accepted pin was blocked through the review path")
	}
}
