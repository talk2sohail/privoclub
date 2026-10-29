package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/config"
	"privo.club/privo/internal/control"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/token"
)

func TestTokenGenerate(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-created")
	getenv := func(k string) string {
		if k == "PRIVO_DATA_DIR" {
			return missing
		}
		return ""
	}
	gen := func() (string, string) {
		var out, errb bytes.Buffer
		if code := run([]string{"token", "generate"}, &out, &errb, getenv); code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
		return strings.TrimSpace(out.String()), errb.String()
	}
	first, stderr := gen()
	id, secret, err := token.Parse(first)
	if err != nil {
		t.Fatalf("output %q is not a token: %v", first, err)
	}
	if strings.Contains(stderr, secret) || strings.Contains(stderr, first) {
		t.Fatalf("stderr repeats the secret: %q", stderr)
	}
	if !strings.Contains(stderr, "PRIVO_BOOTSTRAP_TOKEN") || !strings.Contains(stderr, id) {
		t.Fatalf("stderr should say what to do with it and show the id: %q", stderr)
	}
	if second, _ := gen(); second == first {
		t.Fatal("two runs produced the same token")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("token generate touched the data directory: %v", err)
	}
}

func TestTokenGenerateRejectsArguments(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"token", "generate", "--role", "owner"}, &out, &errb, func(string) string { return "" }); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if out.Len() != 0 {
		t.Fatalf("printed %q", out.String())
	}
}

func TestServeRejectsMalformedBootstrapToken(t *testing.T) {
	var out, errb bytes.Buffer
	getenv := func(k string) string {
		if k == "PRIVO_BOOTSTRAP_TOKEN" {
			return "privo_pat_startup-marker-5512"
		}
		return ""
	}
	if code := run([]string{"serve"}, &out, &errb, getenv); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errb.String(), "PRIVO_BOOTSTRAP_TOKEN") || strings.Contains(errb.String(), "startup-marker-5512") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestInstallBootstrapAtStartup(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, err := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := token.New()
	_, secret, _ := token.Parse(tok.Plain)

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	if err := installBootstrap(ctx, st, org.ID, tok.Plain, logger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), tok.ID) || !strings.Contains(logs.String(), "expires") {
		t.Fatalf("log should name the token id and expiry: %s", logs.String())
	}
	if _, err := st.TokenByID(ctx, tok.ID); err != nil {
		t.Fatalf("token not installed: %v", err)
	}

	// Restart with the variable still set: nothing changes, and the log says to remove it.
	logs.Reset()
	if err := installBootstrap(ctx, st, org.ID, tok.Plain, logger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "PRIVO_BOOTSTRAP_TOKEN") {
		t.Fatalf("second boot should warn to remove the variable: %s", logs.String())
	}
	for _, out := range []string{logs.String()} {
		if strings.Contains(out, secret) || strings.Contains(out, tok.Plain) {
			t.Fatalf("log contains the secret: %s", out)
		}
	}

	// A malformed value is an error and never logged.
	logs.Reset()
	if err := installBootstrap(ctx, st, org.ID, "garbage-marker-0042", logger); err == nil || strings.Contains(err.Error()+logs.String(), "garbage-marker-0042") {
		t.Fatalf("err %v logs %s", err, logs.String())
	}
}

