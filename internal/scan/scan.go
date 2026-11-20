// Package scan reads the definition of an upstream tool, prompt or resource
// for signs of hidden instructions (F11, ADR 0001 pins). It is a hint for the
// person who reviews a definition before accepting it: it never accepts,
// blocks or changes anything, and finding nothing proves nothing.
//
// It walks every string in the definition (description, title, the input
// schema's property descriptions, annotations), since the model reads all of
// them and a reviewer might read only the description.
package scan

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Severity of a finding.
const (
	Warn = "warn" // looks like an instruction to the model, or text a person cannot see
	Note = "note" // unusual; worth a look
)

// Finding is one sign found in a definition.
type Finding struct {
	Path     string `json:"path"`     // where: "description", "inputSchema.properties.repo.description", ...
	Rule     string `json:"rule"`     // a short, stable name
	Severity string `json:"severity"` // warn | note
	Message  string `json:"message"`  // what was found, in plain words
	Excerpt  string `json:"excerpt"`  // the text around it, hidden characters shown as ⟦U+XXXX⟧
}

// MaxFindings bounds what one definition reports.
const MaxFindings = 50

// LongText is the length (in characters) from which a text is called long.
const LongText = 1500

type rule struct {
	name, severity, message string
	re                      *regexp.Regexp
}

// The phrases are English and lower-case matched; they catch the common
// shapes of tool poisoning, not every one.
var rules = []rule{
	{"instruction-tag", Warn, "a tag that addresses the model (<IMPORTANT>, <system>, <instructions>…)",
		regexp.MustCompile(`(?i)</?\s*(important|system|instructions?|secret|hidden|admin|assistant|prompt)\s*>|\[/?inst\]|<\|im_start\|>`)},
	{"override", Warn, "asks the model to ignore or override its instructions",
		regexp.MustCompile(`(?i)\b(ignore|disregard|forget|override)\b[^.\n]{0,40}\b(previous|prior|above|earlier|all|system|other)\b[^.\n]{0,20}\b(instructions?|prompts?|rules?|messages?)\b`)},
	{"conceal", Warn, "asks the model to keep something from the user",
		regexp.MustCompile(`(?i)\b(do not|don't|never|without)\b[^.\n]{0,30}\b(tell|telling|inform|informing|mention|mentioning|show|showing|reveal|revealing|notify|notifying|alert|alerting)\b[^.\n]{0,20}\b(the )?user\b`)},
	{"call-first", Warn, "tells the model to do something before or instead of the call",
		regexp.MustCompile(`(?i)\b(before|prior to)\b[^.\n]{0,20}\b(using|calling|running|answering|responding)\b[^.\n]{0,40}\b(you must|must|always|first)\b|\byou must (first|always)\b`)},
	{"secret-files", Warn, "names files with keys or credentials, or exfiltration",
		regexp.MustCompile(`(?i)~/\.ssh|\bid_(rsa|ed25519|ecdsa)\b|\.aws/credentials|\.netrc\b|\.git-credentials\b|/etc/(passwd|shadow)\b|\bexfiltrat`)},
	{"secret-words", Note, "mentions keys, passwords or tokens; common in honest tools, read why",
		regexp.MustCompile(`(?i)\bprivate keys?\b|\bapi[_ ]?keys?\b|\bpasswords?\b|\bsecret keys?\b|\baccess tokens?\b|\bcredentials\b`)},
	{"system-prompt", Warn, "mentions the system prompt or the conversation",
		regexp.MustCompile(`(?i)\bsystem prompt\b|\bconversation history\b|\bchat history\b|\bprevious messages\b`)},
	{"encoded", Note, "a long encoded-looking run of characters (base64 or hex)",
		regexp.MustCompile(`[A-Za-z0-9+/=_-]{120,}`)},
}

// Scan returns what it finds in the JSON definition def, sorted by path and
// rule. A definition that is not JSON gives one finding.
func Scan(def []byte) []Finding {
	var v any
	if err := json.Unmarshal(def, &v); err != nil {
		return []Finding{{Path: "", Rule: "unreadable", Severity: Warn, Message: "the definition is not valid JSON"}}
	}
	out := []Finding{} // never nil: an API answers [] for a clean definition
	walk("", v, &out)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Rule < out[j].Rule
	})
	if len(out) > MaxFindings {
		out = out[:MaxFindings]
	}
	return out
}

func walk(path string, v any, out *[]Finding) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			// A key is text the model reads too (a property name).
			text(join(path, k)+" (name)", k, out)
			walk(join(path, k), t[k], out)
		}
	case []any:
		for i, e := range t {
			walk(fmt.Sprintf("%s[%d]", path, i), e, out)
		}
	case string:
		text(path, t, out)
	}
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func text(path, s string, out *[]Finding) {
	if len(*out) >= MaxFindings || s == "" {
		return
	}
	if i, r := hidden(s); i >= 0 {
		*out = append(*out, Finding{Path: path, Rule: "hidden-characters", Severity: Warn,
			Message: fmt.Sprintf("characters a person cannot see (first: U+%04X)", r), Excerpt: excerpt(s, i, i+utf8.RuneLen(r))})
	}
	for _, r := range rules {
		if loc := r.re.FindStringIndex(s); loc != nil {
			*out = append(*out, Finding{Path: path, Rule: r.name, Severity: r.severity, Message: r.message, Excerpt: excerpt(s, loc[0], loc[1])})
		}
	}
	if n := utf8.RuneCountInString(s); n >= LongText {
		*out = append(*out, Finding{Path: path, Rule: "long-text", Severity: Note,
			Message: fmt.Sprintf("a long text (%d characters); read all of it", n)})
	}
}

// hidden returns the byte offset and value of the first character a person
// cannot see in a review: format characters (zero-width, bidirectional
// controls, tag characters), private-use and unassigned code points, and
// control characters other than tab and newline. It returns -1 when none.
func hidden(s string) (int, rune) {
	for i, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
		case r == utf8.RuneError:
			return i, r
		case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r), unicode.IsControl(r):
			return i, r
		case !unicode.IsPrint(r) && !unicode.IsSpace(r):
			return i, r
		}
	}
	return -1, 0
}

// excerpt is the text around s[start:end], at most about 160 characters, with
// hidden characters made visible.
func excerpt(s string, start, end int) string {
	const around = 60
	from, to := start, end
	for n := 0; from > 0 && n < around; n++ {
		_, size := utf8.DecodeLastRuneInString(s[:from])
		from -= size
	}
	for n := 0; to < len(s) && n < around; n++ {
		_, size := utf8.DecodeRuneInString(s[to:])
		to += size
	}
	if to-from > 4*(2*around+40) { // a very long match: keep its start
		to = from + 4*(2*around+40)
		for to > from && !utf8.RuneStart(s[to]) {
			to--
		}
	}
	var b strings.Builder
	if from > 0 {
		b.WriteString("…")
	}
	b.WriteString(Reveal(s[from:to]))
	if to < len(s) {
		b.WriteString("…")
	}
	return b.String()
}

// Reveal shows characters a person cannot see as ⟦U+XXXX⟧.
func Reveal(s string) string {
	var b strings.Builder
	for _, r := range s {
		if i, _ := hidden(string(r)); i == 0 {
			fmt.Fprintf(&b, "⟦U+%04X⟧", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
