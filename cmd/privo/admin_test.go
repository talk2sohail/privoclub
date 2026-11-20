package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/web"
)

// adminEnv returns a getenv for a fresh data directory, with a public URL and
// the GitHub settings unless a test overrides them.
func adminEnv(t *testing.T, extra map[string]string) (func(string) string, string) {
	t.Helper()
	dir := t.TempDir()
	vars := map[string]string{
		"PRIVO_DATA_DIR": dir, "PRIVO_PUBLIC_URL": "https://privo.example.com",
		"PRIVO_GITHUB_CLIENT_ID": "Iv1.abc", "PRIVO_GITHUB_CLIENT_SECRET": "secret-value-0123456789",
	}
	for k, v := range extra {
		vars[k] = v
	}
	return func(k string) string { return vars[k] }, dir
}

// setupLinkToken pulls the token out of the URL the command printed.
func setupLinkToken(t *testing.T, stdout string) string {
	t.Helper()
	line := strings.TrimSpace(stdout)
	const prefix = "https://privo.example.com/admin/setup/"
	if !strings.HasPrefix(line, prefix) || strings.Contains(line, "\n") {
		t.Fatalf("stdout %q is not just the setup URL", stdout)
	}
	tok := strings.TrimPrefix(line, prefix)
	if !strings.HasPrefix(tok, "privo_setup_") {
		t.Fatalf("token %q", tok)
	}
	return tok
}

func TestSetupLinkPrintsOnceStoresHash(t *testing.T) {
	getenv, dir := adminEnv(t, nil)
	var out, errb bytes.Buffer
	if code := run([]string{"admin", "setup-link"}, &out, &errb, getenv); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	tok := setupLinkToken(t, out.String())
	if strings.Contains(errb.String(), tok) || strings.Contains(errb.String(), "secret-value-0123456789") {
		t.Fatalf("stderr repeats a secret: %q", errb.String())
	}
	if !strings.Contains(errb.String(), "once") || !strings.Contains(errb.String(), "1 hour") {
		t.Errorf("stderr should say the link works once and for how long: %q", errb.String())
	}

	ctx := context.Background()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, _ := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
	l, err := st.SetupLink(ctx, org.ID, web.HashSecret(tok), time.Now())
	if err != nil {
		t.Fatalf("the stored link is not found by the hash of the printed token: %v", err)
	}
	if l.Person != "" || l.CreatedBy != "cli" || time.Until(l.ExpiresAt) > time.Hour || time.Until(l.ExpiresAt) < 55*time.Minute {
		t.Errorf("link %+v", l)
	}
	// The token itself is nowhere in the data directory.
	files, _ := filepath.Glob(filepath.Join(dir, "privo.db*"))
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		if strings.Contains(string(raw), tok) {
			t.Errorf("%s contains the setup token", filepath.Base(f))
		}
	}
	// A second run replaces the first.
	out.Reset()
	if code := run([]string{"admin", "setup-link"}, &out, &errb, getenv); code != 0 {
		t.Fatal(errb.String())
	}
	if second := setupLinkToken(t, out.String()); second == tok {
		t.Fatal("two runs printed the same token")
	}
	if _, err := st.SetupLink(ctx, org.ID, web.HashSecret(tok), time.Now()); err == nil {
		t.Error("the first link still works after a second one was made")
	}
}

func TestSetupLinkAudited(t *testing.T) {
	getenv, dir := adminEnv(t, nil)
	var out, errb bytes.Buffer
	if code := run([]string{"admin", "setup-link"}, &out, &errb, getenv); code != 0 {
		t.Fatal(errb.String())
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, _ := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
	evs, err := st.RecentAdminEvents(ctx, org.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	tok := setupLinkToken(t, out.String())
	for _, ev := range evs {
		if ev.Action == "setup_link.create" {
			found = true
			if ev.Actor != "cli" || ev.Outcome != "ok" {
				t.Errorf("event %+v", ev)
			}
		}
		if strings.Contains(ev.Action+ev.Actor+ev.ActorID+ev.Client, tok) {
			t.Error("the token is in the audit trail")
		}
	}
	if !found {
		t.Errorf("no setup_link.create event in %+v", evs)
	}
}

func TestSetupLinkPersonFlag(t *testing.T) {
	getenv, dir := adminEnv(t, nil)
	var out, errb bytes.Buffer
	if code := run([]string{"admin", "setup-link", "--person", "Ada Lovelace"}, &out, &errb, getenv); code != 0 {
		t.Fatal(errb.String())
	}
	tok := setupLinkToken(t, out.String())
	ctx := context.Background()
	st, _ := store.Open(ctx, dir)
	t.Cleanup(func() { _ = st.Close() })
	org, _ := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
	l, err := st.SetupLink(ctx, org.ID, web.HashSecret(tok), time.Now())
	if err != nil || l.Person != "Ada Lovelace" {
		t.Fatalf("link %+v, %v", l, err)
	}

	for name, person := range map[string]string{"empty": "  ", "control": "ada\nlovelace", "too long": strings.Repeat("a", 129)} {
		var o, e bytes.Buffer
		if code := run([]string{"admin", "setup-link", "--person", person}, &o, &e, getenv); code != 2 || o.Len() != 0 || !strings.Contains(e.String(), "--person") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, code, o.String(), e.String())
		}
	}
}

