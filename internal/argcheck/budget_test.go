package argcheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Security review 2026-11-22: schemas the library would validate forever, or
// until the stack overflows, or at great cost, stop at the budget. Each case
// must end quickly; a regression ends the test binary or times out.

// within fails a case that takes longer than ten seconds. The limit is loose
// (the race detector is slow); what it catches is a check that does not end.
func within(t *testing.T, name string, f func()) {
	const limit = 10 * time.Second
	t.Helper()
	start := time.Now()
	f()
	if took := time.Since(start); took > limit {
		t.Errorf("%s took %v", name, took)
	}
}

func TestRefLoopsAreUnreadableOrTooCostly(t *testing.T) {
	for name, src := range map[string]string{
		"self":              `{"$ref":"#"}`,
		"two through allOf": `{"$defs":{"a":{"$ref":"#/$defs/b"},"b":{"$ref":"#/$defs/a"}},"allOf":[{"$ref":"#/$defs/a"}]}`,
		"upper case":        `{"$defs":{"a":{"$REF":"#/$defs/b"},"b":{"$REF":"#/$defs/a"}},"ALLOF":[{"$REF":"#/$defs/a"}]}`,
		"through anyOf":     `{"anyOf":[{"type":"string"},{"$ref":"#"}]}`,
		"through not":       `{"type":"object","not":{"$ref":"#"}}`,
		"through if":        `{"$defs":{"x":{"if":{"$ref":"#/$defs/x"}}},"$ref":"#/$defs/x"}`,
		"dynamic":           `{"$dynamicAnchor":"n","allOf":[{"$dynamicRef":"#n"}]}`,
		"long s (U+017F)":   `{"$defs":{"a":{"allOf":[{"$ref":"#/$defs/a"}]}},"containſ":{"type":"string"},"allOf":[{"$ref":"#/$defs/a"}]}`,
	} {
		within(t, name, func() {
			if _, err := Compile(schemaOf(t, src)); !errors.Is(err, ErrUnreadable) {
				t.Errorf("%s: %v", name, err)
			}
		})
	}
	// A loop that only arguments reach is found at the call.
	var c Checker
	within(t, "dependentSchemas", func() {
		s := schemaOf(t, `{"dependentSchemas":{"a":{"$ref":"#"}}}`)
		if err := c.Check("dep", s, json.RawMessage(`{"b":1}`)); err != nil {
			t.Errorf("without a: %v", err)
		}
		if err := c.Check("dep", s, json.RawMessage(`{"a":1}`)); !errors.Is(err, ErrTooCostly) {
			t.Errorf("with a: %v", err)
		}
	})
}

func TestSharedReferencesThatMultiplyStop(t *testing.T) {
	for _, spelling := range [][2]string{{"$ref", "anyOf"}, {"$Ref", "AnyOf"}} {
		defs := map[string]any{"a30": map[string]any{"type": "string"}}
		for i := 29; i >= 0; i-- {
			next := map[string]any{spelling[0]: fmt.Sprintf("#/$defs/a%d", i+1)}
			defs[fmt.Sprintf("a%d", i)] = map[string]any{spelling[1]: []any{next, next}}
		}
		within(t, spelling[0], func() {
			if _, err := Compile(map[string]any{"$defs": defs, spelling[0]: "#/$defs/a0"}); !errors.Is(err, ErrUnreadable) {
				t.Errorf("%v: %v", spelling, err)
			}
		})
	}
	var c Checker
	s := schemaOf(t, `{"type":"object","patternProperties":{"":{"$ref":"#"},".":{"$ref":"#"}}}`)
	within(t, "patterns", func() {
		deep := strings.Repeat(`{"a":`, 25) + `{}` + strings.Repeat(`}`, 25)
		if err := c.Check("p", s, json.RawMessage(deep)); !errors.Is(err, ErrTooCostly) {
			t.Errorf("deep arguments: %v", err)
		}
		if err := c.Check("p", s, json.RawMessage(`{"a":{"b":{}}}`)); err != nil {
			t.Errorf("shallow arguments: %v", err)
		}
	})
}

