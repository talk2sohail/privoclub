package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIdentitiesLinkListUnlink(t *testing.T) {
	st, org := signinOrg(t)
	ctx := context.Background()
	now := time.Now()
	ada := member(t, st, org, "gh-ada", "ada", RoleMember)
	bob := member(t, st, org, "gh-bob", "bob", RoleMember)
	google := Identity{Provider: "https://accounts.google.com", Subject: "1001", Login: "ada@example.com", CreatedAt: now}
	ev := AuditEvent{ID: "e1", Time: now, Actor: "ada", Action: "identity.link"}

	if err := st.LinkIdentity(ctx, org, ada.ID, google, ev); err != nil {
		t.Fatal(err)
	}
	// One identity belongs to one member: neither ada again nor bob.
	if err := st.LinkIdentity(ctx, org, bob.ID, google, AuditEvent{ID: "e2", Time: now}); !errors.Is(err, ErrIdentityExists) {
		t.Fatalf("someone else's identity: %v", err)
	}
	if err := st.LinkIdentity(ctx, org, ada.ID, google, AuditEvent{ID: "e3", Time: now}); !errors.Is(err, ErrIdentityExists) {
		t.Fatalf("linked twice: %v", err)
	}
	ids, err := st.Identities(ctx, org, ada.ID)
	if err != nil || len(ids) != 2 {
		t.Fatalf("ada has %v %v", ids, err)
	}
	if u, err := st.UserByIdentity(ctx, org, google.Provider, google.Subject); err != nil || u.ID != ada.ID {
		t.Fatalf("lookup by the linked identity: %+v %v", u, err)
	}
	// Unlink the GitHub one; the last one stays.
	both := []string{ProviderGitHub, google.Provider}
	// With only GitHub set up, Google is no way in: GitHub is the last.
	if err := st.UnlinkIdentity(ctx, org, ada.ID, ProviderGitHub, "gh-ada", []string{ProviderGitHub}, "", AuditEvent{ID: "e3b", Time: now}); !errors.Is(err, ErrLastIdentity) {
		t.Fatalf("the last usable identity: %v", err)
	}
	second := Session{ID: "sess-ada-2", UserID: ada.ID, CSRF: "c2", CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(12 * time.Hour)}
	if err := st.CreateSession(ctx, org, second, AuditEvent{ID: "e3c", Time: now, Action: "signin"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UnlinkIdentity(ctx, org, ada.ID, ProviderGitHub, "gh-ada", both, "sess-gh-ada", AuditEvent{ID: "e4", Time: now}); err != nil {
		t.Fatal(err)
	}
	// Her other sessions end with it; the one she unlinked from stays, and
	// bob's are untouched.
	at := t0.Add(2 * time.Minute)
	if _, _, err := st.SessionByHash(ctx, org, "sess-ada-2", at, 2*time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ada's other session: %v", err)
	}
	for _, keep := range []string{"sess-gh-ada", "sess-gh-bob"} {
		if _, _, err := st.SessionByHash(ctx, org, keep, at, 2*time.Hour); err != nil {
			t.Fatalf("%s: %v", keep, err)
		}
	}
	if err := st.UnlinkIdentity(ctx, org, ada.ID, google.Provider, google.Subject, both, "", AuditEvent{ID: "e5", Time: now}); !errors.Is(err, ErrLastIdentity) {
		t.Fatalf("the last identity: %v", err)
	}
	if ids, _ := st.Identities(ctx, org, ada.ID); len(ids) != 1 {
		t.Fatalf("a refused unlink removed something: %v", ids)
	}
	// Another member's identity cannot be unlinked through ada.
	if err := st.UnlinkIdentity(ctx, org, ada.ID, ProviderGitHub, "gh-bob", both, "", AuditEvent{ID: "e6", Time: now}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob's identity through ada: %v", err)
	}
	// A disabled member gets no new identity.
	if err := st.DisableUser(ctx, org, bob.ID, now, AuditEvent{ID: "e7", Time: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.LinkIdentity(ctx, org, bob.ID, Identity{Provider: "x", Subject: "y", CreatedAt: now}, AuditEvent{ID: "e8", Time: now}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a disabled member: %v", err)
	}
}

func TestMembersWithoutProviders(t *testing.T) {
	st, org := signinOrg(t)
	ctx := context.Background()
	ada := member(t, st, org, "gh-ada", "ada", RoleMember)
	member(t, st, org, "gh-bob", "bob", RoleMember)
	if err := st.LinkIdentity(ctx, org, ada.ID, Identity{Provider: "https://accounts.google.com", Subject: "1", CreatedAt: time.Now()}, AuditEvent{ID: "e1", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		providers []string
		want      int
	}{
		{[]string{ProviderGitHub}, 0},
		{[]string{"https://accounts.google.com"}, 1}, // bob has only GitHub
		{[]string{ProviderGitHub, "https://accounts.google.com"}, 0},
		{nil, 2},
	} {
		if n, err := st.MembersWithoutProviders(ctx, org, tc.providers); err != nil || n != tc.want {
			t.Errorf("%v: %d %v, want %d", tc.providers, n, err, tc.want)
		}
	}
}
