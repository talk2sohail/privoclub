package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Conditions on a call's arguments (ADR 0024). They read the canonical
// arguments, the bytes that are also sent upstream, so the shape of a value
// cannot make a rule miss while the call still runs.

// Condition operators.
const (
	OpEquals    = "equals"
	OpNotEquals = "not_equals"
	OpIn        = "in"
	OpMatches   = "matches" // the rules' own glob, on a string
	OpExists    = "exists"
	OpMissing   = "missing"
	// Strings compared ignoring case: for names that a server reads without
	// case (repositories, hosts, accounts).
	OpEqualsAnyCase  = "equals_any_case"
	OpInAnyCase      = "in_any_case"
	OpMatchesAnyCase = "matches_any_case"
)

// verdict is what a condition says about a call. Unsure means the argument
// has another type than the rule's value: a server may read "1" as 1 or
// "yes" as true, so the gateway cannot tell. A deny or ask counts unsure as
// holding, an allow as not holding (security review 2026-11-20).
type verdict int

const (
	no verdict = iota
	yes
	unsure
)

// Limits on conditions.
const (
	MaxConditions = 8
	maxPathKeys   = 8
	maxPathLen    = 128
	maxValueJSON  = 1024
	maxInValues   = 50
)

// Condition is one test on an argument.
type Condition struct {
	Arg   string `json:"arg" yaml:"arg"`                         // dotted path of object keys: "repo", "options.force"
	Op    string `json:"op" yaml:"op"`                           // one of the Op constants
	Value any    `json:"value,omitempty" yaml:"value,omitempty"` // a JSON value; a list for "in"; a glob for "matches"
}

var errCondition = errors.New("policy: invalid condition")

// ValidConditions checks a rule's conditions and returns the first problem
// in words a person can act on.
func ValidConditions(cs []Condition) error {
	if len(cs) > MaxConditions {
		return fmt.Errorf("%w: at most %d conditions per rule", errCondition, MaxConditions)
	}
	for _, c := range cs {
		if err := c.valid(); err != nil {
			return err
		}
	}
	return nil
}

func (c Condition) valid() error {
	keys := strings.Split(c.Arg, ".")
	if c.Arg == "" || len(c.Arg) > maxPathLen || len(keys) > maxPathKeys || strings.ContainsFunc(c.Arg, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fmt.Errorf("%w: arg must be a dotted path of up to %d keys and %d characters", errCondition, maxPathKeys, maxPathLen)
	}
	for _, k := range keys {
		if k == "" {
			return fmt.Errorf("%w: arg %q has an empty key", errCondition, c.Arg)
		}
	}
	if b, err := json.Marshal(c.Value); err != nil || len(b) > maxValueJSON {
		return fmt.Errorf("%w: the value of %q must be JSON of at most %d bytes", errCondition, c.Arg, maxValueJSON)
	}
	switch c.Op {
	case OpEquals, OpNotEquals:
	case OpEqualsAnyCase:
		if _, ok := c.Value.(string); !ok {
			return fmt.Errorf("%w: %q needs a text value", errCondition, c.Op)
		}
	case OpIn, OpInAnyCase:
		list, ok := c.Value.([]any)
		if !ok || len(list) == 0 || len(list) > maxInValues {
			return fmt.Errorf("%w: %q needs a list of 1 to %d values", errCondition, c.Op, maxInValues)
		}
		if c.Op == OpInAnyCase {
			for _, x := range list {
				if _, ok := x.(string); !ok {
					return fmt.Errorf("%w: %q needs text values", errCondition, c.Op)
				}
			}
		}
	case OpMatches, OpMatchesAnyCase:
		s, ok := c.Value.(string)
		if !ok || !ValidGlob(s) {
			return fmt.Errorf("%w: %q needs a pattern (* and ? and [a-z])", errCondition, c.Op)
		}
	case OpExists, OpMissing:
		if c.Value != nil {
			return fmt.Errorf("%w: %q takes no value", errCondition, c.Op)
		}
	default:
		return fmt.Errorf("%w: op must be equals, not_equals, in, matches, exists, missing, equals_any_case, in_any_case or matches_any_case", errCondition)
	}
	return nil
}

// Normalize returns the condition with its value decoded the way arguments
// are (numbers as json.Number), so comparing is exact. It is applied when a
// rule is read or created.
func (c Condition) Normalize() Condition {
	if c.Value == nil {
		return c
	}
	b, err := json.Marshal(c.Value)
	if err != nil {
		return c
	}
	if v, err := decodeArgs(b); err == nil {
		c.Value = v
	}
	return c
}

