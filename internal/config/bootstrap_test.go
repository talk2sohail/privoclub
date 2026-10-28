package config

import (
	"bytes"
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
		cfg, err := Load(env(nil))
		if err != nil || cfg.BootstrapToken != "" {
			t.Fatalf("cfg %v err %v", cfg.BootstrapToken, err)
		}
	})

	t.Run("from the environment", func(t *testing.T) {
		cfg, err := Load(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN": "  " + tok.Plain + "\n"}))
		if err != nil || cfg.BootstrapToken.Reveal() != tok.Plain {
			t.Fatalf("reveal %q err %v", cfg.BootstrapToken.Reveal(), err)
		}
	})

	t.Run("from a file, which wins over the variable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bootstrap")
		if err := os.WriteFile(path, []byte(tok.Plain+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN_FILE": path, "PRIVO_BOOTSTRAP_TOKEN": other.Plain}))
		if err != nil || cfg.BootstrapToken.Reveal() != tok.Plain {
			t.Fatalf("reveal %q err %v", cfg.BootstrapToken.Reveal(), err)
		}
	})

	t.Run("a malformed value fails without repeating it", func(t *testing.T) {
		_, err := Load(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN": "privo_pat_leak-marker-9921"}))
		if err == nil || !strings.Contains(err.Error(), "PRIVO_BOOTSTRAP_TOKEN") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), "leak-marker-9921") {
			t.Fatalf("error repeats the value: %v", err)
		}
	})

	t.Run("an unreadable file fails and names the variable", func(t *testing.T) {
		_, err := Load(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN_FILE": filepath.Join(t.TempDir(), "missing")}))
		if err == nil || !strings.Contains(err.Error(), "PRIVO_BOOTSTRAP_TOKEN_FILE") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an empty file is an error, not a silent skip", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty")
		_ = os.WriteFile(path, []byte("\n"), 0o600)
		if _, err := Load(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN_FILE": path})); err == nil {
			t.Fatal("empty bootstrap file accepted")
		}
	})

	t.Run("the secret never prints", func(t *testing.T) {
		cfg, err := Load(env(map[string]string{"PRIVO_BOOTSTRAP_TOKEN": tok.Plain}))
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		slog.New(slog.NewJSONHandler(&buf, nil)).Info("cfg", "token", cfg.BootstrapToken, "cfg", cfg)
		for name, out := range map[string]string{
			"%v":     fmt.Sprintf("%v", cfg),
			"%+v":    fmt.Sprintf("%+v", cfg),
			"%#v":    fmt.Sprintf("%#v", cfg),
			"Sprint": fmt.Sprint(cfg.BootstrapToken),
			"slog":   buf.String(),
		} {
			if strings.Contains(out, tok.Plain) {
				t.Errorf("%s output contains the token: %s", name, out)
			}
		}
	})
}
