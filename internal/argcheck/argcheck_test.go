package argcheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func schemaOf(t *testing.T, src string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(src), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

const strict = `{"type":"object","properties":{"text":{"type":"string"},"n":{"type":"integer","minimum":1}},"required":["text"],"additionalProperties":false}`

func TestMatchingArgumentsPass(t *testing.T) {
	var c Checker
	for _, args := range []string{`{"text":"hi"}`, `{"n":3,"text":""}`} {
		if err := c.Check("h", schemaOf(t, strict), json.RawMessage(args)); err != nil {
			t.Errorf("%s: %v", args, err)
		}
	}
}

func TestMismatchesAreRefused(t *testing.T) {
	var c Checker
	for args, want := range map[string]string{
		`{"text":5}`:             "type",
		`{}`:                     "required",
		`{"text":"a","x":1}`:     "additional properties",
		`{"text":"a","n":0}`:     "minimum",
		`{"text":"a","n":1.5}`:   "type",
		`{"text":"a","n":1e999}`: "out of range",
	} {
		err := c.Check("h", schemaOf(t, strict), json.RawMessage(args))
		var m *MismatchError
		if !errors.As(err, &m) {
			t.Errorf("%s: %v, want a mismatch", args, err)
			continue
		}
		if !strings.Contains(m.Reason, want) {
			t.Errorf("%s: reason %q does not name %q", args, m.Reason, want)
		}
		if errors.Is(err, ErrUnreadable) {
			t.Errorf("%s: a mismatch is not an unreadable schema", args)
		}
	}
}

func TestDraft07AndLocalRefs(t *testing.T) {
	var c Checker
	for name, src := range map[string]string{
		"draft-07 (zod)":           `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"x":{"$ref":"#/definitions/X"}},"definitions":{"X":{"type":"integer"}},"additionalProperties":false}`,
		"2020-12 $defs (pydantic)": `{"type":"object","properties":{"x":{"$ref":"#/$defs/X"}},"$defs":{"X":{"type":"integer"}},"title":"args"}`,
		"OpenAPI extras":           `{"type":"object","properties":{"x":{"type":"integer","nullable":true,"x-vendor":1}}}`,
	} {
		if err := c.Check(name, schemaOf(t, src), json.RawMessage(`{"x":7}`)); err != nil {
			t.Errorf("%s, matching: %v", name, err)
		}
		var m *MismatchError
		if err := c.Check(name, schemaOf(t, src), json.RawMessage(`{"x":"7"}`)); !errors.As(err, &m) {
			t.Errorf("%s, a string for an integer: %v", name, err)
		}
	}
}

// Servers spell the two supported drafts with or without the trailing "#",
// and over http or https; all of them are read, with the right draft's rules.
func TestSchemaVersionSpellings(t *testing.T) {
	var c Checker
	for _, v := range []string{
		"http://json-schema.org/draft-07/schema#", "https://json-schema.org/draft-07/schema#",
		"http://json-schema.org/draft-07/schema", "https://json-schema.org/draft-07/schema",
	} {
		// In draft-07 a $ref's siblings are ignored, so "type":"string" does
		// not apply; under 2020-12 rules {"x":7} would be refused.
		src := fmt.Sprintf(`{"$schema":%q,"type":"object","properties":{"x":{"$ref":"#/definitions/X","type":"string"}},"definitions":{"X":{"type":"integer"}}}`, v)
		if err := c.Check(v, schemaOf(t, src), json.RawMessage(`{"x":7}`)); err != nil {
			t.Errorf("%s: %v, want draft-07 rules", v, err)
		}
	}
	for _, v := range []string{
		"https://json-schema.org/draft/2020-12/schema", "https://json-schema.org/draft/2020-12/schema#",
		"http://json-schema.org/draft/2020-12/schema", "http://json-schema.org/draft/2020-12/schema#",
	} {
		src := fmt.Sprintf(`{"$schema":%q,"type":"object","properties":{"x":{"$ref":"#/$defs/X","type":"string"}},"$defs":{"X":{"type":"integer"}}}`, v)
		var m *MismatchError
		if err := c.Check(v, schemaOf(t, src), json.RawMessage(`{"x":7}`)); !errors.As(err, &m) {
			t.Errorf("%s: %v, want 2020-12 rules (the sibling type applies)", v, err)
		}
	}
	for _, v := range []string{
		"https://json-schema.org/draft/2019-09/schema",
		"http://json-schema.org/draft-06/schema#",
		"https://json-schema.org/draft-07/schema#/definitions",
		"https://example.com/json-schema.org/draft-07/schema",
		"ftp://json-schema.org/draft-07/schema#",
		"json-schema.org/draft-07/schema",
	} {
		src := fmt.Sprintf(`{"$schema":%q,"type":"object"}`, v)
		if err := c.Check(v, schemaOf(t, src), json.RawMessage(`{}`)); !errors.Is(err, ErrUnreadable) {
			t.Errorf("%s: %v, want unreadable", v, err)
		}
	}
}

func TestRemoteRefIsUnreadableAndNeverFetched(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"type":"string"}`))
	}))
	defer srv.Close()
	var c Checker
	for name, src := range map[string]string{
		"remote $ref":    fmt.Sprintf(`{"type":"object","properties":{"x":{"$ref":%q}}}`, srv.URL+"/s.json"),
		"remote $id ref": fmt.Sprintf(`{"$id":%q,"type":"object","properties":{"x":{"$ref":"other.json"}}}`, srv.URL+"/root.json"),
		"draft-04":       `{"$schema":"http://json-schema.org/draft-04/schema#","type":"object","properties":{"n":{"type":"integer","minimum":0,"exclusiveMinimum":true}}}`,
		"not a schema":   `{"type":7}`,
	} {
		err := c.Check(name, schemaOf(t, src), json.RawMessage(`{"x":"a"}`))
		if !errors.Is(err, ErrUnreadable) {
			t.Errorf("%s: %v, want unreadable", name, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the schema server was asked %d times", n)
	}
}

func TestNoSchemaTakesAnyObject(t *testing.T) {
	var c Checker
	for _, schema := range []any{nil, map[string]any{}, map[string]any{"type": "object"}, true} {
		if err := c.Check(fmt.Sprint(schema), schema, json.RawMessage(`{"anything":[1,{"a":null}]}`)); err != nil {
			t.Errorf("%v: %v", schema, err)
		}
	}
	var m *MismatchError
	if err := c.Check("false", false, json.RawMessage(`{}`)); !errors.As(err, &m) {
		t.Errorf("the false schema takes nothing: %v", err)
	}
}

func TestCompiledSchemasAreCachedAndBounded(t *testing.T) {
	var c Checker
	// The same hash is never compiled again: a different schema under it is
	// not looked at.
	if err := c.Check("same", schemaOf(t, strict), json.RawMessage(`{"text":"a"}`)); err != nil {
		t.Fatal(err)
	}
	var m *MismatchError
	if err := c.Check("same", nil, json.RawMessage(`{}`)); !errors.As(err, &m) {
		t.Fatalf("the cached schema was not used: %v", err)
	}
	// An unreadable schema is remembered too.
	_ = c.Check("bad", schemaOf(t, `{"type":7}`), json.RawMessage(`{}`))
	if err := c.Check("bad", nil, json.RawMessage(`{}`)); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("the unreadable schema was not remembered: %v", err)
	}
	for i := range MaxCached + 10 {
		_ = c.Check(fmt.Sprint("h", i), nil, json.RawMessage(`{}`))
	}
	if n := c.cached(); n > MaxCached {
		t.Fatalf("%d compiled schemas kept, the limit is %d", n, MaxCached)
	}
}

func TestReasonIsBounded(t *testing.T) {
	var c Checker
	long := strings.Repeat("é", 4000)
	schema := schemaOf(t, `{"type":"object","properties":{"s":{"type":"integer"}}}`)
	err := c.Check("h", schema, json.RawMessage(`{"s":"`+long+`"}`))
	var m *MismatchError
	if !errors.As(err, &m) {
		t.Fatal(err)
	}
	if len(m.Reason) > MaxReason || !utf8.ValidString(m.Reason) {
		t.Fatalf("reason of %d bytes, valid UTF-8 %v", len(m.Reason), utf8.ValidString(m.Reason))
	}
}

func TestDefinitionProblem(t *testing.T) {
	for def, bad := range map[string]bool{
		`{"name":"t","inputSchema":` + strict + `}`: false,
		`{"name":"t"}`:                false,
		`{"name":"p","arguments":[]}`: false,
		`{"name":"t","inputSchema":{"type":"object","properties":{"x":{"$ref":"https://e.x/s"}}}}`: true,
		`{"name":"t","inputSchema":{"type":7}}`:                                                    true,
		`not json`:                                                                                 false,
	} {
		got := DefinitionProblem([]byte(def))
		if (got != "") != bad {
			t.Errorf("%s: %q", def, got)
		}
		if len(got) > MaxReason {
			t.Errorf("%s: problem of %d bytes", def, len(got))
		}
	}
}
