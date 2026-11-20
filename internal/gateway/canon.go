package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// MaxArgumentBytes bounds tool arguments.
const MaxArgumentBytes = 1 << 20

// ErrBadArguments is returned for arguments that cannot be frozen.
var ErrBadArguments = errors.New("arguments must be a JSON object without duplicate keys")

// Canonical returns the frozen form of tool arguments (ADR 0001 stage 7): a
// JSON object, keys sorted, no insignificant whitespace, numbers kept as
// written, duplicate keys rejected. The digest, the approval and the bytes
// sent upstream are all this exact form, so what a person approved is what runs.
func Canonical(raw json.RawMessage) (json.RawMessage, error) {
	// Missing and null arguments both mean "no arguments".
	if t := bytes.TrimSpace(raw); len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return json.RawMessage("{}"), nil
	}
	if len(raw) > MaxArgumentBytes {
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrBadArguments, MaxArgumentBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := decodeValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing data", ErrBadArguments)
	}
	if _, ok := v.(orderedObject); !ok {
		return nil, fmt.Errorf("%w: not an object", ErrBadArguments)
	}
	var buf bytes.Buffer
	if err := encodeValue(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type orderedObject map[string]any

const maxDepth = 64

func decodeValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%w: nested too deeply", ErrBadArguments)
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadArguments, err)
	}
	if t, ok := tok.(json.Delim); ok {
		switch t {
		case '{':
			obj := orderedObject{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("%w: %w", ErrBadArguments, err)
				}
				key, _ := kt.(string)
				if _, dup := obj[key]; dup {
					return nil, fmt.Errorf("%w: duplicate key %q", ErrBadArguments, key)
				}
				if obj[key], err = decodeValue(dec, depth+1); err != nil {
					return nil, err
				}
			}
			_, _ = dec.Token() // '}'
			return obj, nil
		case '[':
			var arr []any
			for dec.More() {
				v, err := decodeValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			_, _ = dec.Token() // ']'
			if arr == nil {
				arr = []any{}
			}
			return arr, nil
		}
	}
	// A number's exponent of more than 9 digits has no honest use, and rules
	// on arguments compare numbers exactly only up to that (ADR 0027).
	if n, ok := tok.(json.Number); ok {
		if i := strings.IndexAny(string(n), "eE"); i >= 0 && len(strings.TrimLeft(string(n)[i+1:], "+-")) > 9 {
			return nil, fmt.Errorf("%w: a number's exponent is too large", ErrBadArguments)
		}
	}
	return tok, nil // string, json.Number, bool or nil
}

func encodeValue(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case orderedObject:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			buf.Write(kb)
			buf.WriteByte(':')
			if err := encodeValue(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeValue(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case json.Number:
		buf.WriteString(t.String())
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(b)
	}
	return nil
}