func TestSetupLinkNeedsPublicURL(t *testing.T) {
	getenv, dir := adminEnv(t, map[string]string{"PRIVO_PUBLIC_URL": ""})
	var out, errb bytes.Buffer
	if code := run([]string{"admin", "setup-link"}, &out, &errb, getenv); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if out.Len() != 0 || !strings.Contains(errb.String(), "PRIVO_PUBLIC_URL") {
		t.Errorf("stdout %q stderr %q", out.String(), errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "privo.db")); err == nil {
		t.Error("a refused command still created the database")
	}
}

func TestSetupLinkWarnsWhenGitHubIsNotConfigured(t *testing.T) {
	getenv, _ := adminEnv(t, map[string]string{"PRIVO_GITHUB_CLIENT_ID": "", "PRIVO_GITHUB_CLIENT_SECRET": ""})
	var out, errb bytes.Buffer
	if code := run([]string{"admin", "setup-link"}, &out, &errb, getenv); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	setupLinkToken(t, out.String())
	for _, want := range []string{"PRIVO_GITHUB_CLIENT_ID", "PRIVO_GITHUB_CLIENT_SECRET"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr does not name %s: %q", want, errb.String())
		}
	}
}

func TestSetupLinkRejectsBadWebSettings(t *testing.T) {
	getenv, _ := adminEnv(t, map[string]string{"PRIVO_SESSION_MAX_AGE": "not-a-duration"})
	var out, errb bytes.Buffer
	if code := run([]string{"admin", "setup-link"}, &out, &errb, getenv); code != 2 || out.Len() != 0 || !strings.Contains(errb.String(), "PRIVO_SESSION_MAX_AGE") {
		t.Errorf("exit %d stdout %q stderr %q", code, out.String(), errb.String())
	}
}

func TestAdminUsage(t *testing.T) {
	getenv, _ := adminEnv(t, nil)
	for _, args := range [][]string{{"admin"}, {"admin", "nope"}, {"admin", "setup-link", "--bogus"}, {"admin", "setup-link", "extra"}, {"admin", "setup-link", "--person"}} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb, getenv); code != 2 || out.Len() != 0 || !strings.Contains(errb.String(), "usage: privo admin setup-link") {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, code, out.String(), errb.String())
		}
	}
}

func TestAdminReadsTheConfigurationFile(t *testing.T) {
	// admin is a config command: a bad configuration file stops it like it stops serve.
	getenv, _ := adminEnv(t, nil)
	var out, errb bytes.Buffer
	if code := run([]string{"--config", filepath.Join(t.TempDir(), "missing.yaml"), "admin", "setup-link"}, &out, &errb, getenv); code != 2 || out.Len() != 0 {
		t.Errorf("exit %d stdout %q stderr %q", code, out.String(), errb.String())
	}
}

func TestSetupLinkRejectsAPublicURLWithAPath(t *testing.T) {
	getenv, _ := adminEnv(t, map[string]string{"PRIVO_PUBLIC_URL": "https://privo.example.com/privo"})
	var out, errb bytes.Buffer
	if code := run([]string{"admin", "setup-link"}, &out, &errb, getenv); code != 2 || out.Len() != 0 || !strings.Contains(errb.String(), "PRIVO_PUBLIC_URL") {
		t.Errorf("exit %d stdout %q stderr %q", code, out.String(), errb.String())
	}
}

func TestServeRejectsBadWebSettings(t *testing.T) {
	getenv, _ := adminEnv(t, map[string]string{"PRIVO_GITHUB_CLIENT_SECRET": "has spaces in it", "PRIVO_SESSION_IDLE_TIMEOUT": "1ns"})
	var out, errb bytes.Buffer
	if code := run([]string{"serve"}, &out, &errb, getenv); code != 2 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"PRIVO_GITHUB_CLIENT_SECRET", "PRIVO_SESSION_IDLE_TIMEOUT"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr does not name %s: %q", want, errb.String())
		}
	}
	if strings.Contains(errb.String(), "has spaces in it") {
		t.Errorf("stderr repeats the secret: %q", errb.String())
	}
}

func TestWebSettingsDoNotBreakOtherCommands(t *testing.T) {
	// R-03: a mistake in the admin site's settings must not stop the healthcheck
	// or the break-glass token command.
	getenv, _ := adminEnv(t, map[string]string{"PRIVO_SESSION_MAX_AGE": "forever", "PRIVO_LISTEN": "127.0.0.1:1"})
	var out, errb bytes.Buffer
	if code := run([]string{"token", "create", "--name", "x"}, &out, &errb, getenv); code != 0 {
		t.Errorf("token create: exit %d: %s", code, errb.String())
	}
	if code := run([]string{"healthcheck"}, &out, &errb, getenv); code != 1 { // nothing listens: 1, not the configuration error 2
		t.Errorf("healthcheck: exit %d", code)
	}
}

