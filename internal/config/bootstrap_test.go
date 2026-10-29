package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"privo.club/privo/internal/token"
)

func TestBootstrapTokenConfig(t *testing.T) {
	tok, err := token.New()
	if err != nil {
		t.Fatal(err)
	}
	other, _ := token.New()

	t.Run("unset means no bootstrap", func(t *testing.T) {
		bt, err := LoadBootstrapToken(env(nil))
		if err != nil || bt != "" {
			t.Fatalf("token %v err %v", bt, err)
		}
	})

	t.Run("from the environment", func(t *testing.T) {
		bt, err := LoadBootstrapToken(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN": "  " + tok.Plain + "\n"}))
		if err != nil || bt.Reveal() != tok.Plain {
			t.Fatalf("reveal %q err %v", bt.Reveal(), err)
		}
	})

	t.Run("from a file, which wins over the variable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bootstrap")
		if err := os.WriteFile(path, []byte(tok.Plain+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		bt, err := LoadBootstrapToken(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN_FILE": path, "PRIVO_BOOTSTRAP_TOKEN": other.Plain}))
		if err != nil || bt.Reveal() != tok.Plain {
			t.Fatalf("reveal %q err %v", bt.Reveal(), err)
		}
	})

	t.Run("a malformed value fails without repeating it", func(t *testing.T) {
		_, err := LoadBootstrapToken(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN": "privo_pat_leak-marker-9921"}))
		if err == nil || !strings.Contains(err.Error(), "PRIVO_BOOTSTRAP_TOKEN") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), "leak-marker-9921") {
			t.Fatalf("error repeats the value: %v", err)
		}
		if errors.Is(err, ErrBootstrapUnreadable) {
			t.Fatalf("a malformed value is not an unreadable file: %v", err)
		}
	})

	t.Run("an unreadable file fails, names the variable and says so", func(t *testing.T) {
		_, err := LoadBootstrapToken(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN_FILE": filepath.Join(t.TempDir(), "missing")}))
		if err == nil || !strings.Contains(err.Error(), "PRIVO_BOOTSTRAP_TOKEN_FILE") || !errors.Is(err, ErrBootstrapUnreadable) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an empty file is an error, not a silent skip", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty")
		_ = os.WriteFile(path, []byte("\n"), 0o600)
		if _, err := LoadBootstrapToken(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN_FILE": path})); !errors.Is(err, ErrBootstrapUnreadable) {
			t.Fatalf("empty bootstrap file: err = %v", err)
		}
	})

	t.Run("the secret never prints", func(t *testing.T) {
		bt, err := LoadBootstrapToken(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN": tok.Plain}))
		if err != nil {
			t.Fatal(err)
		}
		held := struct{ BootstrapToken Secret }{bt}
		var buf bytes.Buffer
		slog.New(slog.NewJSONHandler(&buf, nil)).Info("cfg", "token", bt, "held", held)
		for name, out := range map[string]string{
			"%v":     fmt.Sprintf("%v", held),
			"%+v":    fmt.Sprintf("%+v", held),
			"%#v":    fmt.Sprintf("%#v", held),
			"Sprint": fmt.Sprint(bt),
			"slog":   buf.String(),
		} {
			if strings.Contains(out, tok.Plain) {
				t.Errorf("%s output contains the token: %s", name, out)
			}
		}
	})
}

// R-03 (independent review, 2026-10-29): Load is shared by every subcommand, so
// a leftover or broken bootstrap variable must not fail it. The healthcheck
// that Docker runs every 15 s and the break-glass `token create` depend on it.
func TestLoadIgnoresBootstrapVariables(t *testing.T) {
	_, err := Load(env(map[string]string{
		"PRIVO_BOOTSTRAP_TOKEN":      "not-a-token",
		"PRIVO_BOOTSTRAP_TOKEN_FILE": filepath.Join(t.TempDir(), "missing"),
	}))
	if err != nil {
		t.Fatalf("Load failed on the bootstrap variables: %v", err)
	}
}
