package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/url"
	"regexp"
	"strings"

	"github.com/yosida95/uritemplate/v3"

	"privo.club/privo/internal/upstream"
)

var aliasRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,15}$`)

// MaxPublicName keeps names within client limits after clients add their own
// prefix (Claude Code adds "mcp__<server>__"; Cursor caps server plus tool at
// 60). See ADR 0002.
const MaxPublicName = 46

// PublicName returns the client-facing name for an upstream item.
// Aliases match ^[a-z][a-z0-9-]{1,15}$, so "__" never occurs inside one.
func PublicName(alias, upstream string) string {
	var b strings.Builder
	for _, r := range upstream {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := alias + "__" + b.String()
	if len(name) <= MaxPublicName && b.String() == upstream {
		return name
	}
	// Normalised or truncated: add a stable hash of the upstream name so two
	// different upstream names can never collide.
	sum := sha256.Sum256([]byte(upstream))
	suffix := "_" + hex.EncodeToString(sum[:])[:6]
	if len(name)+len(suffix) > MaxPublicName {
		name = name[:MaxPublicName-len(suffix)]
	}
	return name + suffix
}

// ResourceURI rewrites an upstream resource URI as privo://<alias>/<escaped uri>.
func ResourceURI(alias, upstream string) string {
	return "privo://" + alias + "/" + url.PathEscape(upstream)
}

// ResourceTemplateURI rewrites an upstream RFC 6570 resource URI template as
// privo://<alias>/<template>, keeping template expressions and slashes intact.
func ResourceTemplateURI(alias, upstreamTemplate string) string {
	return "privo://" + alias + "/" + upstreamTemplate
}

// ParseResourceURI reverses ResourceURI and also accepts template-expanded
// privo://<alias>/<expanded_uri> forms while isolating the alias and refusing
// path traversal.
func ParseResourceURI(s string) (alias, upstream string, ok bool) {
	alias, _, unescaped, ok := parseResourceURI(s)
	return alias, unescaped, ok
}

// ParseResourceTemplateURI reverses ResourceTemplateURI and verifies that the
// alias is valid, the template is free of path traversal, and the template
// parses as a valid RFC 6570 URI template.
func ParseResourceTemplateURI(s string) (alias, upstreamTemplate string, ok bool) {
	rest, found := strings.CutPrefix(s, "privo://")
	if !found {
		return "", "", false
	}
	alias, tmpl, found := strings.Cut(rest, "/")
	if !found || !aliasRe.MatchString(alias) || !validResourceTemplate(tmpl) {
		return "", "", false
	}
	return alias, tmpl, true
}

func parseResourceURI(s string) (alias, raw, unescaped string, ok bool) {
	rest, found := strings.CutPrefix(s, "privo://")
	if !found {
		return "", "", "", false
	}
	alias, enc, found := strings.Cut(rest, "/")
	if !found || !aliasRe.MatchString(alias) || enc == "" {
		return "", "", "", false
	}
	up, err := url.PathUnescape(enc)
	if err != nil {
		return "", "", "", false
	}
	if hasPathTraversal(enc) || hasPathTraversal(up) {
		return "", "", "", false
	}
	// Reject multi-encoded dot-segments (%252e%252e) without rejecting valid
	// single-encoded literal '%' characters (e.g. %25 -> %).
	cur := up
	for range 2 {
		next, err := url.PathUnescape(cur)
		if err != nil || next == cur {
			break
		}
		if hasPathTraversal(next) {
			return "", "", "", false
		}
		cur = next
	}
	return alias, enc, up, true
}

func hasPathTraversal(s string) bool {
	if strings.ContainsRune(s, '\x00') {
		return true
	}
	for _, seg := range strings.FieldsFunc(s, isTraversalSep) {
		if hasDotDotSegment(seg) {
			return true
		}
	}
	return false
}

func isTraversalSep(r rune) bool {
	return r == '/' || r == '\\' || r == '?' || r == '#' || r == '&' || r == '=' || r == ';'
}

func hasDotDotSegment(seg string) bool {
	cur := seg
	for range 3 {
		if cur == ".." {
			return true
		}
		for _, sub := range strings.FieldsFunc(cur, isTraversalSep) {
			if sub == ".." {
				return true
			}
		}
		next, err := url.PathUnescape(cur)
		if err != nil || next == cur {
			break
		}
		cur = next
	}
	return false
}

func validResourceTemplate(pattern string) bool {
	if pattern == "" || hasPathTraversal(pattern) {
		return false
	}
	_, err := uritemplate.New(pattern)
	return err == nil
}

func matchResourceTemplate(pattern, raw, unescaped string) (string, bool) {
	if pattern == "" || hasPathTraversal(pattern) {
		return "", false
	}
	t, err := uritemplate.New(pattern)
	if err != nil {
		return "", false
	}
	if t.Match(raw) != nil {
		return raw, true
	}
	if unescaped != raw && t.Match(unescaped) != nil {
		return unescaped, true
	}
	return "", false
}

// dropCollisions removes every item whose public name is shared with another,
// so a crafted upstream name can never shadow a reviewed tool. Input must be
// sorted by public name.
func dropCollisions(items []upstream.Item, log *slog.Logger) []upstream.Item {
	out := items[:0:0]
	for i := 0; i < len(items); {
		j := i + 1
		name := PublicName(items[i].Alias, items[i].Name)
		for j < len(items) && PublicName(items[j].Alias, items[j].Name) == name {
			j++
		}
		if j-i == 1 {
			out = append(out, items[i])
		} else if log != nil {
			log.Warn("public name collision; items hidden", "name", name, "count", j-i, "server", items[i].Alias)
		}
		i = j
	}
	return out
}
