package store

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// ADR 0029: editing a server in one transaction.

// staticCred is a sealed-looking static credential of server srv1.
func staticCred(id, purpose string) Credential {
	return Credential{ID: id, ServerID: "srv1", Purpose: purpose, KeyVersion: 1, RowVersion: 1, Ciphertext: []byte("sealed-" + id)}
}

// credPurposes lists server srv1's credentials as purpose=ciphertext.
func credPurposes(t *testing.T, s *Store, org OrgID) []string {
	t.Helper()
	cs, err := s.ServerCredentials(context.Background(), org, "srv1")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range cs {
		out = append(out, c.Purpose+"="+string(c.Ciphertext))
	}
	slices.Sort(out)
	return out
}

func TestEditServerIsOneTransaction(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	srv := Server{OrgID: org, ID: "srv1", Alias: "acme", Transport: "http", Enabled: true, CreatedAt: t0,
		Config: ServerConfig{URL: "https://acme.example/mcp", HeaderNames: []string{"Authorization"}}}
	if err := s.CreateServer(ctx, srv, []Credential{staticCred("c-auth", "header:Authorization")}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.ServerByID(ctx, org, "srv1")

	cfg := srv.Config
	cfg.URL, cfg.HeaderNames = "https://acme.example/v2/mcp", []string{"Authorization", "X-Team"}
	out, err := s.EditServer(ctx, org, "srv1", ServerEdit{Version: before.UpdatedAt, Config: cfg,
		SetCreds: []Credential{staticCred("c-auth2", "header:Authorization"), staticCred("c-team", "header:X-Team")}},
		[]AuditEvent{adminEv("server.url"), adminEv("server.headers:+X-Team")})
	if err != nil || out.Ended != 0 || out.Reviewed != 0 {
		t.Fatalf("edit: %+v %v", out, err)
	}
	after, _ := s.ServerByID(ctx, org, "srv1")
	if after.Config.URL != cfg.URL || !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("after the edit: %+v (updated %v, was %v)", after.Config, after.UpdatedAt, before.UpdatedAt)
	}
	// A credential with the same purpose is replaced, never doubled.
	if got := credPurposes(t, s, org); !slices.Equal(got, []string{"header:Authorization=sealed-c-auth2", "header:X-Team=sealed-c-team"}) {
		t.Fatalf("credentials: %q", got)
	}
	if acts := auditActions(t, s, org); !slices.Contains(acts, "server.url") || !slices.Contains(acts, "server.headers:+X-Team") {
		t.Fatalf("audit: %q", acts)
	}

	// A failing step (an event that cannot be written: the same id again)
	// leaves the server as it was.
	cfg2 := after.Config
	cfg2.URL = "https://elsewhere.example/mcp"
	if _, err := s.EditServer(ctx, org, "srv1", ServerEdit{Version: after.UpdatedAt, Config: cfg2, RemoveCreds: []string{"header:X-Team"}},
		[]AuditEvent{adminEv("server.url")}); err == nil {
		t.Fatal("an edit whose audit failed went through")
	}
	again, _ := s.ServerByID(ctx, org, "srv1")
	if again.Config.URL != cfg.URL || len(credPurposes(t, s, org)) != 2 {
		t.Fatalf("a failed edit changed the server: %+v", again.Config)
	}

	if _, err := s.EditServer(ctx, org, "nope", ServerEdit{Version: t0, Config: cfg}, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown server: %v", err)
	}
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	if _, err := s.EditServer(ctx, other.ID, "srv1", ServerEdit{Version: t0, Config: cfg}, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org's server: %v", err)
	}
}

