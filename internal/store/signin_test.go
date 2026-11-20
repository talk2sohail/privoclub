package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for web sign-in storage (ADR 0016): users, identities, sessions, setup
// links and OAuth states.

func signinOrg(t *testing.T) (*Store, OrgID) {
	t.Helper()
	s, _ := open(t)
	org, err := s.EnsureOrg(context.Background(), "default", "Default")
	if err != nil {
		t.Fatal(err)
	}
	return s, org.ID
}

var t0 = time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

func adminEv(action string) AuditEvent {
	return AuditEvent{ID: "ev-" + action, CallID: "call-" + action, Time: t0, Actor: "tester", ActorID: "t", Action: action, Decision: "allow", Outcome: "ok"}
}

func auditActions(t *testing.T, s *Store, org OrgID) []string {
	t.Helper()
	evs, err := s.RecentAdminEvents(context.Background(), org, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, e.Action)
	}
	return out
}

func newLink(t *testing.T, s *Store, org OrgID, id, hash string, now time.Time) {
	t.Helper()
	err := s.CreateSetupLink(context.Background(), org, NewSetupLink{ID: id, SecretHash: hash, CreatedBy: "cli", Now: now, ExpiresAt: now.Add(time.Hour)}, adminEv("setup_link.create:"+id))
	if err != nil {
		t.Fatal(err)
	}
}

func claimFor(linkID, subject, person, sessionHash string, now time.Time) Claim {
	return Claim{LinkID: linkID, Subject: subject, Login: "login-" + subject, DisplayName: "Name " + subject, Person: person, UserID: "user-" + subject,
		Session: Session{ID: sessionHash, CSRF: "csrf-" + subject, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(12 * time.Hour)}, Now: now}
}

func TestSetupLinkSingleUseAndExpiry(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	newLink(t, s, org, "l1", "hash-1", t0)

	l, err := s.SetupLink(ctx, org, "hash-1", t0.Add(time.Minute))
	if err != nil || l.ID != "l1" {
		t.Fatalf("SetupLink = %+v, %v", l, err)
	}
	if _, err := s.SetupLink(ctx, org, "no-such-hash", t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown hash: %v, want ErrNotFound", err)
	}
	// Expired: exactly at the expiry the link is gone.
	if _, err := s.SetupLink(ctx, org, "hash-1", t0.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("at expiry: %v, want ErrNotFound", err)
	}

	// Claimed once: the second claim and any later lookup fail.
	u, err := s.ClaimSetup(ctx, org, claimFor("l1", "1001", "alice", "sess-a", t0.Add(time.Minute)), adminEv("setup.claim"))
	if err != nil {
		t.Fatal(err)
	}
	if !u.Has(RoleOwner) || len(u.Roles) != 1 || u.Person != "alice" || u.OrgID != org {
		t.Errorf("claimed user %+v", u)
	}
	if _, err := s.SetupLink(ctx, org, "hash-1", t0.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Errorf("claimed link still visible: %v", err)
	}
	if _, err := s.ClaimSetup(ctx, org, claimFor("l1", "1002", "bob", "sess-b", t0.Add(2*time.Minute)), adminEv("setup.claim2")); !errors.Is(err, ErrSetupUnavailable) {
		t.Errorf("second claim: %v, want ErrSetupUnavailable", err)
	}
	// An expired link cannot be claimed either.
	newLink(t, s, org, "l2", "hash-2", t0)
	if _, err := s.ClaimSetup(ctx, org, claimFor("l2", "1003", "carol", "sess-c", t0.Add(61*time.Minute)), adminEv("setup.claim3")); !errors.Is(err, ErrSetupUnavailable) {
		t.Errorf("expired claim: %v, want ErrSetupUnavailable", err)
	}
}

