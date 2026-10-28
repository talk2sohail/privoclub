package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
