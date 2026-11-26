package scan

import (
	"bytes"
	"encoding/json"
	"sort"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultResultCap is the default byte budget scanned across all text in a
// single tool result, prompt or resource content (256 KiB).
const DefaultResultCap = 256 << 10

const (
	maxResultNodes = 100_000
	maxResultDepth = 128
)

// ResultFinding is one sign found in a tool result, prompt or resource content.
// It holds only the rule name, severity and byte offset, never a text snippet.
type ResultFinding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Offset   int    `json:"offset"`
}

// Result scans the text fields of an MCP CallToolResult, GetPromptResult or
// ReadResourceResult using the existing hidden-character and instruction rules,
// up to capBytes across all walked text in the result (default DefaultResultCap).
// Findings never contain result text.
func Result(res any, capBytes int) []ResultFinding {
	if capBytes <= 0 {
		capBytes = DefaultResultCap
	}
	s := &resultScanner{left: capBytes}
	switch r := res.(type) {
	case *mcp.CallToolResult:
		if r == nil {
			return []ResultFinding{}
		}
		for _, c := range r.Content {
			if s.done() {
				break
			}
			s.scanContent(c)
		}
		if !s.done() && r.StructuredContent != nil {
			s.scanStructured(r.StructuredContent, 0)
		}
	case *mcp.GetPromptResult:
		if r == nil {
			return []ResultFinding{}
		}
		s.scanText(r.Description)
		for _, m := range r.Messages {
			if s.done() {
				break
			}
			if m != nil {
				s.scanContent(m.Content)
			}
		}
	case *mcp.ReadResourceResult:
		if r == nil {
			return []ResultFinding{}
		}
		for _, rc := range r.Contents {
			if s.done() {
				break
			}
			s.scanResource(rc)
		}
	}
	out := s.out
	if out == nil {
		return []ResultFinding{}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Offset != out[j].Offset {
			return out[i].Offset < out[j].Offset
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

type resultScanner struct {
	left  int
	base  int
	nodes int
	out   []ResultFinding
}

func (s *resultScanner) done() bool {
	return s.left <= 0 || len(s.out) >= MaxFindings || s.nodes >= maxResultNodes
}

func (s *resultScanner) scanResource(rc *mcp.ResourceContents) {
	if rc == nil || s.done() {
		return
	}
	s.scanText(rc.Text)
}

func (s *resultScanner) scanContent(c mcp.Content) {
	if c == nil || s.done() {
		return
	}
	switch v := c.(type) {
	case *mcp.TextContent:
		if v != nil {
			s.scanText(v.Text)
		}
	case *mcp.EmbeddedResource:
		if v != nil {
			s.scanResource(v.Resource)
		}
	case *mcp.ResourceLink:
		if v != nil {
			s.scanText(v.Name)
			s.scanText(v.Title)
			s.scanText(v.Description)
		}
	}
}

func (s *resultScanner) scanStructured(v any, depth int) {
	if v == nil || s.done() || depth > maxResultDepth {
		return
	}
	s.nodes++
	if s.nodes > maxResultNodes {
		return
	}
	switch t := v.(type) {
	case string:
		s.scanText(t)
	case json.RawMessage:
		s.scanJSONBytes(t, depth)
	case []byte:
		s.scanJSONBytes(t, depth)
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s.done() {
				break
			}
			s.scanText(k)
			s.scanStructured(t[k], depth+1)
		}
	case map[string]string:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s.done() {
				break
			}
			s.scanText(k)
			s.scanText(t[k])
		}
	case []any:
		for _, e := range t {
			if s.done() {
				break
			}
			s.scanStructured(e, depth+1)
		}
	case []string:
		for _, e := range t {
			if s.done() {
				break
			}
			s.scanText(e)
		}
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		// Non-string JSON scalars carry no natural-language text.
	default:
		// Structs or typed containers passed directly in memory.
		raw, err := json.Marshal(t)
		if err == nil {
			s.scanJSONBytes(raw, depth)
		}
	}
}

func (s *resultScanner) scanJSONBytes(raw []byte, depth int) {
	if len(raw) == 0 || s.done() {
		return
	}
	var val any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&val); err != nil {
		s.scanText(string(raw))
		return
	}
	s.scanStructured(val, depth+1)
}

func (s *resultScanner) scanText(text string) {
	if s.left <= 0 || len(s.out) >= MaxFindings || text == "" {
		return
	}
	chunk := text
	exhausted := false
	if len(chunk) > s.left {
		chunk = clipUTF8(chunk, s.left)
		exhausted = true
	}
	if chunk != "" {
		if idx, _ := hidden(chunk); idx >= 0 && len(s.out) < MaxFindings {
			s.out = append(s.out, ResultFinding{
				Rule:     "hidden-characters",
				Severity: Warn,
				Offset:   s.base + idx,
			})
		}
		for _, r := range rules {
			if len(s.out) >= MaxFindings {
				break
			}
			if loc := r.re.FindStringIndex(chunk); loc != nil {
				s.out = append(s.out, ResultFinding{
					Rule:     r.name,
					Severity: r.severity,
					Offset:   s.base + loc[0],
				})
			}
		}
		s.base += len(chunk)
		s.left -= len(chunk)
	}
	if exhausted {
		s.left = 0
	}
}

// clipUTF8 truncates s to at most maxBytes without splitting a valid multi-byte
// UTF-8 sequence at the boundary (which would create a spurious RuneError).
func clipUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for back := 1; back < utf8.UTFMax && cut-back >= 0; back++ {
		start := cut - back
		if utf8.RuneStart(s[start]) {
			r, size := utf8.DecodeRuneInString(s[start:])
			if r != utf8.RuneError && start+size > cut {
				return s[:start]
			}
			break
		}
	}
	return s[:cut]
}
