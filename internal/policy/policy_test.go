package policy

import (
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/store"
)

func rule(id, server, tool, subject, action string, prio int) store.Policy {
	return store.Policy{ID: id, Server: server, Tool: tool, Subject: subject, Action: action, Priority: prio}
}

func TestEvaluate(t *testing.T) {
	now := time.Now()
	member := Request{Role: "member", TokenID: "t1", Server: "github", Tool: "create_issue"}
	past := now.Add(-time.Minute)
	cases := []struct {
		name  string
		rules []store.Policy
		req   Request
		def   string
		want  string
	}{
		{"no rules, default allow", nil, member, "allow", "allow"},
		{"no rules, default deny", nil, member, "deny", "deny"},
		{"bad default falls back to allow", nil, member, "maybe", "allow"},
		{"deny beats allow at same priority", []store.Policy{rule("a", "*", "*", "*", "allow", 0), rule("d", "github", "create_*", "*", "deny", 0)}, member, "allow", "deny"},
		{"ask beats allow", []store.Policy{rule("a", "*", "*", "*", "allow", 0), rule("k", "*", "*", "role:member", "ask", 0)}, member, "allow", "ask"},
		{"higher priority grant beats ask", []store.Policy{rule("k", "*", "*", "*", "ask", 0), rule("g", "github", "create_issue", "token:t1", "allow", 100)}, member, "allow", "allow"},
		{"other server ignored", []store.Policy{rule("d", "slack", "*", "*", "deny", 0)}, member, "allow", "allow"},
		{"other role ignored", []store.Policy{rule("d", "*", "*", "role:admin", "deny", 0)}, member, "allow", "allow"},
		{"other token ignored", []store.Policy{rule("d", "*", "*", "token:t2", "deny", 0)}, member, "allow", "allow"},
		{"glob mismatch ignored", []store.Policy{rule("d", "*", "delete_*", "*", "deny", 0)}, member, "allow", "allow"},
		{"expired rule ignored", []store.Policy{{ID: "x", Server: "*", Tool: "*", Subject: "*", Action: "deny", ExpiresAt: &past}}, member, "allow", "allow"},
		{"invalid action ignored", []store.Policy{rule("x", "*", "*", "*", "explode", 50)}, member, "allow", "allow"},
		{"literal name with glob chars", []store.Policy{rule("d", "*", escapeGlob("a*b"), "*", "deny", 0)}, Request{Server: "s", Tool: "aXb"}, "allow", "allow"},
	}
	for _, c := range cases {
		got := Evaluate(c.rules, c.req, c.def, now)
		if got.Action != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, got.Action, got.Reason, c.want)
		}
	}
}

func TestAnnotationsOnlyTighten(t *testing.T) {
	now := time.Now()
	yes := true
	destructive := Request{Server: "s", Tool: "drop", Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes}}
	if d := Evaluate(nil, destructive, "allow", now); d.Action != "ask" {
		t.Fatalf("destructive tool under default allow: %s", d.Action)
	}
	if d := Evaluate(nil, destructive, "deny", now); d.Action != "deny" {
		t.Fatalf("annotation relaxed a default deny: %s", d.Action)
	}
	readOnly := Request{Server: "s", Tool: "get", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}
	if d := Evaluate(nil, readOnly, "ask", now); d.Action != "ask" {
		t.Fatalf("readOnlyHint relaxed a default ask: %s", d.Action)
	}
	unannotated := Request{Server: "s", Tool: "x"}
	if d := Evaluate(nil, unannotated, "allow", now); d.Action != "allow" {
		t.Fatalf("missing annotations changed the default: %s", d.Action)
	}
}