func decodeArgs(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// UnmarshalJSON reads a condition keeping numbers exact (no float64), so a
// rule tests the number its author wrote (security review 2026-11-20).
func (c *Condition) UnmarshalJSON(b []byte) error {
	var raw struct {
		Arg   string          `json:"arg"`
		Op    string          `json:"op"`
		Value json.RawMessage `json:"value"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	c.Arg, c.Op, c.Value = raw.Arg, raw.Op, nil
	if len(bytes.TrimSpace(raw.Value)) > 0 {
		v, err := decodeArgs(raw.Value)
		if err != nil {
			return err
		}
		c.Value = v
	}
	return nil
}

// eval tests the condition against decoded arguments (nil when there are
// none). A missing path is a plain no for equals, in and matches. A value of
// another type than the rule's is unsure, never a no (ADR 0024, security
// review 2026-11-20).
func (c Condition) eval(args any) verdict {
	v, found := lookup(args, c.Arg)
	switch c.Op {
	case OpExists:
		return bool2v(found)
	case OpMissing:
		return bool2v(!found)
	case OpNotEquals:
		if !found {
			return yes
		}
		switch equal(v, c.Value) {
		case yes:
			return no
		case no:
			return yes
		}
		return unsure
	}
	if !found {
		return no
	}
	switch c.Op {
	case OpEquals:
		return equal(v, c.Value)
	case OpIn:
		list, _ := c.Value.([]any)
		out := no
		for _, x := range list {
			switch equal(v, x) {
			case yes:
				return yes
			case unsure:
				out = unsure
			}
		}
		if out == unsure {
			// Unsure only when no value of the list shares the argument's type.
			for _, x := range list {
				if kind(x) == kind(v) {
					return no
				}
			}
		}
		return out
	case OpMatches, OpMatchesAnyCase:
		str, ok := v.(string)
		p, _ := c.Value.(string)
		if !ok {
			return unsure
		}
		if c.Op == OpMatchesAnyCase {
			str, p = strings.ToLower(str), strings.ToLower(p)
		}
		m, err := globMatch(p, str)
		return bool2v(err == nil && m)
	case OpEqualsAnyCase:
		str, ok := v.(string)
		want, _ := c.Value.(string)
		if !ok {
			return unsure
		}
		return bool2v(strings.EqualFold(str, want))
	case OpInAnyCase:
		str, ok := v.(string)
		if !ok {
			return unsure
		}
		list, _ := c.Value.([]any)
		for _, x := range list {
			if want, _ := x.(string); strings.EqualFold(str, want) {
				return yes
			}
		}
		return no
	}
	return no
}

func bool2v(b bool) verdict {
	if b {
		return yes
	}
	return no
}

// kind names a decoded JSON value's type.
func kind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case string:
		return "string"
	case json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "other"
}

// equal compares an argument with a rule's value: unsure when their types
// differ or a number cannot be read, otherwise yes or no.
func equal(v, want any) verdict {
	if kind(v) != kind(want) {
		return unsure
	}
	return equalJSON(v, want)
}

// equalNumbers compares two JSON numbers by exact value, at any length,
// without expanding them: each becomes a sign, its significant digits and an
// exponent. A number it cannot read is unsure (security review 2026-11-20:
// 42.000…0 is 42).
func equalNumbers(a, b string) verdict {
	na, oka := decimal(a)
	nb, okb := decimal(b)
	if !oka || !okb {
		return unsure
	}
	return bool2v(na == nb)
}

type dec struct {
	neg    bool
	digits string // significant digits, no leading or trailing zeros; "" for zero
	exp    int    // value = digits × 10^exp
}

func decimal(s string) (dec, bool) {
	var d dec
	if strings.HasPrefix(s, "-") {
		d.neg, s = true, s[1:]
	}
	mant, expPart, hasExp := strings.Cut(strings.ToLower(s), "e")
	intPart, frac, _ := strings.Cut(mant, ".")
	if intPart == "" || strings.Trim(intPart+frac, "0123456789") != "" {
		return d, false
	}
	exp := 0
	if hasExp {
		e := strings.TrimPrefix(expPart, "+")
		if len(strings.TrimPrefix(e, "-")) == 0 || len(strings.TrimPrefix(e, "-")) > 9 {
			return d, false
		}
		var err error
		if exp, err = strconv.Atoi(e); err != nil {
			return d, false
		}
	}
	digits := strings.TrimLeft(intPart+frac, "0")
	exp -= len(frac)
	trimmed := strings.TrimRight(digits, "0")
	exp += len(digits) - len(trimmed)
	d.digits = trimmed
	if d.digits == "" {
		return dec{}, true // zero, whatever its sign
	}
	d.exp = exp
	return d, true
}

func lookup(args any, path string) (any, bool) {
	cur := args
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// equalJSON compares decoded JSON values of the same kind; numbers by exact
// value at any length (1 equals 1.0 equals 1e0).
func equalJSON(a, b any) verdict {
	switch x := a.(type) {
	case nil:
		return bool2v(b == nil)
	case bool:
		y, ok := b.(bool)
		return bool2v(ok && x == y)
	case string:
		y, ok := b.(string)
		return bool2v(ok && x == y)
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return unsure
		}
		return equalNumbers(x.String(), y.String())
	case []any:
		y, ok := b.([]any)
		if !ok {
			return unsure
		}
		if len(x) != len(y) {
			return no
		}
		out := yes
		for i := range x {
			switch equal(x[i], y[i]) {
			case no:
				return no
			case unsure:
				out = unsure
			}
		}
		return out
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return unsure
		}
		if len(x) != len(y) {
			return no
		}
		out := yes
		for k, xv := range x {
			yv, ok := y[k]
			if !ok {
				return no
			}
			switch equal(xv, yv) {
			case no:
				return no
			case unsure:
				out = unsure
			}
		}
		return out
	}
	return unsure
}
