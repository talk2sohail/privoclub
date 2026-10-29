package vault

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

var base = Binding{OrgID: "org1", CredentialID: "cred1", OwnerID: "user1", ServerID: "srv1", Purpose: "env", KeyVersion: 1, RowVersion: 1}

func TestSealOpenRoundTrip(t *testing.T) {
	dek := mustKey(t)
	ct, err := Seal(dek, []byte("ghp_secret"), base)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("ghp_secret")) {
		t.Fatal("ciphertext contains plaintext")
	}
	pt, err := Open(dek, ct, base)
	if err != nil || string(pt) != "ghp_secret" {
		t.Fatalf("Open = %q, %v", pt, err)
	}
	ct2, _ := Seal(dek, []byte("ghp_secret"), base)
	if bytes.Equal(ct, ct2) {
		t.Fatal("two seals produced identical ciphertext (nonce reuse)")
	}
}

func TestOpenRejectsAnyBindingChange(t *testing.T) {
	dek := mustKey(t)
	ct, _ := Seal(dek, []byte("secret"), base)
	changes := map[string]func(b *Binding){
		"org":         func(b *Binding) { b.OrgID = "org2" },
		"credential":  func(b *Binding) { b.CredentialID = "cred2" },
		"owner":       func(b *Binding) { b.OwnerID = "user2" },
		"server":      func(b *Binding) { b.ServerID = "srv2" },
		"purpose":     func(b *Binding) { b.Purpose = "header" },
		"key version": func(b *Binding) { b.KeyVersion = 2 },
		"row version": func(b *Binding) { b.RowVersion = 2 },
		// Field boundaries must not be ambiguous: "ab"+"c" vs "a"+"bc".
		"boundary": func(b *Binding) { b.OwnerID, b.ServerID = "user1s", "rv1" },
	}
	for name, change := range changes {
		b := base
		change(&b)
		if _, err := Open(dek, ct, b); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%s change: err = %v, want ErrDecrypt", name, err)
		}
	}
}

func TestOpenRejectsTamperingAndWrongKey(t *testing.T) {
	dek := mustKey(t)
	ct, _ := Seal(dek, []byte("secret"), base)
	for i := range ct {
		bad := bytes.Clone(ct)
		bad[i] ^= 0x01
		if _, err := Open(dek, bad, base); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("flipping byte %d was not detected", i)
		}
	}
	if _, err := Open(mustKey(t), ct, base); !errors.Is(err, ErrDecrypt) {
		t.Fatal("wrong DEK opened the ciphertext")
	}
	if _, err := Open(dek, ct[:10], base); !errors.Is(err, ErrDecrypt) {
		t.Fatal("truncated ciphertext opened")
	}
}

func TestDEKWrapping(t *testing.T) {
	kek, dek := mustKey(t), mustKey(t)
	w, err := WrapDEK(kek, dek, "org1", 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnwrapDEK(kek, w, "org1", 1)
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("unwrap: %v", err)
	}
	if _, err := UnwrapDEK(kek, w, "org2", 1); !errors.Is(err, ErrDecrypt) {
		t.Fatal("DEK unwrapped for another org")
	}
	if _, err := UnwrapDEK(kek, w, "org1", 2); !errors.Is(err, ErrDecrypt) {
		t.Fatal("DEK unwrapped as another key version")
	}
	if _, err := UnwrapDEK(mustKey(t), w, "org1", 1); !errors.Is(err, ErrDecrypt) {
		t.Fatal("DEK unwrapped with the wrong KEK")
	}
	if _, err := WrapDEK(kek[:16], dek, "org1", 1); err == nil {
		t.Fatal("short KEK accepted")
	}
}

func TestSealValidatesInputs(t *testing.T) {
	if _, err := Seal(make([]byte, 16), []byte("x"), base); err == nil {
		t.Fatal("short DEK accepted")
	}
	if _, err := Seal(mustKey(t), []byte("x"), Binding{CredentialID: "c"}); err == nil {
		t.Fatal("binding without org accepted")
	}
}

func TestLoadEnvKeyProvider(t *testing.T) {
	key := mustKey(t)
	enc := base64.StdEncoding.EncodeToString(key)

	t.Run("file preferred and env scrubbed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "key")
		if err := os.WriteFile(path, []byte(enc+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PRIVO_MASTER_KEY_FILE", path)
		t.Setenv("PRIVO_MASTER_KEY", base64.StdEncoding.EncodeToString(mustKey(t)))
		p, err := LoadEnvKeyProvider()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(p.Current(), key) {
			t.Fatal("file key not preferred")
		}
		if os.Getenv("PRIVO_MASTER_KEY") != "" {
			t.Fatal("PRIVO_MASTER_KEY left in the environment")
		}
	})

	t.Run("env with previous key", func(t *testing.T) {
		prev := mustKey(t)
		t.Setenv("PRIVO_MASTER_KEY_FILE", "")
		t.Setenv("PRIVO_MASTER_KEY", base64.RawURLEncoding.EncodeToString(key))
		t.Setenv("PRIVO_MASTER_KEY_PREVIOUS", base64.StdEncoding.EncodeToString(prev))
		p, err := LoadEnvKeyProvider()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(p.Current(), key) || !bytes.Equal(p.Previous(), prev) {
			t.Fatal("keys not loaded")
		}
	})

	t.Run("missing and malformed", func(t *testing.T) {
		t.Setenv("PRIVO_MASTER_KEY_FILE", "")
		t.Setenv("PRIVO_MASTER_KEY", "")
		if _, err := LoadEnvKeyProvider(); err == nil {
			t.Fatal("missing key accepted")
		}
		t.Setenv("PRIVO_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 16)))
		if _, err := LoadEnvKeyProvider(); err == nil {
			t.Fatal("16-byte key accepted")
		}
		t.Setenv("PRIVO_MASTER_KEY", "not base64 !!")
		if _, err := LoadEnvKeyProvider(); err == nil {
			t.Fatal("garbage key accepted")
		}
	})
}

func TestLoadKeyProviderUsesTheGivenLookup(t *testing.T) {
	key, prev := mustKey(t), mustKey(t)
	dir := t.TempDir()
	kf, pf := filepath.Join(dir, "k"), filepath.Join(dir, "p")
	if err := os.WriteFile(kf, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pf, []byte(base64.StdEncoding.EncodeToString(prev)), 0o600); err != nil {
		t.Fatal(err)
	}
	// The process environment says something else; only the lookup counts.
	t.Setenv("PRIVO_MASTER_KEY_FILE", "/nonexistent")
	t.Setenv("PRIVO_MASTER_KEY", base64.StdEncoding.EncodeToString(mustKey(t)))
	vals := map[string]string{"PRIVO_MASTER_KEY_FILE": kf, "PRIVO_MASTER_KEY_PREVIOUS_FILE": pf}
	p, err := LoadKeyProvider(func(k string) string { return vals[k] })
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.Current(), key) || !bytes.Equal(p.Previous(), prev) {
		t.Fatal("the keys named by the lookup were not loaded")
	}
	if _, err := LoadKeyProvider(func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "PRIVO_MASTER_KEY_FILE") {
		t.Fatalf("missing key: %v, want an error naming PRIVO_MASTER_KEY_FILE", err)
	}
}
