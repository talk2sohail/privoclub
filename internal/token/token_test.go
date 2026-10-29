package token

import (
	"strings"
	"testing"
)

func TestNewAndParse(t *testing.T) {
	tok, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Plain, Prefix) || len(tok.ID) != idLen {
		t.Fatalf("token %q id %q", tok.Plain, tok.ID)
	}
	id, secret, err := Parse(tok.Plain)
	if err != nil || id != tok.ID {
		t.Fatalf("Parse: %q %v", id, err)
	}
	if !Verify(secret, tok.SecretHash) {
		t.Fatal("secret does not verify against its hash")
	}
	if strings.Contains(tok.SecretHash, secret) {
		t.Fatal("hash contains the secret")
	}
	other, _ := New()
	if other.Plain == tok.Plain || other.ID == tok.ID {
		t.Fatal("tokens repeat")
	}
}

func TestParseRejects(t *testing.T) {
	tok, _ := New()
	cases := map[string]string{
		"empty":        "",
		"other prefix": "ghp_" + tok.Plain[len(Prefix):],
		"truncated":    tok.Plain[:len(tok.Plain)-1],
		"extended":     tok.Plain + "a",
		"bad crc":      tok.Plain[:len(tok.Plain)-1] + flip(tok.Plain[len(tok.Plain)-1]),
		"bad secret":   tok.Plain[:len(Prefix)+idLen+1] + flip(tok.Plain[len(Prefix)+idLen+1]) + tok.Plain[len(Prefix)+idLen+2:],
		"uppercase":    strings.ToUpper(tok.Plain),
	}
	for name, s := range cases {
		if _, _, err := Parse(s); err == nil {
			t.Errorf("%s: accepted %q", name, s)
		}
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	a, _ := New()
	b, _ := New()
	_, secretB, _ := Parse(b.Plain)
	if Verify(secretB, a.SecretHash) {
		t.Fatal("another token's secret verified")
	}
}

func flip(c byte) string {
	if c == 'a' {
		return "b"
	}
	return "a"
}

// R-10 (independent review, 2026-10-29): a well-formed token made by hand has
// no entropy, and as the bootstrap token it was an owner credential for 24
// hours. Weak is a cheap check for the careless case; it cannot prove that a
// secret is random, and a token from `privo token generate` is.
func TestWeak(t *testing.T) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	for name, secret := range map[string]string{
		"one symbol":                strings.Repeat("a", 52),
		"two symbols":               strings.Repeat("ab", 26),
		"a pattern of four":         strings.Repeat("k2q7", 13),
		"the alphabet in order":     alphabet + alphabet[:20],
		"the alphabet backwards":    "765432zyxwvutsrqponmlkjihgfedcba",
		"a run of eight":            "w2k7mq4xz5dbn3rt6pjyvgqqqqqqqqhaecfus2l7o4i6c5e3f7h2",
		"fifteen different symbols": strings.Repeat("abcdefghijklmno", 4)[:52],
		"nothing":                   "",
	} {
		if !Weak(secret) {
			t.Errorf("%s: %q was accepted", name, secret)
		}
	}
	for name, secret := range map[string]string{
		"a made-up random one":    "w2k7mq4xz5dbn3rt6pjyvghaecfus2l7o4i6c5e3f7h2d4g6b5n3",
		"a run of seven is fine":  "w2k7mq4xz5dbn3rt6pjyvgqqqqqqqhaecfus2l7o4i6c5e3f7h2d",
		"sixteen symbols is fine": strings.Repeat("w2k7mq4xz5dbn3rt", 4)[:52],
	} {
		if Weak(secret) {
			t.Errorf("%s: %q was refused", name, secret)
		}
	}
	// A token from New is never weak: a false alarm here would stop a first boot.
	for i := 0; i < 2000; i++ {
		tok, err := New()
		if err != nil {
			t.Fatal(err)
		}
		_, secret, _ := Parse(tok.Plain)
		if Weak(secret) {
			t.Fatalf("a generated secret was refused: %q", secret)
		}
	}
}
