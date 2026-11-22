// Package argcheck checks a tool call's arguments against the input schema
// of the tool's accepted definition (F02). A schema is compiled once per
// definition and kept by the definition's hash.
//
// Schemas come from upstream servers, which are untrusted:
//   - no loader is given to the schema library, so a $ref to anything outside
//     the schema is an unreadable schema and is never fetched;
//   - the library is Privo's copy (internal/jsonschema, PRIVO.md), which
//     validates within a budget: a schema whose validation would loop or blow
//     up stops at the budget, and the call is refused;
//   - the schema's text is bounded in nesting before the library decodes it,
//     since its decoding costs the size times the nesting.
package argcheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"unicode/utf8"

	"privo.club/privo/internal/jsonschema"
)

// ErrUnreadable marks a schema Privo cannot use. Calls to its tool are
// refused, because their arguments cannot be checked.
var ErrUnreadable = errors.New("the tool's input schema cannot be read")

// ErrTooCostly marks arguments that cannot be checked against their schema
// within MaxSteps. The call is refused.
var ErrTooCostly = errors.New("the arguments are too large to check against this tool's input schema")

// MismatchError is arguments that do not match the schema. Reason can repeat
// argument values, so it goes back to the caller who sent them and nowhere
// else: never to the audit or the log.
type MismatchError struct{ Reason string }

func (e *MismatchError) Error() string {
	return "the arguments do not match the input schema: " + e.Reason
}

// Limits. Vars so tests can lower them.
var (
	// MaxSteps is the budget of one call's check, in units of about one
	// simple validation step (internal/jsonschema/budget.go).
	MaxSteps int64 = 1_000_000
	// trialSteps is the budget of checking empty arguments when the schema
	// is compiled: a schema that cannot even do that loops or blows up, and
	// is unreadable, so the review shows it.
	trialSteps int64 = 200_000
	// maxCachedNodes bounds the subschemas a Checker's compiled schemas hold
	// together: one 64 KiB schema can hold thousands.
	maxCachedNodes = 200_000
)

const (
	// MaxReason bounds the reason shown to the caller.
	MaxReason = 512
	// MaxCached bounds the compiled schemas a Checker keeps.
	MaxCached = 4096
	// maxSchemaBytes and maxSchemaNesting bound a schema's text before it is
	// decoded. A tool's whole definition is at most 64 KiB (upstream).
	maxSchemaBytes   = 64 << 10
	maxSchemaNesting = 64
)

type compiled struct {
	schema *jsonschema.Resolved // nil: no schema, any object
	err    error                // ErrUnreadable with the cause
}

// Checker checks arguments, caching compiled schemas. The zero value is ready
// to use and safe for concurrent use.
type Checker struct {
	mu    sync.Mutex
	cache map[string]compiled
	nodes int // subschemas held in cache
}

// Check checks args, a JSON object, against schema, the tool's inputSchema as
// the upstream sent it. hash identifies the definition schema came from; the
// same hash must always come with the same schema. It returns nil, a
// *MismatchError, ErrTooCostly, or an error wrapping ErrUnreadable.
func (c *Checker) Check(hash string, schema any, args json.RawMessage) error {
	cs := c.compiled(hash, func() (*jsonschema.Resolved, error) { return Compile(schema) })
	if cs.err != nil {
		return cs.err
	}
	if cs.schema == nil {
		return nil
	}
	var instance any
	if err := json.Unmarshal(args, &instance); err != nil {
		// Canonical arguments are valid JSON; what fails here is a number
		// beyond a 64-bit float, which no schema can check.
		return &MismatchError{Reason: "a number is out of range"}
	}
	switch err := cs.schema.ValidateWithin(instance, MaxSteps); {
	case errors.Is(err, jsonschema.ErrBudget):
		return ErrTooCostly
	case err != nil:
		return &MismatchError{Reason: clip(err.Error(), MaxReason)}
	}
	return nil
}

func (c *Checker) compiled(key string, compile func() (*jsonschema.Resolved, error)) compiled {
	c.mu.Lock()
	cs, ok := c.cache[key]
	c.mu.Unlock()
	if ok {
		return cs
	}
	r, err := compile()
	cs = compiled{schema: r, err: err}
	n := 1
	if r != nil {
		n = r.Nodes()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil || len(c.cache) >= MaxCached || c.nodes+n > maxCachedNodes {
		c.cache, c.nodes = make(map[string]compiled), 0
	}
	if n <= maxCachedNodes {
		c.cache[key] = cs
		c.nodes += n
	}
	return cs
}

// cached reports how many compiled schemas c keeps (for tests).
func (c *Checker) cached() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cache)
}

// The $schema values the library validates with; it refuses any other at
// every call, so Privo refuses it here, where the review can show it.
var versions = map[string]bool{
	"": true,
	"http://json-schema.org/draft-07/schema#":      true,
	"https://json-schema.org/draft-07/schema#":     true,
	"https://json-schema.org/draft/2020-12/schema": true,
}

// Compile prepares schema for checking. A nil schema (none sent) gives nil:
// the tool takes any object. An error wraps ErrUnreadable.
func Compile(schema any) (*jsonschema.Resolved, error) {
	if schema == nil {
		return nil, nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	if string(raw) == "null" {
		return nil, nil
	}
	if err := bounded(raw); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	if !versions[s.Schema] {
		return nil, fmt.Errorf("%w: $schema %q is not draft-07 or 2020-12", ErrUnreadable, clip(s.Schema, 80))
	}
	// No Loader: a reference outside the schema fails here, unfetched.
	r, err := s.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	if err := r.ValidateWithin(map[string]any{}, trialSteps); errors.Is(err, jsonschema.ErrBudget) {
		return nil, fmt.Errorf("%w: checking even empty arguments does not end within the limit (a $ref loop?)", ErrUnreadable)
	}
	return r, nil
}

// bounded refuses schema text that is too large or nested too deeply to
// decode cheaply.
func bounded(raw []byte) error {
	if len(raw) > maxSchemaBytes {
		return fmt.Errorf("the schema is larger than %d bytes", maxSchemaBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			if depth++; depth > maxSchemaNesting {
				return fmt.Errorf("the schema is nested more than %d levels deep", maxSchemaNesting)
			}
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
}

// reviewed caches DefinitionProblem's answers (never the compiled schemas,
// which can be large) by the definition's hash: the review reads every pin,
// pending ones too, each time it is shown.
var reviewed struct {
	mu      sync.Mutex
	answers map[[sha256.Size]byte]string
}

// DefinitionProblem reads a tool's stored definition and says why its input
// schema cannot be used, or "" when it can (or when the definition is not a
// tool's). It is for the review, before anyone accepts the definition.
func DefinitionProblem(definition []byte) string {
	var def struct {
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal(definition, &def); err != nil || len(def.InputSchema) == 0 {
		return ""
	}
	sum := sha256.Sum256(definition)
	reviewed.mu.Lock()
	answer, ok := reviewed.answers[sum]
	reviewed.mu.Unlock()
	if ok {
		return answer
	}
	var schema any
	if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
		answer = clip(ErrUnreadable.Error(), MaxReason)
	} else if _, err := Compile(schema); err != nil {
		answer = clip(err.Error(), MaxReason)
	}
	reviewed.mu.Lock()
	defer reviewed.mu.Unlock()
	if reviewed.answers == nil || len(reviewed.answers) >= MaxCached {
		reviewed.answers = map[[sha256.Size]byte]string{}
	}
	reviewed.answers[sum] = answer
	return answer
}

// clip cuts s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
