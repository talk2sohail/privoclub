// Package policy decides whether a call is allowed, needs a person's
// approval, or is denied (ADR 0001 decision stage).
//
// Rules match a server alias (or "*"), an upstream tool name glob and a
// subject ("*", "role:<role>", "team:<slug>", "user:<id>" or "token:<id>";
// ADR 0020; "user:" for one member, F10 scoped grants). Among the matching rules the
// highest priority wins; within that priority deny beats ask beats allow.
// When no rule matches, the org default applies, tightened (never relaxed)
// by the tool's reviewed annotations: a tool that declares itself
// destructive needs approval under the default "allow".
package policy

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/store"
)

// Request describes a call to decide on.
type Request struct {
	Roles       []string // the caller's roles: a member's current ones (ADR 0025), or a token's own
	TokenID     string
	UserID      string          // the caller's member: a signed-in person or a member's own token (ADR 0021)
	Teams       []string        // the caller's teams, "everyone" included when they are in it
	Server      string          // alias
	Tool        string          // upstream name (tool, prompt, or resource URI)
	PinHash     string          // hash of the reviewed definition being called
	Args        json.RawMessage // the canonical arguments sent upstream; conditions read them (ADR 0027)
	Annotations *mcp.ToolAnnotations
}

// Decision is the outcome with an explanation for the caller and the audit log.
type Decision struct {
	Action string // allow | ask | deny
	RuleID string // empty when the default applied
	Reason string
}

var rank = map[string]int{store.ActionAllow: 0, store.ActionAsk: 1, store.ActionDeny: 2}

// ValidAction reports whether a is a rule action.
func ValidAction(a string) bool { _, ok := rank[a]; return ok }

// ValidSubject reports whether s is a well-formed subject.
func ValidSubject(s string) bool {
	switch {
	case s == "*":
		return true
	case strings.HasPrefix(s, "role:"):
		return store.ValidRole(strings.TrimPrefix(s, "role:"))
	case strings.HasPrefix(s, "team:"):
		t := strings.TrimPrefix(s, "team:")
		return t == store.TeamEveryone || store.ValidTeamSlug(t)
	case strings.HasPrefix(s, "token:"):
		return len(s) > len("token:")
	case strings.HasPrefix(s, "user:"):
		return len(s) > len("user:") && len(s) <= 128
	}
	return false
}

// ValidGlob reports whether g is a usable tool pattern.
func ValidGlob(g string) bool {
	if g == "" || len(g) > 128 {
		return false
	}
	return checkPattern(g) == nil
}

func matches(r store.Policy, req Request, now time.Time) bool {
	if r.ExpiresAt != nil && !now.Before(*r.ExpiresAt) {
		return false
	}
	if r.Server != "*" && r.Server != req.Server {
		return false
	}
	if ok, err := globMatch(r.Tool, req.Tool); err != nil || !ok {
		return false
	}
	// A preset rule only covers the definition it was built from; a changed
	// and re-reviewed tool falls back to the default until presets are reapplied.
	if r.PinHash != "" && r.PinHash != req.PinHash {
		return false
	}
	switch {
	case r.Subject == "*":
		return true
	case strings.HasPrefix(r.Subject, "role:"):
		return slices.Contains(req.Roles, strings.TrimPrefix(r.Subject, "role:"))
	case strings.HasPrefix(r.Subject, "team:"):
		return slices.Contains(req.Teams, strings.TrimPrefix(r.Subject, "team:"))
	case strings.HasPrefix(r.Subject, "token:"):
		return strings.TrimPrefix(r.Subject, "token:") == req.TokenID
	case strings.HasPrefix(r.Subject, "user:"):
		return req.UserID != "" && strings.TrimPrefix(r.Subject, "user:") == req.UserID
	}
	return false
}

// RuleConditions reads a rule's conditions.
func RuleConditions(r store.Policy) ([]Condition, error) {
	if r.Conditions == "" {
		return nil, nil
	}
	var cs []Condition
	if err := json.Unmarshal([]byte(r.Conditions), &cs); err != nil {
		return nil, err
	}
	for i := range cs {
		cs[i] = cs[i].Normalize()
	}
	return cs, nil
}

// conditionsHold reports whether all of a rule's conditions hold. Doubt
// fails closed: a rule whose stored conditions cannot be read, or whose
// condition is unsure about an argument of another type, still matches when
// it denies or asks, and does not when it allows.
func conditionsHold(r store.Policy, args func() any) bool {
	cs, err := RuleConditions(r)
	if err != nil || ValidConditions(cs) != nil {
		return r.Action != store.ActionAllow
	}
	for _, c := range cs {
		switch c.eval(args()) {
		case no:
			return false
		case unsure:
			if r.Action == store.ActionAllow {
				return false
			}
		}
	}
	return true
}

