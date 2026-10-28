package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/url"
	"strings"

	"privo.club/privo/internal/upstream"
)

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

// ParseResourceURI reverses ResourceURI.
func ParseResourceURI(s string) (alias, upstream string, ok bool) {
	rest, found := strings.CutPrefix(s, "privo://")
	if !found {
		return "", "", false
	}
	alias, enc, found := strings.Cut(rest, "/")
	if !found || alias == "" || enc == "" {
		return "", "", false
	}
	up, err := url.PathUnescape(enc)
	if err != nil {
		return "", "", false
	}
	return alias, up, true
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