// ---- privo admin invite (ADR 0017)

func inviteToken(t *testing.T, stdout string) string {
	t.Helper()
	line := strings.TrimSpace(stdout)
	const prefix = "https://privo.example.com/admin/invite/"
	if !strings.HasPrefix(line, prefix) || strings.Contains(line, "\n") {
		t.Fatalf("stdout %q is not just the invite URL", stdout)
	}
	tok := strings.TrimPrefix(line, prefix)
	if !strings.HasPrefix(tok, "privo_invite_") {
		t.Fatalf("token %q", tok)
	}
	return tok
}

func TestInvitePrintsOnceStoresHash(t *testing.T) {
	getenv, dir := adminEnv(t, nil)
	var out, errb bytes.Buffer
	if code := run([]string{"admin", "invite", "--person", "ada", "--role", "approver"}, &out, &errb, getenv); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	tok := inviteToken(t, out.String())
	if strings.Contains(errb.String(), tok) || !strings.Contains(errb.String(), "once") || !strings.Contains(errb.String(), "72 hours") {
		t.Fatalf("stderr %q", errb.String())
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, _ := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
	in, err := st.Invite(ctx, org.ID, web.HashSecret(tok), time.Now())
	if err != nil || in.Person != "ada" || !slices.Equal(in.Roles, []string{store.RoleApprover}) || in.CreatedBy != "cli" {
		t.Fatalf("stored invite %+v %v", in, err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "privo.db*"))
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		if strings.Contains(string(raw), tok) {
			t.Errorf("%s contains the invite token", filepath.Base(f))
		}
	}
}

func TestInviteAudited(t *testing.T) {
	getenv, dir := adminEnv(t, nil)
	var out, errb bytes.Buffer
	if code := run([]string{"admin", "invite", "--person", "ada"}, &out, &errb, getenv); code != 0 {
		t.Fatal(errb.String())
	}
	ctx := context.Background()
	st, _ := store.Open(ctx, dir)
	t.Cleanup(func() { _ = st.Close() })
	org, _ := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
	evs, _ := st.RecentAdminEvents(ctx, org.ID, 10)
	if len(evs) == 0 || evs[0].Action != "invite.create:member:ada" || evs[0].Actor != "cli" {
		t.Fatalf("audit %+v", evs)
	}
}

func TestInviteNeedsPersonAndValidRole(t *testing.T) {
	getenv, _ := adminEnv(t, nil)
	for _, args := range [][]string{
		{"admin", "invite"},
		{"admin", "invite", "--person"},
		{"admin", "invite", "--person", "ada", "--role", "owner"},
		{"admin", "invite", "--person", "ada", "--role", "root"},
		{"admin", "invite", "--person", "a\x1bb"},
		{"admin", "invite", "--name", "ada"},
	} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb, getenv); code != 2 || out.Len() != 0 {
			t.Errorf("%q: exit %d, stdout %q", args, code, out.String())
		}
	}
	noURL, _ := adminEnv(t, map[string]string{"PRIVO_PUBLIC_URL": ""})
	var out, errb bytes.Buffer
	if code := run([]string{"admin", "invite", "--person", "ada"}, &out, &errb, noURL); code != 2 || !strings.Contains(errb.String(), "PRIVO_PUBLIC_URL") {
		t.Errorf("without a public URL: exit %d %q", code, errb.String())
	}
	// An existing member cannot be invited again.
	getenv2, _ := adminEnv(t, nil)
	_ = run([]string{"admin", "invite", "--person", "ada"}, &out, &errb, getenv2)
	out.Reset()
	errb.Reset()
	if code := run([]string{"admin", "invite", "--person", "ada"}, &out, &errb, getenv2); code != 0 {
		t.Errorf("a second invite for a person who has not joined yet replaces the first: exit %d %s", code, errb.String())
	}
}

// F24 second review R-02: the break-glass token command leaves a trace, in
// the same transaction as the token.
func TestTokenCreateIsAudited(t *testing.T) {
	getenv, dir := adminEnv(t, nil)
	var out, errb bytes.Buffer
	if code := run([]string{"token", "create", "--name", "break-glass", "--role", "owner", "--kind", "control"}, &out, &errb, getenv); code != 0 {
		t.Fatal(errb.String())
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, _ := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
	evs, err := st.RecentAdminEvents(ctx, org.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Action == "token.create:control:owner:break-glass" {
			if ev.Actor != "cli" || ev.ActorID != "cli" || ev.Client != "cli" || ev.Outcome != "ok" {
				t.Fatalf("audited as %+v", ev)
			}
			if strings.Contains(ev.Action, strings.TrimSpace(out.String())) {
				t.Fatal("the secret is in the audit")
			}
			return
		}
	}
	t.Fatalf("no token.create event in %+v", evs)
}
