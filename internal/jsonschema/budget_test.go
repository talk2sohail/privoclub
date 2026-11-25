package jsonschema

import (
	"encoding/json"
	"errors"
	"testing"
)

func resolved(t *testing.T, src string) *Resolved {
	t.Helper()
	var s Schema
	if err := json.Unmarshal([]byte(src), &s); err != nil {
		t.Fatal(err)
	}
	r, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Privo: a validation stopped by the budget is never read as a result: "not"
// and "oneOf" would read the stopped branch as a failure and pass.
func TestAStopIsNeverValid(t *testing.T) {
	for _, src := range []string{
		`{"not":{"$ref":"#"}}`,
		`{"oneOf":[{"$ref":"#"},{"type":"object"}]}`,
		`{"anyOf":[{"$ref":"#"},{"type":"object"}]}`,
		`{"if":{"$ref":"#"},"then":{"type":"string"}}`,
	} {
		if err := resolved(t, src).ValidateWithin(map[string]any{}, 10_000); !errors.Is(err, ErrBudget) {
			t.Errorf("%s: %v", src, err)
		}
	}
}

func TestBudgetAndFrames(t *testing.T) {
	r := resolved(t, `{"type":"object","properties":{"a":{"type":"string"}}}`)
	if err := r.ValidateWithin(map[string]any{"a": "x"}, 100); err != nil {
		t.Fatalf("within the budget: %v", err)
	}
	if err := r.ValidateWithin(map[string]any{"a": "x"}, 1); !errors.Is(err, ErrBudget) {
		t.Fatalf("past the budget: %v", err)
	}
	// Nesting stops at MaxFrames even with budget left.
	deep := resolved(t, `{"allOf":[{"$ref":"#"}]}`)
	if err := deep.ValidateWithin(map[string]any{}, 1<<40); !errors.Is(err, ErrBudget) {
		t.Fatalf("a loop with a huge budget: %v", err)
	}
}