// Evaluate decides a request against rules and the org default action.
// Grants (rules made from an approval) are weighed last: one that matches
// turns an ask into allow, and nothing else (ADR 0027). A grant's priority is
// that of the rule it was made over: it lifts an ask of that rank or lower,
// or the default's, never a higher ask the approver did not see.
func Evaluate(rules []store.Policy, req Request, defaultAction string, now time.Time) Decision {
	d, askRank, byRule := evaluate(rules, req, defaultAction, now)
	if d.Action != store.ActionAsk {
		return d
	}
	for _, g := range rules {
		if g.GrantOf != "" && g.Action == store.ActionAllow && (!byRule || askRank <= g.Priority) && matches(g, req, now) {
			return Decision{Action: store.ActionAllow, RuleID: g.ID, Reason: fmt.Sprintf("grant %s from approval %s, over %s", g.ID, g.GrantOf, d.Reason)}
		}
	}
	return d
}

func evaluate(rules []store.Policy, req Request, defaultAction string, now time.Time) (Decision, int, bool) {
	var decoded bool
	var args any
	argsOf := func() any {
		if !decoded {
			decoded = true
			if len(req.Args) > 0 {
				args, _ = decodeArgs(req.Args)
			}
		}
		return args
	}
	var best *store.Policy
	for i := range rules {
		r := &rules[i]
		if r.GrantOf != "" || !ValidAction(r.Action) || !matches(*r, req, now) || !conditionsHold(*r, argsOf) {
			continue
		}
		if best == nil || r.Priority > best.Priority ||
			(r.Priority == best.Priority && rank[r.Action] > rank[best.Action]) {
			best = r
		}
	}
	if best != nil {
		return Decision{Action: best.Action, RuleID: best.ID,
			Reason: fmt.Sprintf("rule %s (%s %s on %s for %s)", best.ID, best.Action, best.Tool, best.Server, best.Subject)}, best.Priority, true
	}
	if !ValidAction(defaultAction) {
		defaultAction = store.ActionAllow
	}
	if defaultAction == store.ActionAllow && declaresDestructive(req.Annotations) {
		return Decision{Action: store.ActionAsk, Reason: "default: the tool declares itself destructive"}, 0, false
	}
	return Decision{Action: defaultAction, Reason: "default for this organisation"}, 0, false
}

// declaresDestructive is true only on an explicit declaration. Annotations
// may tighten a decision, never relax one (ADR 0001). Note: the MCP spec
// treats a missing destructiveHint as true when readOnlyHint is false; Privo
// does not, because annotations are untrusted and most servers omit them.
// Organisations that want caution by default set the default action to "ask".
func declaresDestructive(a *mcp.ToolAnnotations) bool {
	return a != nil && !a.ReadOnlyHint && a.DestructiveHint != nil && *a.DestructiveHint
}

// Preset names.
const (
	PresetReadOnly        = "read-only"
	PresetAskBeforeWrites = "ask-before-writes"
)

// PresetTool is a reviewed tool and the hash of its pinned definition.
type PresetTool struct {
	Tool *mcp.Tool
	Hash string
}

// PresetRules builds per-tool rules for a server from its reviewed tools.
// "read-only" allows tools that declare readOnlyHint and denies the rest;
// "ask-before-writes" allows read-only tools and asks for the rest. The
// annotations come from definitions an admin reviewed (pins), so trusting
// them here is the admin's decision. Tools added later fall to the default.
func PresetRules(preset, server string, tools []PresetTool) ([]store.Policy, error) {
	other := map[string]string{PresetReadOnly: store.ActionDeny, PresetAskBeforeWrites: store.ActionAsk}[preset]
	if other == "" {
		return nil, fmt.Errorf("unknown preset %q", preset)
	}
	out := make([]store.Policy, 0, len(tools))
	for _, pt := range tools {
		t := pt.Tool
		action := other
		if t.Annotations != nil && t.Annotations.ReadOnlyHint {
			action = store.ActionAllow
		}
		out = append(out, store.Policy{Server: server, Tool: escapeGlob(t.Name), Subject: "*", Action: action,
			Priority: 10, PinHash: pt.Hash, Note: "preset " + preset})
	}
	return out, nil
}

// ExactTool is a tool pattern that matches only name.
func ExactTool(name string) string { return escapeGlob(name) }

// escapeGlob makes a literal tool name safe to use as a pattern.
func escapeGlob(name string) string {
	r := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)
	return r.Replace(name)
}