// One step can be costly: each is paid for.
func TestCostlyStepsStop(t *testing.T) {
	enum := make([]string, 4000)
	for i := range enum {
		enum[i] = fmt.Sprintf("%q", fmt.Sprint("value-", i))
	}
	alts := make([]string, 4000)
	for i := range alts {
		alts[i] = fmt.Sprintf("[a-z]*q%d", i)
	}
	many := `[` + strings.Repeat(`"zz",`, 100_000) + `"zz"]`
	last := `[` + strings.Repeat(`"value-3999",`, 100_000) + `"value-3999"]` // each item matches the last entry
	long := `"` + strings.Repeat("a", 900_000) + `"`
	for name, tc := range map[string]struct{ schema, args string }{
		"enum under items":          {`{"type":"object","properties":{"xs":{"items":{"enum":[` + strings.Join(enum, ",") + `]}}}}`, `{"xs":` + last + `}`},
		"contains with an enum":     {`{"type":"object","properties":{"xs":{"contains":{"enum":[` + strings.Join(enum, ",") + `]}}}}`, `{"xs":` + many + `}`},
		"long pattern, long string": {`{"type":"object","properties":{"s":{"pattern":"^(?:` + strings.Join(alts, "|") + `)$"}}}`, `{"s":` + long + `}`},
		"uniqueItems in a union":    {`{"type":"object","properties":{"xs":{"anyOf":[` + strings.Repeat(`{"uniqueItems":true,"maxItems":1},`, 200) + `{"uniqueItems":true,"maxItems":1}]}}}`, `{"xs":` + many + `}`},
	} {
		var c Checker
		within(t, name, func() {
			if err := c.Check(name, schemaOf(t, tc.schema), json.RawMessage(tc.args)); !errors.Is(err, ErrTooCostly) {
				t.Errorf("%s: %v", name, err)
			}
		})
	}
}

func TestDeeplyNestedSchemasAreRefusedQuickly(t *testing.T) {
	for _, kw := range []string{"not", "items"} {
		src := strings.Repeat(`{"`+kw+`":`, 5000) + `{}` + strings.Repeat(`}`, 5000)
		var schema any
		dec := json.NewDecoder(strings.NewReader(src))
		if err := dec.Decode(&schema); err != nil {
			t.Fatal(err)
		}
		within(t, kw, func() {
			if _, err := Compile(schema); !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "nested") {
				t.Errorf("%s: %v", kw, err)
			}
		})
	}
}

func TestRecursiveSchemasStayCheap(t *testing.T) {
	// A filter tree: several references back at each level, but one argument
	// takes one of them, so the cost grows with the arguments only.
	var c Checker
	s := schemaOf(t, `{"$defs":{"f":{"type":"object","properties":{"and":{"type":"array","items":{"$ref":"#/$defs/f"}},"or":{"type":"array","items":{"$ref":"#/$defs/f"}},"not":{"$ref":"#/$defs/f"},"eq":{"type":"string"}},"additionalProperties":false}},"type":"object","properties":{"filter":{"$ref":"#/$defs/f"}}}`)
	deep := `{"eq":"x"}`
	for range 30 {
		deep = `{"and":[` + deep + `],"not":{"eq":"y"}}`
	}
	if err := c.Check("tree", s, json.RawMessage(`{"filter":`+deep+`}`)); err != nil {
		t.Fatalf("a deep filter: %v", err)
	}
	var m *MismatchError
	if err := c.Check("tree", s, json.RawMessage(`{"filter":{"and":[{"eq":1}]}}`)); !errors.As(err, &m) {
		t.Fatalf("a wrong filter: %v", err)
	}
	// A wide union and a long enum are fine for ordinary arguments.
	variants := make([]string, 200)
	for i := range variants {
		variants[i] = fmt.Sprintf(`{"type":"object","properties":{"kind":{"const":"k%d"}},"required":["kind"]}`, i)
	}
	union := schemaOf(t, `{"type":"object","properties":{"op":{"oneOf":[`+strings.Join(variants, ",")+`]},"tz":{"enum":[`+strings.Repeat(`"Europe/X",`, 600)+`"UTC"]}}}`)
	if err := c.Check("union", union, json.RawMessage(`{"op":{"kind":"k7"},"tz":"UTC"}`)); err != nil {
		t.Fatalf("a wide union: %v", err)
	}
}

