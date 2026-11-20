package policy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/store"
)

func cond(t *testing.T, cs ...Condition) string {
	t.Helper()
	if err := ValidConditions(cs); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cs)
	return string(b)
}

func holdsOn(t *testing.T, c Condition, args string) bool {
	t.Helper()
	var a any
	if args != "" {
		var err error
		if a, err = decodeArgs([]byte(args)); err != nil {
			t.Fatal(err)
		}
	}
	return c.Normalize().holds(a)
}

// ADR 0024: each operator, types never mix, nested paths, missing paths.
func TestConditions(t *testing.T) {
	cases := []struct {
		c    Condition
		args string
		want bool
	}{
		{Condition{Arg: "repo", Op: OpEquals, Value: "prod"}, `{"repo":"prod"}`, true},
		{Condition{Arg: "repo", Op: OpEquals, Value: "prod"}, `{"repo":"Prod"}`, false},
		{Condition{Arg: "repo", Op: OpEquals, Value: "prod"}, `{"repo":["prod"]}`, false},
		{Condition{Arg: "repo", Op: OpEquals, Value: "prod"}, `{}`, false},
		{Condition{Arg: "repo", Op: OpEquals, Value: "prod"}, ``, false},
		{Condition{Arg: "n", Op: OpEquals, Value: 1}, `{"n":1.0}`, true},
		{Condition{Arg: "n", Op: OpEquals, Value: 1}, `{"n":"1"}`, false},
		{Condition{Arg: "n", Op: OpEquals, Value: 100}, `{"n":1e2}`, true},
		{Condition{Arg: "n", Op: OpEquals, Value: 1}, `{"n":1e999999999}`, false}, // compared as written, never expanded
		{Condition{Arg: "force", Op: OpEquals, Value: true}, `{"force":true}`, true},
		{Condition{Arg: "force", Op: OpEquals, Value: true}, `{"force":"true"}`, false},
		{Condition{Arg: "o.force", Op: OpEquals, Value: true}, `{"o":{"force":true}}`, true},
		{Condition{Arg: "o.force", Op: OpEquals, Value: true}, `{"o":true}`, false},
		{Condition{Arg: "repo", Op: OpNotEquals, Value: "prod"}, `{"repo":"dev"}`, true},
		{Condition{Arg: "repo", Op: OpNotEquals, Value: "prod"}, `{}`, true},
		{Condition{Arg: "repo", Op: OpNotEquals, Value: "prod"}, `{"repo":"prod"}`, false},
		{Condition{Arg: "db", Op: OpIn, Value: []any{"analytics", "logs"}}, `{"db":"logs"}`, true},
		{Condition{Arg: "db", Op: OpIn, Value: []any{"analytics", "logs"}}, `{"db":"users"}`, false},
		{Condition{Arg: "db", Op: OpIn, Value: []any{"analytics"}}, `{}`, false},
		{Condition{Arg: "branch", Op: OpMatches, Value: "release/*"}, `{"branch":"release/1.2"}`, true},
		{Condition{Arg: "branch", Op: OpMatches, Value: "release/*"}, `{"branch":"main"}`, false},
		{Condition{Arg: "branch", Op: OpMatches, Value: "*"}, `{"branch":7}`, false},
		{Condition{Arg: "tags", Op: OpEquals, Value: []any{"a", "b"}}, `{"tags":["a","b"]}`, true},
		{Condition{Arg: "q", Op: OpExists}, `{"q":null}`, true},
		{Condition{Arg: "q", Op: OpExists}, `{}`, false},
		{Condition{Arg: "q", Op: OpMissing}, `{}`, true},
		{Condition{Arg: "q", Op: OpMissing}, ``, true},
	}
	for _, c := range cases {
		if got := holdsOn(t, c.c, c.args); got != c.want {
			t.Errorf("%+v on %s = %v, want %v", c.c, c.args, got, c.want)
		}
	}
}