// R-03 (independent review, 2026-10-29): what an unreadable bootstrap file
// means depends on the organisation. The first boot must not start without the
// token it was told to install; a leftover on an organisation that is set up
// already must not stop it.
func TestBootstrapAtStartup(t *testing.T) {
	ctx := context.Background()
	unreadable := errors.Join(config.ErrBootstrapUnreadable, errors.New("PRIVO_BOOTSTRAP_TOKEN_FILE: open /run/secrets/bootstrap: no such file"))
	setup := func(t *testing.T) (*store.Store, store.OrgID, *bytes.Buffer, *slog.Logger) {
		t.Helper()
		st, err := store.Open(ctx, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		org, err := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
		if err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		return st, org.ID, &logs, slog.New(slog.NewTextHandler(&logs, nil))
	}

	t.Run("a first boot that cannot read its token refuses to start", func(t *testing.T) {
		st, org, _, logger := setup(t)
		err := bootstrapAtStartup(ctx, st, org, bootstrapInput{err: unreadable}, logger)
		if err == nil || !strings.Contains(err.Error(), "PRIVO_BOOTSTRAP_TOKEN_FILE") {
			t.Fatalf("err = %v, want a failure that names the file variable", err)
		}
	})
	t.Run("after the token was used, a missing file is only a warning", func(t *testing.T) {
		st, org, logs, logger := setup(t)
		tok, _ := token.New()
		if err := bootstrapAtStartup(ctx, st, org, bootstrapInput{token: config.Secret(tok.Plain)}, logger); err != nil {
			t.Fatal(err)
		}
		logs.Reset()
		if err := bootstrapAtStartup(ctx, st, org, bootstrapInput{err: unreadable}, logger); err != nil {
			t.Fatalf("a leftover variable stopped the start: %v", err)
		}
		if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "PRIVO_BOOTSTRAP_TOKEN_FILE") {
			t.Fatalf("log should warn about the leftover: %s", logs.String())
		}
	})
	t.Run("an organisation with control tokens ignores a missing file", func(t *testing.T) {
		st, org, _, logger := setup(t)
		plain, _, err := control.IssueToken(ctx, st, org, control.TokenSpec{Name: "owner", Role: store.RoleOwner, Kind: store.KindControl, TTL: time.Hour})
		if err != nil || plain == "" {
			t.Fatal(err)
		}
		if err := bootstrapAtStartup(ctx, st, org, bootstrapInput{err: unreadable}, logger); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no bootstrap configured does nothing", func(t *testing.T) {
		st, org, logs, logger := setup(t)
		if err := bootstrapAtStartup(ctx, st, org, bootstrapInput{}, logger); err != nil || logs.Len() != 0 {
			t.Fatalf("err %v, log %q", err, logs.String())
		}
	})
}

// The healthcheck that Docker runs every 15 s and the break-glass `token
// create` must not depend on a bootstrap variable that is left over (R-03).
func TestHealthcheckAndTokenCreateIgnoreBootstrapVariables(t *testing.T) {
	dataDir := t.TempDir()
	getenv := func(k string) string {
		switch k {
		case "PRIVO_BOOTSTRAP_TOKEN":
			return "not-a-token"
		case "PRIVO_BOOTSTRAP_TOKEN_FILE":
			return filepath.Join(dataDir, "missing")
		case "PRIVO_DATA_DIR":
			return dataDir
		case "PRIVO_LISTEN":
			return "127.0.0.1:1"
		}
		return ""
	}
	var out, errb bytes.Buffer
	if code := run([]string{"healthcheck"}, &out, &errb, getenv); code != 1 {
		t.Errorf("healthcheck exit %d (1 means: configuration fine, nothing listening): %s", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"token", "create", "--name", "break-glass"}, &out, &errb, getenv); code != 0 || !strings.HasPrefix(out.String(), "privo_pat_") {
		t.Errorf("token create exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
}

// R-08 (independent review, 2026-10-29): clearing the variable from the
// process environment does not remove it from /proc/<pid>/environ, so the
// only honest mitigation is the file form. A token given as a plain variable
// is a warning, as the master key is.
func TestBootstrapTokenFromEnvironmentWarns(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		source   string
		wantWarn bool
	}{
		{"PRIVO_BOOTSTRAP_TOKEN", true},
		{"PRIVO_BOOTSTRAP_TOKEN_FILE", false},
	} {
		t.Run(tc.source, func(t *testing.T) {
			st, err := store.Open(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			org, err := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
			if err != nil {
				t.Fatal(err)
			}
			tok, _ := token.New()
			_, secret, _ := token.Parse(tok.Plain)
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			in := bootstrapInput{token: config.Secret(tok.Plain), source: tc.source}
			if err := bootstrapAtStartup(ctx, st, org.ID, in, logger); err != nil {
				t.Fatal(err)
			}
			out := logs.String()
			warned := strings.Contains(out, "level=WARN") && strings.Contains(out, "PRIVO_BOOTSTRAP_TOKEN_FILE") && strings.Contains(out, "/proc/")
			if warned != tc.wantWarn {
				t.Errorf("warned = %v, want %v; log: %s", warned, tc.wantWarn, out)
			}
			if strings.Contains(out, secret) || strings.Contains(out, tok.Plain) {
				t.Errorf("log contains the token: %s", out)
			}
		})
	}
}
