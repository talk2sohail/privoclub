// Privo: limits on validation. Not part of the upstream library (see
// PRIVO.md). Schemas come from upstream MCP servers, which are untrusted, and
// the upstream library validates by plain recursion with no limit: a $ref
// loop overflows the stack (a fatal error no recover() catches), references
// that share their targets cost 2^n steps, and one step can be costly (a
// large enum, a long pattern on a long string, hashing for uniqueItems). Every
// validation therefore spends from a budget and stops when it is gone.

package jsonschema

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"
)

// ErrBudget is returned when validation would cost more than its budget.
var ErrBudget = errors.New("jsonschema: validation stopped: it would cost more than its budget")

// DefaultBudget is the budget of Validate, in units of about one simple
// validation step each.
const DefaultBudget = 2_000_000

// MaxFrames bounds how deeply validations nest (the stack).
const MaxFrames = 1000

// maxPath bounds the schema locations kept for messages. The upstream library
// kept every subschema's whole JSON pointer, so long keys over many
// subschemas held hundreds of megabytes (security review 2026-11-22).
const maxPath = 120

// grow appends a segment to a schema location until it is maxPath long;
// past that the location stays as it is (it is only shown, clipped).
func grow(path []byte, format string, args ...any) []byte {
	if len(path) >= maxPath {
		return path
	}
	return fmt.Appendf(path, format, args...)
}

// names shows a list of property names briefly: how many, and the first.
func names(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	if len(list) == 1 {
		return fmt.Sprintf("%q", clipText(list[0], 60))
	}
	return fmt.Sprintf("%d, the first %q", len(list), clipText(list[0], 60))
}

// maxMessage is the length past which an error is no longer wrapped with the
// schema's location, so a message cannot grow with the nesting.
const maxMessage = 4096

// spend takes n units from the budget; false when they are not there.
func (st *state) spend(n int64) bool {
	if n < 1 {
		n = 1
	}
	if st.budget < n {
		st.budget = 0
		st.exceeded = true
		return false
	}
	st.budget -= n
	return true
}

// spendMatch pays for matching a regular expression against s: the work of
// Go's linear-time matcher grows with both lengths.
func (st *state) spendMatch(pattern, s string) bool {
	return st.spend(1 + int64(len(pattern))*int64(len(s)+1)/64)
}

// spendInstance pays for the work that walks the instance itself: the keys of
// an object (additionalProperties, propertyNames, unevaluatedProperties), the
// items of an array.
func (st *state) spendInstance(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
		return st.spend(int64(v.Len()))
	}
	return true
}

// spendTree pays one unit for every value in v, for work that reads all of
// it (hashing for uniqueItems). It stops as soon as the budget is gone.
func (st *state) spendTree(v reflect.Value) bool {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return st.spend(1)
		}
		v = v.Elem()
	}
	if !st.spend(1) {
		return false
	}
	switch v.Kind() {
	case reflect.Map:
		for _, e := range v.Seq2() {
			if !st.spendTree(e) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if !st.spendTree(v.Index(i)) {
				return false
			}
		}
	case reflect.String:
		return st.spend(int64(v.Len()) / 64)
	}
	return true
}

// wrap is wrapf for validate: it names the schema's location, except on a
// stop for the budget and on a message that is already long.
func (st *state) wrap(errp *error, s *Schema) {
	if *errp == nil || errors.Is(*errp, ErrBudget) || len((*errp).Error()) > maxMessage {
		return
	}
	*errp = fmt.Errorf("validating %s: %w", clipText(st.rs.schemaString(s), 120), *errp)
}

// brief shows a value in at most about 80 bytes. The upstream library printed
// whole values, which repeated up to a megabyte of arguments in every error.
func brief(v reflect.Value) string {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "null"
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Map:
		return fmt.Sprintf("an object with %d properties", v.Len())
	case reflect.Slice, reflect.Array:
		return fmt.Sprintf("an array of %d items", v.Len())
	case reflect.String:
		return fmt.Sprintf("%q", clipText(v.String(), 60))
	}
	return clipText(fmt.Sprint(v.Interface()), 60)
}

// clipText cuts s to at most n bytes without splitting a character.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// fixedCost is what one validation against s costs apart from its
// subschemas, patterns and the instance: one unit, one per entry of every
// list it walks, and the size of the values it compares the instance with.
func fixedCost(s *Schema) int64 {
	c := 1 + len(s.Types) + len(s.Enum) + len(s.Required) + len(s.Properties) + len(s.PatternProperties) +
		len(s.AllOf) + len(s.AnyOf) + len(s.OneOf) + len(s.PrefixItems) + len(s.ItemsArray) +
		len(s.DependentSchemas) + len(s.DependencySchemas)
	for _, l := range s.DependentRequired {
		c += 1 + len(l)
	}
	for _, l := range s.DependencyStrings {
		c += 1 + len(l)
	}
	cost := int64(c)
	// Names are hashed for every lookup and compared: pay for their bytes.
	nameBytes := 0
	for _, n := range s.Required {
		nameBytes += len(n)
	}
	for n := range s.Properties {
		nameBytes += len(n)
	}
	for n, l := range s.DependentRequired {
		nameBytes += len(n)
		for _, m := range l {
			nameBytes += len(m)
		}
	}
	for n, l := range s.DependencyStrings {
		nameBytes += len(n)
		for _, m := range l {
			nameBytes += len(m)
		}
	}
	for n := range s.DependentSchemas {
		nameBytes += len(n)
	}
	for n := range s.DependencySchemas {
		nameBytes += len(n)
	}
	cost += int64(nameBytes) / 64
	size := func(v any) int64 {
		b, err := json.Marshal(v)
		if err != nil {
			return 1 << 20
		}
		return int64(len(b)) / 64
	}
	for _, e := range s.Enum {
		cost += size(e)
	}
	if s.Const != nil {
		cost += size(*s.Const)
	}
	// Messages name the schema's location: long names cost their length.
	cost += int64(len(strings.TrimSpace(s.ID))) / 64
	return cost
}

// Nodes is how many schemas r holds, for callers that bound the memory of
// the resolved schemas they keep.
func (r *Resolved) Nodes() int { return len(r.resolvedInfos) }