func TestConditionValidation(t *testing.T) {
	bad := []Condition{
		{Arg: "", Op: OpEquals, Value: "x"},
		{Arg: "a..b", Op: OpEquals, Value: "x"},
		{Arg: strings.Repeat("a.", 9) + "a", Op: OpEquals, Value: "x"},
		{Arg: "a", Op: "regex", Value: "x"},
		{Arg: "a", Op: OpIn, Value: "x"},
		{Arg: "a", Op: OpIn, Value: []any{}},
		{Arg: "a", Op: OpMatches, Value: 3},
		{Arg: "a", Op: OpMatches, Value: "["},
		{Arg: "a", Op: OpExists, Value: "x"},
		{Arg: "a", Op: OpEquals, Value: strings.Repeat("x", 2000)},
		{Arg: "a\nb", Op: OpEquals, Value: "x"},
	}
	for _, c := range bad {
		if err := ValidConditions([]Condition{c}); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	many := make([]Condition, MaxConditions+1)
	for i := range many {
		many[i] = Condition{Arg: "a", Op: OpExists}
	}
	if ValidConditions(many) == nil {
		t.Error("too many conditions accepted")
	}
}

// A conditioned deny applies to its arguments only, and a grant never lifts
// it, whatever their ranks (ADR 0024 point 6).
func TestGrantOnlyReplacesAsk(t *testing.T) {
	now := time.Now()
	deny := rule("d", "github", "delete_repo", "*", "deny", 0)
	deny.Conditions = cond(t, Condition{Arg: "repo", Op: OpEquals, Value: "prod"})
	ask := rule("k", "github", "*", "*", "ask", 0)
	grant := rule("g", "github", ExactTool("delete_repo"), "user:u-ada", "allow", 0)
	grant.GrantOf = "appr-1"
	rules := []store.Policy{deny, ask, grant}
	req := func(args string) Request {
		return Request{UserID: "u-ada", Server: "github", Tool: "delete_repo", Args: json.RawMessage(args)}
	}
	if d := Evaluate(rules, req(`{"repo":"prod"}`), "allow", now); d.Action != "deny" || d.RuleID != "d" {
		t.Fatalf("prod: %+v", d)
	}
	if d := Evaluate(rules, req(`{"repo":"sandbox"}`), "allow", now); d.Action != "allow" || d.RuleID != "g" {
		t.Fatalf("sandbox: %+v", d)
	}
	// A grant ranked high still does not beat a deny.
	grant.Priority = 1000
	if d := Evaluate([]store.Policy{deny, ask, grant}, req(`{"repo":"prod"}`), "allow", now); d.Action != "deny" {
		t.Fatalf("a high grant over a deny: %+v", d)
	}
	// Without an ask there is nothing for a grant to replace: default allow stays allow, default deny stays deny.
	if d := Evaluate([]store.Policy{grant}, req(`{}`), "deny", now); d.Action != "deny" {
		t.Fatalf("grant over the default deny: %+v", d)
	}
	// Another member's grant does nothing.
	other := Request{UserID: "u-bob", Server: "github", Tool: "delete_repo", Args: json.RawMessage(`{"repo":"sandbox"}`)}
	if d := Evaluate(rules, other, "allow", now); d.Action != "ask" {
		t.Fatalf("bob: %+v", d)
	}
}

// A rule whose stored conditions cannot be read fails closed.
func TestUnreadableConditionsFailClosed(t *testing.T) {
	now := time.Now()
	deny := rule("d", "*", "*", "*", "deny", 0)
	deny.Conditions = "not json"
	allow := rule("a", "*", "*", "*", "allow", 5)
	allow.Conditions = "not json"
	if d := Evaluate([]store.Policy{deny}, Request{Server: "s", Tool: "t"}, "allow", now); d.Action != "deny" {
		t.Fatalf("an unreadable deny: %+v", d)
	}
	if d := Evaluate([]store.Policy{allow}, Request{Server: "s", Tool: "t"}, "deny", now); d.Action != "deny" {
		t.Fatalf("an unreadable allow: %+v", d)
	}
}
