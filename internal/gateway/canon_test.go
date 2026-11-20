package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalEquivalentFormsMatch(t *testing.T) {
	want := `{"a":[1,{"x":true,"y":null}],"repo":"prod"}`
	for _, in := range []string{
		`{"repo":"prod","a":[1,{"y":null,"x":true}]}`,
		`{ "repo" : "prod" , "a" : [ 1 , { "x" : true , "y" : null } ] }`,
		"{\n\t\"a\":[1,{\"x\":true,\"y\":null}],\"repo\":\"prod\"}\n",
	} {
		got, err := Canonical(json.RawMessage(in))
		if err != nil || string(got) != want {
			t.Errorf("Canonical(%s) = %s, %v", in, got, err)
		}
	}
}

func TestCanonicalRejects(t *testing.T) {
	for _, in := range []string{
		`{"repo":"prod","repo":"sandbox"}`,
		`{"a":{"k":1,"k":2}}`,
		`[1,2]`, `"x"`, `3`, `{"a":1} {"b":2}`, `{"a":`, `{"a":1}x`,
	} {
		if got, err := Canonical(json.RawMessage(in)); err == nil {
			t.Errorf("Canonical(%s) accepted as %s", in, got)
		}
	}
}

func TestCanonicalEmptyAndNumbers(t *testing.T) {
	for _, in := range []string{"", "  ", "{}", "null", " null "} {
		if got, _ := Canonical(json.RawMessage(in)); string(got) != "{}" {
			t.Errorf("%q -> %s", in, got)
		}
	}
	got, _ := Canonical(json.RawMessage(`{"n":12345678901234567890,"f":1.50}`))
	if string(got) != `{"f":1.50,"n":12345678901234567890}` {
		t.Fatalf("numbers changed: %s", got)
	}
}

// FuzzCanonical checks that canonical output is stable and itself canonical.
func FuzzCanonical(f *testing.F) {
	for _, s := range []string{`{"a":1}`, `{"b":[1,"x",{"c":null}],"a":true}`, `{}`, `{"k":"é"}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := Canonical(json.RawMessage(in))
		if err != nil {
			return
		}
		again, err := Canonical(out)
		if err != nil || string(again) != string(out) {
			t.Fatalf("not idempotent: %s -> %s (%v)", out, again, err)
		}
		if !json.Valid(out) {
			t.Fatalf("invalid JSON: %s", out)
		}
	})
}

// ADR 0024 review: an exponent beyond 9 digits is refused, so rules compare
// every number they see exactly.
func TestCanonicalRefusesHugeExponents(t *testing.T) {
	if _, err := Canonical(json.RawMessage(`{"n":1e1234567890}`)); err == nil {
		t.Fatal("a 10-digit exponent was accepted")
	}
	if got, err := Canonical(json.RawMessage(`{"n":1e123456789,"m":1000.00000000000000000000000000000000000000000000000000000000000000000}`)); err != nil ||
		!strings.Contains(string(got), "1000.0000") {
		t.Fatalf("ordinary long numbers: %s %v", got, err)
	}
}