func TestTooCostlyArgumentsAreRefused(t *testing.T) {
	old := MaxSteps
	MaxSteps = 1000
	defer func() { MaxSteps = old }()
	var c Checker
	s := schemaOf(t, `{"type":"object","properties":{"xs":{"type":"array","items":{"anyOf":[{"type":"string"},{"type":"number"},{"type":"boolean"}]}}}}`)
	if err := c.Check("x", s, json.RawMessage(`{"xs":["a",1,true]}`)); err != nil {
		t.Fatal(err)
	}
	big := `{"xs":[` + strings.Repeat(`"a",`, 999) + `"a"]}`
	if err := c.Check("x", s, json.RawMessage(big)); !errors.Is(err, ErrTooCostly) {
		t.Fatalf("got %v", err)
	}
}

func TestUnsupportedVersionsAreUnreadable(t *testing.T) {
	for _, v := range []string{"http://json-schema.org/draft-04/schema#", "https://json-schema.org/draft/2019-09/schema"} {
		if _, err := Compile(map[string]any{"$schema": v, "type": "object"}); !errors.Is(err, ErrUnreadable) {
			t.Errorf("%s: %v", v, err)
		}
	}
}

func TestMessagesStayShort(t *testing.T) {
	var c Checker
	long := strings.Repeat("x", 100_000)
	s := schemaOf(t, `{"type":"object","properties":{"s":{"anyOf":[{"type":"integer"},{"enum":[`+strings.Repeat(`"`+strings.Repeat("e", 200)+`",`, 50)+`"z"]},{"pattern":"^y"}]}}}`)
	err := c.Check("m", s, json.RawMessage(`{"s":"`+long+`"}`))
	var m *MismatchError
	if !errors.As(err, &m) {
		t.Fatal(err)
	}
	if len(m.Reason) > 300 || strings.Contains(m.Reason, strings.Repeat("x", 100)) || strings.Contains(m.Reason, "eeeeeeeeee") {
		t.Fatalf("reason %q", m.Reason)
	}
}

// Third round of the review: messages and kept paths are bounded whatever the
// schema's names; the review keeps answers, not compiled schemas.
func TestLongNamesStayCheap(t *testing.T) {
	key := strings.Repeat("k", 30_000)
	leaves := strings.Repeat(`{},`, 5000) + `{}`
	if _, err := Compile(schemaOf(t, `{"properties":{"`+key+`":{"allOf":[`+leaves+`]}}}`)); err != nil {
		t.Fatal(err)
	}
	var c Checker
	s := schemaOf(t, `{"type":"object","anyOf":[`+strings.Repeat(`{"additionalProperties":false,"required":["`+strings.Repeat("r", 2000)+`"]},`, 20)+`{"additionalProperties":false}]}`)
	err := c.Check("names", s, json.RawMessage(`{"`+strings.Repeat("x", 500_000)+`":1}`))
	var m *MismatchError
	if !errors.As(err, &m) && !errors.Is(err, ErrTooCostly) {
		t.Fatal(err)
	}
	if m != nil && len(m.Reason) > MaxReason {
		t.Fatalf("reason of %d bytes", len(m.Reason))
	}
}

func TestCacheIsBoundedBySubschemas(t *testing.T) {
	old := maxCachedNodes
	maxCachedNodes = 2500
	defer func() { maxCachedNodes = old }()
	var c Checker
	wide := schemaOf(t, `{"allOf":[`+strings.Repeat(`{},`, 999)+`{}]}`)
	for i := range 10 {
		_ = c.Check(fmt.Sprint("w", i), wide, json.RawMessage(`{}`))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nodes > maxCachedNodes {
		t.Fatalf("%d subschemas kept", c.nodes)
	}
}