func TestEditServerEndsConnectionsAndCredentials(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	srv := oauthServer(t, s, org, "linear")
	ada := member(t, s, org, "2001", "ada", RoleMember)
	bob := member(t, s, org, "2002", "bob", RoleMember)
	for i, u := range []User{ada, bob} {
		if _, err := s.SaveGrant(ctx, org, grantFor(srv, u.ID, "g"+u.ID), false, adminEv("upstream.connect:"+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	other := oauthServer(t, s, org, "github")
	if _, err := s.SaveGrant(ctx, org, grantFor(other, ada.ID, "keep"), false, adminEv("upstream.connect:github")); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.ServerByID(ctx, org, srv)
	out, err := s.EditServer(ctx, org, srv, ServerEdit{Version: cur.UpdatedAt, Config: cur.Config, EndConnections: true}, []AuditEvent{adminEv("server.oauth_client")})
	if err != nil || out.Ended != 2 {
		t.Fatalf("ended %+v, %v", out, err)
	}
	if gs, _ := s.ListServerGrants(ctx, org, srv); len(gs) != 0 {
		t.Fatalf("grants left: %+v", gs)
	}
	// Their sealed tokens went with them; another server's grant stays.
	var n int
	_ = s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM credentials WHERE org_id = ? AND server_id = ? AND owner_id <> ''`, org, srv).Scan(&n)
	if n != 0 {
		t.Fatalf("%d personal credentials left", n)
	}
	if gs, _ := s.ListServerGrants(ctx, org, other); len(gs) != 1 {
		t.Fatalf("another server's grant: %+v", gs)
	}
	if acts := auditActions(t, s, org); !slices.Contains(acts, "server.connections_ended:2") {
		t.Fatalf("audit: %q", acts)
	}
}

func TestEditServerSendsToolsBackToReview(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	addServer(t, s, org, "srv1", "kit", nil)
	addServer(t, s, org, "srv2", "other", nil)
	pins := []Pin{
		{ServerID: "srv1", Kind: "tool", Name: "echo", Hash: "h1", Definition: "{}"},
		{ServerID: "srv1", Kind: "tool", Name: "shout", Hash: "h2", Definition: "{}"},
		{ServerID: "srv1", Kind: "tool", Name: "bad", Hash: "h3", Definition: "{}"},
		{ServerID: "srv2", Kind: "tool", Name: "echo", Hash: "h1", Definition: "{}"},
	}
	if err := s.RecordSeen(ctx, org, pins); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptPins(ctx, org, "srv1", []PinKey{{Kind: "tool", Name: "echo", Hash: "h1"}, {Kind: "tool", Name: "shout", Hash: "h2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BlockPins(ctx, org, "srv1", []PinKey{{Kind: "tool", Name: "bad", Hash: "h3"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptPins(ctx, org, "srv2", []PinKey{{Kind: "tool", Name: "echo", Hash: "h1"}}); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.ServerByID(ctx, org, "srv1")
	out, err := s.EditServer(ctx, org, "srv1", ServerEdit{Version: cur.UpdatedAt, Config: cur.Config, ReviewAgain: true}, []AuditEvent{adminEv("server.url")})
	if err != nil || out.Reviewed != 2 {
		t.Fatalf("reviewed %+v, %v", out, err)
	}
	status := func(server string) map[string]string {
		ps, _ := s.ListPins(ctx, org, server)
		m := map[string]string{}
		for _, p := range ps {
			m[p.Name] = p.Status
		}
		return m
	}
	// Accepted goes back to pending; blocked stays blocked; another server
	// keeps what it accepted.
	if got := status("srv1"); got["echo"] != PinPending || got["shout"] != PinPending || got["bad"] != PinBlocked {
		t.Fatalf("srv1 pins: %v", got)
	}
	if got := status("srv2"); got["echo"] != PinAccepted {
		t.Fatalf("srv2 pins: %v", got)
	}
	if acts := auditActions(t, s, org); !slices.Contains(acts, "server.review_again:2") {
		t.Fatalf("audit: %q", acts)
	}
}

// Security review of the third slice, M1: an edit built from an old read of
// the server is refused, so it cannot undo another admin's change or leave a
// header credential the configuration no longer names.
func TestEditServerRefusesAStaleEdit(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	addServer(t, s, org, "srv1", "kit", nil)
	read, _ := s.ServerByID(ctx, org, "srv1")
	cfg := read.Config
	cfg.HeaderNames = []string{"X-Api-Key"}
	if _, err := s.EditServer(ctx, org, "srv1", ServerEdit{Version: read.UpdatedAt, Config: cfg, SetCreds: []Credential{staticCred("c-key", "header:X-Api-Key")}},
		[]AuditEvent{adminEv("server.headers:+X-Api-Key")}); err != nil {
		t.Fatal(err)
	}
	moved := read.Config
	moved.URL = "https://elsewhere.example/mcp"
	if _, err := s.EditServer(ctx, org, "srv1", ServerEdit{Version: read.UpdatedAt, Config: moved, NewAddress: true}, []AuditEvent{adminEv("server.url")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("an edit from a stale read: %v", err)
	}
	if got := credPurposes(t, s, org); len(got) != 1 {
		t.Fatalf("credentials after the refused edit: %q", got)
	}
}

// A new address deletes every static header credential not given again,
// whatever the configuration it was computed from said.
func TestNewAddressDropsEveryStaticHeader(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	srv := Server{OrgID: org, ID: "srv1", Alias: "kit", Transport: "http", Enabled: true, CreatedAt: t0,
		Config: ServerConfig{URL: "https://kit.example/mcp", HeaderNames: []string{"Authorization"}}}
	// A credential the configuration does not name (left by an older edit).
	if err := s.CreateServer(ctx, srv, []Credential{staticCred("c-auth", "header:Authorization"), staticCred("c-stray", "header:X-Stray"),
		staticCred("c-env", "env:TOKEN")}); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.ServerByID(ctx, org, "srv1")
	cfg := cur.Config
	cfg.URL, cfg.HeaderNames = "https://elsewhere.example/mcp", []string{"Authorization"}
	if _, err := s.EditServer(ctx, org, "srv1", ServerEdit{Version: cur.UpdatedAt, Config: cfg, NewAddress: true,
		SetCreds: []Credential{staticCred("c-auth2", "header:Authorization")}}, []AuditEvent{adminEv("server.url")}); err != nil {
		t.Fatal(err)
	}
	if got := credPurposes(t, s, org); !slices.Equal(got, []string{"env:TOKEN=sealed-c-env", "header:Authorization=sealed-c-auth2"}) {
		t.Fatalf("credentials: %q", got)
	}
}
