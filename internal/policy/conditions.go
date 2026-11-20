package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
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
	case OpIn:
		list, ok := c.Value.([]any)
		if !ok || len(list) == 0 || len(list) > maxInValues {
			return fmt.Errorf("%w: \"in\" needs a list of 1 to %d values", errCondition, maxInValues)
		}
	case OpMatches:
		s, ok := c.Value.(string)
		if !ok || !ValidGlob(s) {
			return fmt.Errorf("%w: \"matches\" needs a pattern (* and ? and [a-z])", errCondition)
		}
	case OpExists, OpMissing:
		if c.Value != nil {
			return fmt.Errorf("%w: %q takes no value", errCondition, c.Op)
		}
	default:
		return fmt.Errorf("%w: op must be equals, not_equals, in, matches, exists or missing", errCondition)
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

// holds tests the condition against decoded arguments (nil when there are
// none). Values of another type, and missing paths, never satisfy equals,
// in or matches (ADR 0024 point 3).
func (c Condition) holds(args any) bool {
	v, found := lookup(args, c.Arg)
	switch c.Op {
	case OpEquals:
		return found && equalJSON(v, c.Value)
	case OpNotEquals:
		return !found || !equalJSON(v, c.Value)
	case OpIn:
		if !found {
			return false
		}
		list, _ := c.Value.([]any)
		for _, x := range list {
			if equalJSON(v, x) {
				return true
			}
		}
		return false
	case OpMatches:
		s, ok := v.(string)
		p, _ := c.Value.(string)
		if !found || !ok {
			return false
		}
		m, err := globMatch(p, s)
		return err == nil && m
	case OpExists:
		return found
	case OpMissing:
		return !found
	}
	return false
}

// equalNumbers compares two JSON numbers by value. Numbers that are long or
// have a large exponent (which big.Rat would expand at great cost, and which
// an AI client controls) compare as written.
func equalNumbers(a, b string) bool {
	if !smallNumber(a) || !smallNumber(b) {
		return a == b
	}
	ra, oka := new(big.Rat).SetString(a)
	rb, okb := new(big.Rat).SetString(b)
	return oka && okb && ra.Cmp(rb) == 0
}

func smallNumber(s string) bool {
	if len(s) > 64 {
		return false
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err := strconv.Atoi(strings.TrimPrefix(s[i+1:], "+"))
		return err == nil && exp >= -400 && exp <= 400
	}
	return true
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

// equalJSON compares decoded JSON values; numbers by value (1 equals 1.0),
// never across types ("1" is not 1).
func equalJSON(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		return equalNumbers(x.String(), y.String())
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equalJSON(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !equalJSON(xv, yv) {
				return false
			}
		}
		return true
	}
	return false
}
