package gateway

import (
	"encoding/json"
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