func TestPresetRules(t *testing.T) {
	tools := []PresetTool{
		{Tool: &mcp.Tool{Name: "get_issue", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, Hash: "h-get"},
		{Tool: &mcp.Tool{Name: "create_issue"}, Hash: "h-create"},
		{Tool: &mcp.Tool{Name: "weird*name"}, Hash: "h-weird"},
	}
	hashes := map[string]string{"get_issue": "h-get", "create_issue": "h-create", "weird*name": "h-weird"}
	rules, err := PresetRules(PresetReadOnly, "github", tools)
	if err != nil || len(rules) != 3 {
		t.Fatalf("%v %v", rules, err)
	}
	for i := range rules {
		rules[i].ID = rules[i].Tool
	}
	check := func(tool, want string) {
		t.Helper()
		if d := Evaluate(rules, Request{Server: "github", Tool: tool, PinHash: hashes[tool]}, "allow", time.Now()); d.Action != want {
			t.Errorf("%s: %s, want %s", tool, d.Action, want)
		}
	}
	check("get_issue", "allow")
	check("create_issue", "deny")
	check("weird*name", "deny")
	check("weirdXname", "allow") // escaped: no accidental wildcard
	if _, err := PresetRules("nope", "github", tools); err == nil {
		t.Fatal("unknown preset accepted")
	}
	ask, _ := PresetRules(PresetAskBeforeWrites, "github", tools)
	if ask[1].Action != "ask" || ask[0].Action != "allow" {
		t.Fatalf("ask-before-writes: %+v", ask)
	}
}

func TestValidation(t *testing.T) {
	for _, s := range []string{"*", "role:member", "role:approver", "token:abc"} {
		if !ValidSubject(s) {
			t.Errorf("%q rejected", s)
		}
	}
	for _, s := range []string{"", "role:king", "token:", "user:x"} {
		if ValidSubject(s) {
			t.Errorf("%q accepted", s)
		}
	}
	if ValidGlob("[") || ValidGlob("") || !ValidGlob("create_*") {
		t.Error("glob validation wrong")
	}
}

func TestStarCoversSlashes(t *testing.T) {
	now := time.Now()
	deny := []store.Policy{rule("d", "fs", "*", "*", "deny", 0)}
	for _, name := range []string{"delete", "admin/delete", "a/b/c"} {
		if d := Evaluate(deny, Request{Server: "fs", Tool: name}, "allow", now); d.Action != "deny" {
			t.Errorf("deny * did not cover %q", name)
		}
	}
	mid := []store.Policy{rule("d", "*", "*delete*", "*", "deny", 0)}
	if d := Evaluate(mid, Request{Server: "s", Tool: "repo/delete"}, "allow", now); d.Action != "deny" {
		t.Error("*delete* did not cover repo/delete")
	}
}

func TestPresetRuleFollowsReviewedDefinition(t *testing.T) {
	rules, _ := PresetRules(PresetReadOnly, "s", []PresetTool{{Tool: &mcp.Tool{Name: "t", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, Hash: "old"}})
	rules[0].ID = "p"
	if d := Evaluate(rules, Request{Server: "s", Tool: "t", PinHash: "old"}, "ask", time.Now()); d.Action != "allow" {
		t.Fatalf("reviewed definition: %s", d.Action)
	}
	if d := Evaluate(rules, Request{Server: "s", Tool: "t", PinHash: "new"}, "ask", time.Now()); d.Action != "ask" {
		t.Fatalf("changed definition kept the preset's allow: %s", d.Action)
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		p, s string
		ok   bool
	}{
		{"*", "", true}, {"*", "a/b", true}, {"a*", "abc", true}, {"a*c", "abxc", true}, {"a*c", "abxd", false},
		{"?", "a", true}, {"?", "", false}, {"a?c", "a/c", true}, {"[ab]x", "bx", true}, {"[!ab]x", "bx", false},
		{"[a-c]", "b", true}, {"[a-c]", "d", false}, {`a\*b`, "a*b", true}, {`a\*b`, "axb", false},
		{"*a*a*a*a*a*b", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false}, // no exponential blow-up
		{"get_*", "get_issue", true}, {"get_*", "list_issue", false},
	}
	for _, c := range cases {
		got, err := globMatch(c.p, c.s)
		if err != nil || got != c.ok {
			t.Errorf("globMatch(%q, %q) = %v, %v; want %v", c.p, c.s, got, err, c.ok)
		}
	}
	for _, bad := range []string{"[", "[abc", `a\`} {
		if ValidGlob(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
