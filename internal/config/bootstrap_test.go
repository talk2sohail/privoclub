package config

import (
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
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

// handMade builds a well-formed token from an id and a secret of the caller's
// choosing, as a script or an operator could: the checksum is public.
func handMade(id, secret string) string {
	body := token.Prefix + id + "_" + secret
	return fmt.Sprintf("%s%08x", body, crc32.ChecksumIEEE([]byte(body)))
}

// R-10: a well-formed token with no entropy is refused at startup, and the
// error neither repeats it nor blames the file.
func TestBootstrapTokenWeakIsRefused(t *testing.T) {
	weak := handMade("aaaaaaaaaaaaaaaa", strings.Repeat("a", 52))
	if _, _, err := token.Parse(weak); err != nil {
		t.Fatalf("the test token must be well-formed: %v", err)
	}
	path := filepath.Join(t.TempDir(), "bootstrap")
	if err := os.WriteFile(path, []byte(weak+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, vars := range map[string]map[string]string{
		"environment": {"PRIVO_BOOTSTRAP_TOKEN": weak},
		"file":        {"PRIVO_BOOTSTRAP_TOKEN_FILE": path},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadBootstrapToken(env(vars))
			if err == nil {
				t.Fatal("a token with no entropy was accepted")
			}
			if !strings.Contains(err.Error(), "privo token generate") || !strings.Contains(err.Error(), "PRIVO_BOOTSTRAP_TOKEN") {
				t.Errorf("err = %v, want it to name the variable and say how to make a token", err)
			}
			if strings.Contains(err.Error(), strings.Repeat("a", 20)) {
				t.Errorf("error repeats the secret: %v", err)
			}
			if errors.Is(err, ErrBootstrapUnreadable) {
				t.Errorf("a weak token is not an unreadable file: %v", err)
			}
		})
	}
}

// R-08: the warning about the environment needs to know where the token came from.
func TestBootstrapTokenSource(t *testing.T) {
	for name, tc := range map[string]struct {
		vars map[string]string
		want string
	}{
		"nothing":       {nil, ""},
		"blank values":  {map[string]string{"PRIVO_BOOTSTRAP_TOKEN": "  ", "PRIVO_BOOTSTRAP_TOKEN_FILE": " "}, ""},
		"the variable":  {map[string]string{"PRIVO_BOOTSTRAP_TOKEN": "x"}, "PRIVO_BOOTSTRAP_TOKEN"},
		"the file":      {map[string]string{"PRIVO_BOOTSTRAP_TOKEN_FILE": "/run/secrets/b"}, "PRIVO_BOOTSTRAP_TOKEN_FILE"},
		"the file wins": {map[string]string{"PRIVO_BOOTSTRAP_TOKEN_FILE": "/run/secrets/b", "PRIVO_BOOTSTRAP_TOKEN": "x"}, "PRIVO_BOOTSTRAP_TOKEN_FILE"},
	} {
		if got := BootstrapTokenSource(env(tc.vars)); got != tc.want {
			t.Errorf("%s: source = %q, want %q", name, got, tc.want)
		}
	}
}
