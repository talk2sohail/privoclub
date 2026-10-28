package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func bootstrapTok(org OrgID, id string) APIToken {
	now := time.Now()
	return APIToken{OrgID: org, ID: id, Name: "bootstrap", Role: RoleOwner, Kind: KindControl, Person: "bootstrap",
		SecretHash: "h-" + id, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
}

func bootstrapEvent(id string) AuditEvent {
	return AuditEvent{ID: id, CallID: id + "-call", Time: time.Now(), Actor: "system", Action: "bootstrap.token.install:x", Decision: "allow", Outcome: "ok"}
}

func countAudit(t *testing.T, s *Store, org OrgID, action string) int {
	t.Helper()
	var n int
	if err := s.r.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_events WHERE org_id = ? AND phase = 'admin' AND action = ?`, org, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInstallBootstrapToken(t *testing.T) {
	t.Run("installs once, marks the org, and audits in the same step", func(t *testing.T) {
		ctx := context.Background()
		s, _ := open(t)
		a, _ := twoOrgs(t, s)
		ev := bootstrapEvent("ev1")
		ev.Action = "bootstrap.token.install:tid1"
		res, err := s.InstallBootstrapToken(ctx, bootstrapTok(a, "tid1"), ev)
		if err != nil || res != BootstrapInstalled {
			t.Fatalf("first install = %v, %v", res, err)
		}
		got, err := s.TokenByID(ctx, "tid1")
		if err != nil || got.OrgID != a || got.Role != RoleOwner || got.Kind != KindControl || got.Name != "bootstrap" {
			t.Fatalf("stored token %+v %v", got, err)
		}
		if v, _ := s.Setting(ctx, a, BootstrapSetting, ""); v != "tid1" {
			t.Fatalf("marker = %q, want tid1", v)
		}
		if n := countAudit(t, s, a, "bootstrap.token.install:tid1"); n != 1 {
			t.Fatalf("audit rows = %d, want 1", n)
		}
	})

	t.Run("a second install is refused and writes nothing", func(t *testing.T) {
		ctx := context.Background()
		s, _ := open(t)
		a, _ := twoOrgs(t, s)
		if _, err := s.InstallBootstrapToken(ctx, bootstrapTok(a, "tid1"), bootstrapEvent("ev1")); err != nil {
			t.Fatal(err)
		}
		res, err := s.InstallBootstrapToken(ctx, bootstrapTok(a, "tid2"), bootstrapEvent("ev2"))
		if err != nil || res != BootstrapConsumed {
			t.Fatalf("second install = %v, %v", res, err)
		}
		if _, err := s.TokenByID(ctx, "tid2"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second token stored: %v", err)
		}
		if n := countAudit(t, s, a, "bootstrap.token.install:x"); n != 1 {
			t.Fatalf("audit rows = %d, want 1", n)
		}
	})

	t.Run("a revoked bootstrap token does not come back", func(t *testing.T) {
		ctx := context.Background()
		s, _ := open(t)
		a, _ := twoOrgs(t, s)
		if _, err := s.InstallBootstrapToken(ctx, bootstrapTok(a, "tid1"), bootstrapEvent("ev1")); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeToken(ctx, a, "tid1"); err != nil {
			t.Fatal(err)
		}
		// Same token again (a restart with the variable still set) and a different one.
		for _, id := range []string{"tid1", "tid2"} {
			res, err := s.InstallBootstrapToken(ctx, bootstrapTok(a, id), bootstrapEvent("ev-"+id))
			if err != nil || res != BootstrapConsumed {
				t.Fatalf("reinstall %s = %v, %v", id, res, err)
			}
		}
		if got, _ := s.TokenByID(ctx, "tid1"); got.RevokedAt == nil {
			t.Fatal("revoked token was revived")
		}
	})

	t.Run("an org that already has a control token is not fresh", func(t *testing.T) {
		ctx := context.Background()
		s, _ := open(t)
		a, _ := twoOrgs(t, s)
		ops := bootstrapTok(a, "ops1")
		ops.Name, ops.Role = "ops", RoleAdmin
		if err := s.CreateToken(ctx, ops); err != nil {
			t.Fatal(err)
		}
		res, err := s.InstallBootstrapToken(ctx, bootstrapTok(a, "tid1"), bootstrapEvent("ev1"))
		if err != nil || res != BootstrapNotNeeded {
			t.Fatalf("install = %v, %v", res, err)
		}
		if _, err := s.TokenByID(ctx, "tid1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("token stored anyway: %v", err)
		}
		if v, _ := s.Setting(ctx, a, BootstrapSetting, ""); v != "" {
			t.Fatalf("marker written for an org that did not need it: %q", v)
		}
	})

	t.Run("a revoked control token still counts: the org has been administered", func(t *testing.T) {
		ctx := context.Background()
		s, _ := open(t)
		a, _ := twoOrgs(t, s)
		ops := bootstrapTok(a, "ops1")
		ops.Name = "ops"
		if err := s.CreateToken(ctx, ops); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeToken(ctx, a, "ops1"); err != nil {
			t.Fatal(err)
		}
		if res, err := s.InstallBootstrapToken(ctx, bootstrapTok(a, "tid1"), bootstrapEvent("ev1")); err != nil || res != BootstrapNotNeeded {
			t.Fatalf("install = %v, %v", res, err)
		}
	})

	t.Run("gateway tokens alone do not block the bootstrap", func(t *testing.T) {
		ctx := context.Background()
		s, _ := open(t)
		a, _ := twoOrgs(t, s)
		agent := bootstrapTok(a, "agent1")
		agent.Name, agent.Kind, agent.Role = "agent", KindGateway, RoleMember
		if err := s.CreateToken(ctx, agent); err != nil {
			t.Fatal(err)
		}
		if res, err := s.InstallBootstrapToken(ctx, bootstrapTok(a, "tid1"), bootstrapEvent("ev1")); err != nil || res != BootstrapInstalled {
			t.Fatalf("install = %v, %v", res, err)
		}
	})

	t.Run("organisations are independent", func(t *testing.T) {
		ctx := context.Background()
		s, _ := open(t)
		a, b := twoOrgs(t, s)
		if res, err := s.InstallBootstrapToken(ctx, bootstrapTok(a, "tid1"), bootstrapEvent("ev1")); err != nil || res != BootstrapInstalled {
			t.Fatalf("org a = %v, %v", res, err)
		}
		if res, err := s.InstallBootstrapToken(ctx, bootstrapTok(b, "tid2"), bootstrapEvent("ev2")); err != nil || res != BootstrapInstalled {
			t.Fatalf("org b = %v, %v", res, err)
		}
		if v, _ := s.Setting(ctx, b, BootstrapSetting, ""); v != "tid2" {
			t.Fatalf("org b marker = %q", v)
		}
	})

	t.Run("only an owner control token can be a bootstrap token", func(t *testing.T) {
		ctx := context.Background()
		s, _ := open(t)
		a, _ := twoOrgs(t, s)
		for name, mut := range map[string]func(*APIToken){
			"admin role":   func(tk *APIToken) { tk.Role = RoleAdmin },
			"gateway kind": func(tk *APIToken) { tk.Kind = KindGateway },
			"no expiry":    func(tk *APIToken) { tk.ExpiresAt = time.Time{} },
			"over 7 days":  func(tk *APIToken) { tk.ExpiresAt = tk.CreatedAt.Add(8 * 24 * time.Hour) },
		} {
			tk := bootstrapTok(a, "tid-"+name)
			mut(&tk)
			if _, err := s.InstallBootstrapToken(ctx, tk, bootstrapEvent("ev-"+name)); err == nil {
				t.Errorf("%s: accepted", name)
			}
		}
		if v, _ := s.Setting(ctx, a, BootstrapSetting, ""); v != "" {
			t.Fatalf("marker written after refusals: %q", v)
		}
	})

	t.Run("a failed audit write rolls the whole install back", func(t *testing.T) {
		ctx := context.Background()
		s, _ := open(t)
		a, _ := twoOrgs(t, s)
		if err := s.InsertAudit(ctx, a, AuditEvent{ID: "ev1", CallID: "c", Phase: "admin", Time: time.Now(), Action: "earlier"}); err != nil {
			t.Fatal(err)
		}
		// Reusing the audit id makes the audit insert fail.
		if _, err := s.InstallBootstrapToken(ctx, bootstrapTok(a, "tid1"), bootstrapEvent("ev1")); err == nil {
			t.Fatal("install succeeded although its audit record could not be written")
		}
		if _, err := s.TokenByID(ctx, "tid1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("token stored without audit: %v", err)
		}
		if v, _ := s.Setting(ctx, a, BootstrapSetting, ""); v != "" {
			t.Fatalf("marker written without audit: %q", v)
		}
	})
}
