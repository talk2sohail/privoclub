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