func TestClaimUsesTheLinksPersonFirst(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	// A link made with --person names the person the owner's tokens use.
	err := s.CreateSetupLink(ctx, org, NewSetupLink{ID: "lp", SecretHash: "hash-lp", Person: "alice", CreatedBy: "cli", Now: t0, ExpiresAt: t0.Add(time.Hour)}, adminEv("setup_link.create:lp"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.ClaimSetup(ctx, org, claimFor("lp", "1001", "octocat", "sess-lp", t0.Add(time.Minute)), adminEv("setup.claim.lp"))
	if err != nil || u.Person != "alice" {
		t.Fatalf("ClaimSetup = %+v, %v; want person alice (the link's), not octocat (the fallback)", u, err)
	}
	got, err := s.UserByIdentity(ctx, org, ProviderGitHub, "1001")
	if err != nil || got.Person != "alice" {
		t.Errorf("stored person = %q, %v", got.Person, err)
	}
	// Without a person on the link the claim's own is used.
	newLink(t, s, org, "ln", "hash-ln", t0)
	u2, err := s.ClaimSetup(ctx, org, claimFor("ln", "1002", "bob", "sess-ln", t0.Add(time.Minute)), adminEv("setup.claim.ln"))
	if err != nil || u2.Person != "bob" {
		t.Errorf("ClaimSetup = %+v, %v; want person bob", u2, err)
	}
}

func TestSetupLinkNewOneRetiresOld(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	newLink(t, s, org, "old", "hash-old", t0)
	newLink(t, s, org, "new", "hash-new", t0.Add(time.Minute))
	if _, err := s.SetupLink(ctx, org, "hash-old", t0.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Errorf("the older link still works: %v", err)
	}
	if _, err := s.ClaimSetup(ctx, org, claimFor("old", "1", "a", "sess-1", t0.Add(2*time.Minute)), adminEv("c1")); !errors.Is(err, ErrSetupUnavailable) {
		t.Errorf("claim of a retired link: %v", err)
	}
	if _, err := s.SetupLink(ctx, org, "hash-new", t0.Add(2*time.Minute)); err != nil {
		t.Errorf("the new link: %v", err)
	}
}

func TestClaimRacesHaveOneWinner(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	newLink(t, s, org, "race", "hash-race", t0)
	var wins, losses atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := claimFor("race", fmt.Sprint(2000+i), fmt.Sprintf("person%d", i), fmt.Sprintf("sess-race-%d", i), t0.Add(time.Minute))
			switch _, err := s.ClaimSetup(ctx, org, c, adminEv(fmt.Sprintf("setup.claim.%d", i))); {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, ErrSetupUnavailable):
				losses.Add(1)
			default:
				t.Errorf("claim %d: unexpected error %v", i, err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || losses.Load() != 11 {
		t.Fatalf("wins %d, losses %d; want 1 and 11", wins.Load(), losses.Load())
	}
	var users, sessions, claimEvents int
	_ = s.r.QueryRow(`SELECT COUNT(*) FROM users WHERE org_id = ?`, org).Scan(&users)
	_ = s.r.QueryRow(`SELECT COUNT(*) FROM sessions WHERE org_id = ?`, org).Scan(&sessions)
	for _, a := range auditActions(t, s, org) {
		if strings.HasPrefix(a, "setup.claim.") {
			claimEvents++
		}
	}
	if users != 1 || sessions != 1 || claimEvents != 1 {
		t.Errorf("users %d, sessions %d, claim audit rows %d; want 1 each", users, sessions, claimEvents)
	}
}

func TestClaimRefusedForExistingIdentityKeepsLink(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	newLink(t, s, org, "first", "hash-first", t0)
	if _, err := s.ClaimSetup(ctx, org, claimFor("first", "777", "alice", "sess-1", t0), adminEv("claim-ok")); err != nil {
		t.Fatal(err)
	}
	newLink(t, s, org, "second", "hash-second", t0.Add(time.Minute))
	before := len(auditActions(t, s, org))

	// Same GitHub id again: refused, and the link and the audit log are untouched.
	_, err := s.ClaimSetup(ctx, org, claimFor("second", "777", "alice2", "sess-2", t0.Add(2*time.Minute)), adminEv("claim-dup"))
	if !errors.Is(err, ErrIdentityExists) {
		t.Fatalf("err = %v, want ErrIdentityExists", err)
	}
	// The person name is taken by someone else.
	_, err = s.ClaimSetup(ctx, org, claimFor("second", "888", "alice", "sess-3", t0.Add(2*time.Minute)), adminEv("claim-person"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if _, err := s.SetupLink(ctx, org, "hash-second", t0.Add(3*time.Minute)); err != nil {
		t.Errorf("the link was consumed by a refused claim: %v", err)
	}
	if after := len(auditActions(t, s, org)); after != before {
		t.Errorf("a refused claim wrote %d audit rows", after-before)
	}
	var sessions int
	_ = s.r.QueryRow(`SELECT COUNT(*) FROM sessions WHERE org_id = ?`, org).Scan(&sessions)
	if sessions != 1 {
		t.Errorf("%d sessions after a refused claim, want the first claim's 1", sessions)
	}
	// A new GitHub account with a free person name still claims it.
	if _, err := s.ClaimSetup(ctx, org, claimFor("second", "999", "bob", "sess-4", t0.Add(3*time.Minute)), adminEv("claim-bob")); err != nil {
		t.Errorf("a valid claim after refused ones: %v", err)
	}
}

func TestIdentityUniquePerProvider(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	newLink(t, s, org, "l", "h", t0)
	if _, err := s.ClaimSetup(ctx, org, claimFor("l", "4242", "alice", "sess-1", t0), adminEv("c")); err != nil {
		t.Fatal(err)
	}
	u, err := s.UserByIdentity(ctx, org, ProviderGitHub, "4242")
	if err != nil || u.Person != "alice" {
		t.Fatalf("UserByIdentity = %+v, %v", u, err)
	}
	// A login is not an identity: only the numeric subject finds a user.
	if _, err := s.UserByIdentity(ctx, org, ProviderGitHub, "login-4242"); !errors.Is(err, ErrNotFound) {
		t.Errorf("found a user by login: %v", err)
	}
	// Another provider with the same subject is another identity.
	if _, err := s.UserByIdentity(ctx, org, "google", "4242"); !errors.Is(err, ErrNotFound) {
		t.Errorf("other provider: %v", err)
	}
	// Another org never sees it.
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	if _, err := s.UserByIdentity(ctx, other.ID, ProviderGitHub, "4242"); !errors.Is(err, ErrNotFound) {
		t.Errorf("other org: %v", err)
	}
}

func startSession(t *testing.T, s *Store, org OrgID, sessHash string, now time.Time) User {
	t.Helper()
	const subject = "1"
	newLink(t, s, org, "link-"+subject, "lh-"+subject, now)
	u, err := s.ClaimSetup(context.Background(), org, claimFor("link-"+subject, subject, "p"+subject, sessHash, now), adminEv("claim-"+subject))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSessionExpiry(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	startSession(t, s, org, "sess-1", t0)
	idle := 2 * time.Hour

	sess, u, err := s.SessionByHash(ctx, org, "sess-1", t0.Add(time.Hour), idle)
	if err != nil || sess.CSRF != "csrf-1" || u.Person != "p1" {
		t.Fatalf("SessionByHash = %+v, %+v, %v", sess, u, err)
	}
	// Idle: nothing happened for the idle timeout.
	if _, _, err := s.SessionByHash(ctx, org, "sess-1", t0.Add(2*time.Hour), idle); !errors.Is(err, ErrNotFound) {
		t.Errorf("idle session: %v, want ErrNotFound", err)
	}
	// Touching keeps it alive, but never past the absolute lifetime.
	if err := s.TouchSession(ctx, org, "sess-1", t0.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SessionByHash(ctx, org, "sess-1", t0.Add(3*time.Hour), idle); err != nil {
		t.Errorf("touched session: %v", err)
	}
	for h := 1; h <= 6; h++ {
		_ = s.TouchSession(ctx, org, "sess-1", t0.Add(time.Duration(h)*2*time.Hour-time.Minute))
	}
	if _, _, err := s.SessionByHash(ctx, org, "sess-1", t0.Add(12*time.Hour), idle); !errors.Is(err, ErrNotFound) {
		t.Errorf("absolute expiry: %v, want ErrNotFound", err)
	}
	// Another org has no such session.
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	if _, _, err := s.SessionByHash(ctx, other.ID, "sess-1", t0, idle); !errors.Is(err, ErrNotFound) {
		t.Errorf("other org: %v", err)
	}
}

func TestTouchSessionWritesAtMostOncePerMinute(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	startSession(t, s, org, "sess-1", t0)
	read := func() string {
		var v string
		if err := s.r.QueryRow(`SELECT last_seen_at FROM sessions WHERE org_id = ? AND id = 'sess-1'`, org).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	base := read()
	_ = s.TouchSession(ctx, org, "sess-1", t0.Add(30*time.Second))
	if read() != base {
		t.Error("touched again within a minute")
	}
	_ = s.TouchSession(ctx, org, "sess-1", t0.Add(61*time.Second))
	if read() == base {
		t.Error("not touched after a minute")
	}
}

func TestSessionReadsLiveUser(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	u := startSession(t, s, org, "sess-1", t0)
	now := t0.Add(time.Minute)

	if _, err := s.SetUserRoles(ctx, org, u.ID, []string{RoleMember}, adminEv("role")); err != nil {
		t.Fatal(err)
	}
	_, got, err := s.SessionByHash(ctx, org, "sess-1", now, time.Hour)
	if err != nil || len(got.Roles) != 1 || !got.Has(RoleMember) {
		t.Fatalf("after a role change: %+v, %v", got, err)
	}
	if _, err := s.SetUserRoles(ctx, org, u.ID, []string{"emperor"}, adminEv("bad")); err == nil {
		t.Error("an unknown role was accepted")
	}

	if err := s.DisableUser(ctx, org, u.ID, now, adminEv("disable")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SessionByHash(ctx, org, "sess-1", now, time.Hour); !errors.Is(err, ErrNotFound) {
		t.Errorf("session of a disabled user: %v, want ErrNotFound", err)
	}
	var left int
	_ = s.r.QueryRow(`SELECT COUNT(*) FROM sessions WHERE org_id = ? AND user_id = ?`, org, u.ID).Scan(&left)
	if left != 0 {
		t.Errorf("%d sessions of a disabled user remain", left)
	}
	d, err := s.UserByIdentity(ctx, org, ProviderGitHub, "1")
	if err != nil || d.DisabledAt == nil {
		t.Errorf("UserByIdentity should report the disabled user: %+v, %v", d, err)
	}
}

func TestCreateAndDeleteSession(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	u := startSession(t, s, org, "sess-1", t0)
	now := t0.Add(time.Hour)
	sess := Session{ID: "sess-2", UserID: u.ID, CSRF: "csrf-2", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(12 * time.Hour)}
	if err := s.CreateSession(ctx, org, sess, adminEv("signin")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SessionByHash(ctx, org, "sess-2", now, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, org, "sess-2", adminEv("signout")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SessionByHash(ctx, org, "sess-2", now, time.Hour); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted session still valid: %v", err)
	}
	// Deleting twice is not an error and writes no second audit row.
	n := len(auditActions(t, s, org))
	if err := s.DeleteSession(ctx, org, "sess-2", adminEv("signout-again")); err != nil {
		t.Errorf("second delete: %v", err)
	}
	if len(auditActions(t, s, org)) != n {
		t.Error("deleting nothing wrote an audit row")
	}
	// A session for a user that does not exist is refused, and so is a duplicate id.
	bad := Session{ID: "sess-3", UserID: "nobody", CSRF: "c", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.CreateSession(ctx, org, bad, adminEv("signin-bad")); err == nil {
		t.Error("a session for an unknown user was created")
	}
	dup := Session{ID: "sess-1", UserID: u.ID, CSRF: "c", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.CreateSession(ctx, org, dup, adminEv("signin-dup")); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate session id: %v, want ErrConflict", err)
	}
}

func TestOAuthStateSingleUse(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	st := OAuthState{Hash: "state-hash", Purpose: PurposeSetup, SetupLinkID: "link-1", Verifier: "verifier-1", ExpiresAt: t0.Add(10 * time.Minute)}
	if err := s.CreateOAuthState(ctx, org, st, t0, 500); err != nil {
		t.Fatal(err)
	}
	got, err := s.UseOAuthState(ctx, org, "state-hash", t0.Add(time.Minute))
	if err != nil || got.Purpose != PurposeSetup || got.SetupLinkID != "link-1" || got.Verifier != "verifier-1" {
		t.Fatalf("UseOAuthState = %+v, %v", got, err)
	}
	if _, err := s.UseOAuthState(ctx, org, "state-hash", t0.Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Errorf("replay: %v, want ErrNotFound", err)
	}
	// Expired.
	_ = s.CreateOAuthState(ctx, org, OAuthState{Hash: "old", Purpose: PurposeSignIn, Verifier: "v", ExpiresAt: t0.Add(10 * time.Minute)}, t0, 500)
	if _, err := s.UseOAuthState(ctx, org, "old", t0.Add(10*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired state: %v, want ErrNotFound", err)
	}
	// Unknown and other-org states.
	if _, err := s.UseOAuthState(ctx, org, "never-issued", t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	_ = s.CreateOAuthState(ctx, org, OAuthState{Hash: "mine", Purpose: PurposeSignIn, Verifier: "v", ExpiresAt: t0.Add(time.Hour)}, t0, 500)
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	if _, err := s.UseOAuthState(ctx, other.ID, "mine", t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("other org: %v", err)
	}
	if err := s.CreateOAuthState(ctx, org, OAuthState{Hash: "mine", Purpose: PurposeSignIn, Verifier: "v", ExpiresAt: t0.Add(time.Hour)}, t0, 500); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate hash: %v, want ErrConflict", err)
	}
}

func TestOAuthStateCap(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	signin := func(hash string, at time.Time) error {
		return s.CreateOAuthState(ctx, org, OAuthState{Hash: hash, Purpose: PurposeSignIn, Verifier: "v", ExpiresAt: at.Add(10 * time.Minute)}, at, 5)
	}
	for i := 0; i < 5; i++ {
		if err := signin(fmt.Sprint("s", i), t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	// At the cap the oldest anonymous sign-in makes room; the new one is kept.
	if err := signin("s5", t0.Add(5*time.Second)); err != nil {
		t.Fatalf("sixth state: %v", err)
	}
	if _, err := s.UseOAuthState(ctx, org, "s0", t0.Add(6*time.Second)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the oldest was kept: %v", err)
	}
	for _, h := range []string{"s1", "s5"} {
		if _, err := s.UseOAuthState(ctx, org, h, t0.Add(6*time.Second)); err != nil {
			t.Fatalf("%s: %v", h, err)
		}
	}
}

// F05 R-01, F07 R-03: a flood of anonymous sign-ins locks out no setup link,
// invite or account connection, and those keep one sign-in each.
func TestSignInFloodLocksOutNothingElse(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	exp := t0.Add(10 * time.Minute)
	for i := 0; i < 50; i++ {
		if err := s.CreateOAuthState(ctx, org, OAuthState{Hash: fmt.Sprint("flood", i), Purpose: PurposeSignIn, Verifier: "v", ExpiresAt: exp}, t0, 5); err != nil {
			t.Fatal(err)
		}
	}
	others := []OAuthState{
		{Hash: "setup-1", Purpose: PurposeSetup, SetupLinkID: "link", Verifier: "v", ExpiresAt: exp},
		{Hash: "invite-1", Purpose: PurposeInvite, InviteID: "inv", Verifier: "v", ExpiresAt: exp},
		{Hash: "connect-1", Purpose: PurposeConnect, UserID: "u1", ServerID: "srv", Verifier: "v", ExpiresAt: exp},
	}
	for _, st := range others {
		if err := s.CreateOAuthState(ctx, org, st, t0, 5); err != nil {
			t.Fatalf("%s during a flood: %v", st.Purpose, err)
		}
	}
	// One in flight per link and per person: a new start replaces the older.
	for i := 2; i < 10; i++ {
		for _, st := range others {
			st.Hash = fmt.Sprintf("%s-%d", st.Purpose, i)
			if err := s.CreateOAuthState(ctx, org, st, t0, 5); err != nil {
				t.Fatalf("%s again: %v", st.Purpose, err)
			}
		}
	}
	for _, h := range []string{"setup-1", "invite-1", "connect-1"} {
		if _, err := s.UseOAuthState(ctx, org, h, t0); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s was not replaced: %v", h, err)
		}
	}
	for _, h := range []string{"setup-9", "invite-9", "connect-9"} {
		if _, err := s.UseOAuthState(ctx, org, h, t0); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
	// Setups, invites and connections together are still bounded.
	for i := 0; i < 5; i++ {
		err := s.CreateOAuthState(ctx, org, OAuthState{Hash: fmt.Sprint("c", i), Purpose: PurposeConnect, UserID: fmt.Sprint("user", i), Verifier: "v", ExpiresAt: exp}, t0, 5)
		if i < 5 && err != nil {
			t.Fatalf("connect %d: %v", i, err)
		}
	}
	if err := s.CreateOAuthState(ctx, org, OAuthState{Hash: "c5", Purpose: PurposeConnect, UserID: "user5", Verifier: "v", ExpiresAt: exp}, t0, 5); !errors.Is(err, ErrTooManyStates) {
		t.Fatalf("over the bound: %v", err)
	}
}

func TestPruneSignIn(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	u := startSession(t, s, org, "sess-live", t0.Add(30*time.Hour))
	// An old session (expired), an idle one, an expired state, an old unclaimed link.
	old := Session{ID: "sess-old", UserID: u.ID, CSRF: "c", CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(12 * time.Hour)}
	if err := s.CreateSession(ctx, org, old, adminEv("signin-old")); err != nil {
		t.Fatal(err)
	}
	idle := Session{ID: "sess-idle", UserID: u.ID, CSRF: "c", CreatedAt: t0.Add(20 * time.Hour), LastSeenAt: t0.Add(20 * time.Hour), ExpiresAt: t0.Add(80 * time.Hour)}
	if err := s.CreateSession(ctx, org, idle, adminEv("signin-idle")); err != nil {
		t.Fatal(err)
	}
	_ = s.CreateOAuthState(ctx, org, OAuthState{Hash: "state-old", Purpose: PurposeSignIn, Verifier: "v", ExpiresAt: t0.Add(10 * time.Minute)}, t0, 500)
	_ = s.CreateOAuthState(ctx, org, OAuthState{Hash: "state-live", Purpose: PurposeSignIn, Verifier: "v", ExpiresAt: t0.Add(40 * time.Hour)}, t0, 500)
	newLink(t, s, org, "link-old", "hash-old", t0)

	now := t0.Add(31 * time.Hour)
	if err := s.PruneSignIn(ctx, org, now, 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := s.r.QueryRow(q, org).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM sessions WHERE org_id = ?`); n != 1 {
		t.Errorf("%d sessions left, want only the live one", n)
	}
	if n := count(`SELECT COUNT(*) FROM oauth_states WHERE org_id = ?`); n != 1 {
		t.Errorf("%d states left, want only the unexpired one", n)
	}
	if n := count(`SELECT COUNT(*) FROM setup_links WHERE org_id = ? AND claimed_at IS NULL`); n != 0 {
		t.Errorf("%d expired unclaimed links left", n)
	}
	if n := count(`SELECT COUNT(*) FROM setup_links WHERE org_id = ? AND claimed_at IS NOT NULL`); n != 1 {
		t.Errorf("claimed links must stay as a record: %d", n)
	}
}

// The schema has no place for a GitHub token, a client secret or a plain
// session secret; what the tables hold are hashes and ids.
func TestSignInSchemaHasNoSecretColumns(t *testing.T) {
	s, _ := signinOrg(t)
	for _, table := range []string{"users", "identities", "sessions", "setup_links", "oauth_states"} {
		rows, err := s.r.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var col string
			_ = rows.Scan(&col)
			n++
			for _, bad := range []string{"token", "access", "client_secret", "password", "cookie"} {
				if strings.Contains(col, bad) {
					t.Errorf("%s.%s looks like it holds a secret", table, col)
				}
			}
		}
		_ = rows.Close()
		if n == 0 {
			t.Errorf("table %s does not exist", table)
		}
	}
}

// Every table of this feature is isolated by org.
func TestSignInTablesAreIsolatedPerOrg(t *testing.T) {
	ctx := context.Background()
	s, org := signinOrg(t)
	other, _ := s.EnsureOrg(ctx, "other", "Other")
	startSession(t, s, org, "sess-1", t0)
	if _, err := s.SetupLink(ctx, other.ID, "lh-1", t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("setup link visible from another org: %v", err)
	}
	newLink(t, s, other.ID, "ol", "lh-1", t0) // the same hash in another org is a different link
	if _, err := s.SetupLink(ctx, other.ID, "lh-1", t0); err != nil {
		t.Errorf("same hash, other org: %v", err)
	}
}
