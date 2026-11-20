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

func evalOn(t *testing.T, c Condition, args string) verdict {
	t.Helper()
	var a any
	if args != "" {
		var err error
		if a, err = decodeArgs([]byte(args)); err != nil {
			t.Fatal(err)
		}
	}
	return c.Normalize().eval(a)
}

// ADR 0024 and its security review: each operator; numbers by exact value
// at any length; a value of another type is unsure, never a plain no; nested
// and missing paths.
func TestConditions(t *testing.T) {
	long42 := "42." + strings.Repeat("0", 70)
	cases := []struct {
		c    Condition
		args string
		want verdict
	}{
		{Condition{Arg: "repo", Op: OpEquals, Value: "prod"}, `{"repo":"prod"}`, yes},
		{Condition{Arg: "repo", Op: OpEquals, Value: "prod"}, `{"repo":"Prod"}`, no},
		{Condition{Arg: "repo", Op: OpEquals, Value: "prod"}, `{"repo":["prod"]}`, unsure},
		{Condition{Arg: "repo", Op: OpEquals, Value: "prod"}, `{}`, no},
		{Condition{Arg: "repo", Op: OpEquals, Value: "prod"}, ``, no},
		{Condition{Arg: "repo", Op: OpEqualsAnyCase, Value: "prod"}, `{"repo":"PROD"}`, yes},
		{Condition{Arg: "repo", Op: OpEqualsAnyCase, Value: "prod"}, `{"repo":1}`, unsure},
		{Condition{Arg: "n", Op: OpEquals, Value: 1}, `{"n":1.0}`, yes},
		{Condition{Arg: "n", Op: OpEquals, Value: 1}, `{"n":"1"}`, unsure},
		{Condition{Arg: "n", Op: OpEquals, Value: 100}, `{"n":1e2}`, yes},
		{Condition{Arg: "n", Op: OpEquals, Value: 100}, `{"n":10000e-2}`, yes},
		{Condition{Arg: "n", Op: OpEquals, Value: 0}, `{"n":-0.000}`, yes},
		{Condition{Arg: "n", Op: OpEquals, Value: 42}, `{"n":` + long42 + `}`, yes},
		{Condition{Arg: "n", Op: OpEquals, Value: 42}, `{"n":42.` + strings.Repeat("0", 70) + `1}`, no},
		{Condition{Arg: "n", Op: OpEquals, Value: 1}, `{"n":1e999999999}`, no},
		{Condition{Arg: "force", Op: OpEquals, Value: true}, `{"force":true}`, yes},
		{Condition{Arg: "force", Op: OpEquals, Value: true}, `{"force":"yes"}`, unsure},
		{Condition{Arg: "force", Op: OpEquals, Value: true}, `{"force":1}`, unsure},
		{Condition{Arg: "o.force", Op: OpEquals, Value: true}, `{"o":{"force":true}}`, yes},
		{Condition{Arg: "o.force", Op: OpEquals, Value: true}, `{"o":true}`, no},
		{Condition{Arg: "repo", Op: OpNotEquals, Value: "prod"}, `{"repo":"dev"}`, yes},
		{Condition{Arg: "repo", Op: OpNotEquals, Value: "prod"}, `{}`, yes},
		{Condition{Arg: "repo", Op: OpNotEquals, Value: "prod"}, `{"repo":"prod"}`, no},
		{Condition{Arg: "repo", Op: OpNotEquals, Value: "prod"}, `{"repo":7}`, unsure},
		{Condition{Arg: "id", Op: OpIn, Value: []any{42, 77}}, `{"id":` + long42 + `}`, yes},
		{Condition{Arg: "id", Op: OpIn, Value: []any{42, 77}}, `{"id":"42"}`, unsure},
		{Condition{Arg: "id", Op: OpIn, Value: []any{42, "x"}}, `{"id":"y"}`, no},
		{Condition{Arg: "db", Op: OpIn, Value: []any{"analytics", "logs"}}, `{"db":"logs"}`, yes},
		{Condition{Arg: "db", Op: OpIn, Value: []any{"analytics", "logs"}}, `{"db":"users"}`, no},
		{Condition{Arg: "db", Op: OpIn, Value: []any{"analytics"}}, `{}`, no},
		{Condition{Arg: "db", Op: OpInAnyCase, Value: []any{"Analytics"}}, `{"db":"ANALYTICS"}`, yes},
		{Condition{Arg: "branch", Op: OpMatches, Value: "release/*"}, `{"branch":"release/1.2"}`, yes},
		{Condition{Arg: "branch", Op: OpMatches, Value: "release/*"}, `{"branch":"main"}`, no},
		{Condition{Arg: "branch", Op: OpMatches, Value: "*"}, `{"branch":7}`, unsure},
		{Condition{Arg: "branch", Op: OpMatchesAnyCase, Value: "Release/*"}, `{"branch":"RELEASE/2"}`, yes},
		{Condition{Arg: "tags", Op: OpEquals, Value: []any{"a", "b"}}, `{"tags":["a","b"]}`, yes},
		{Condition{Arg: "tags", Op: OpEquals, Value: []any{"a", "b"}}, `{"tags":["a",2]}`, unsure},
		{Condition{Arg: "q", Op: OpExists}, `{"q":null}`, yes},
		{Condition{Arg: "q", Op: OpExists}, `{}`, no},
		{Condition{Arg: "q", Op: OpMissing}, `{}`, yes},
		{Condition{Arg: "q", Op: OpMissing}, ``, yes},
	}
	for _, c := range cases {
		if got := evalOn(t, c.c, c.args); got != c.want {
			t.Errorf("%+v on %.80s = %v, want %v", c.c, c.args, got, c.want)
		}
	}
}

// Doubt fails closed: an unsure condition lets a deny or ask apply and keeps
// an allow from applying.
func TestUnsureFailsClosed(t *testing.T) {
	now := time.Now()
	deny := rule("d", "*", "transfer", "*", "deny", 0)
	deny.Conditions = cond(t, Condition{Arg: "account_id", Op: OpIn, Value: []any{42, 77}})
	allow := rule("a", "*", "transfer", "*", "allow", 0)
	allow.Conditions = cond(t, Condition{Arg: "account_id", Op: OpEquals, Value: 1})
	req := func(args string) Request { return Request{Server: "bank", Tool: "transfer", Args: json.RawMessage(args)} }
	for _, args := range []string{`{"account_id":"42"}`, `{"account_id":42.` + strings.Repeat("0", 70) + `}`, `{"account_id":42}`} {
		if d := Evaluate([]store.Policy{deny}, req(args), "allow", now); d.Action != "deny" {
			t.Errorf("deny on %.60s: %+v", args, d)
		}
	}
	if d := Evaluate([]store.Policy{deny}, req(`{"account_id":5}`), "allow", now); d.Action != "allow" {
		t.Errorf("another account: %+v", d)
	}
	if d := Evaluate([]store.Policy{allow}, req(`{"account_id":"1"}`), "deny", now); d.Action != "deny" {
		t.Errorf("an unsure allow applied: %+v", d)
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
	defer func() { grant.Priority = 0 }()
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

// Security review 2026-11-20: a grant lifts asks up to the rank it was made
// over, never a higher ask (here a conditioned one) the approver did not see.
func TestGrantCoversItsRankOnly(t *testing.T) {
	now := time.Now()
	low := rule("k", "github", "*", "*", "ask", 0)
	high := rule("m", "github", "merge", "*", "ask", 100)
	high.Conditions = cond(t, Condition{Arg: "base", Op: OpEquals, Value: "main"})
	grant := rule("g", "github", ExactTool("merge"), "user:u-ada", "allow", 0)
	grant.GrantOf = "appr-1"
	rules := []store.Policy{low, high, grant}
	req := func(base string) Request {
		return Request{UserID: "u-ada", Server: "github", Tool: "merge", Args: json.RawMessage(`{"base":"` + base + `"}`)}
	}
	if d := Evaluate(rules, req("dev"), "allow", now); d.Action != "allow" {
		t.Fatalf("dev, under the grant: %+v", d)
	}
	if d := Evaluate(rules, req("main"), "allow", now); d.Action != "ask" || d.RuleID != "m" {
		t.Fatalf("main, a higher ask: %+v", d)
	}
	// A grant made over the default's ask lifts only that.
	grant.Priority = -1000
	if d := Evaluate([]store.Policy{grant}, req("dev"), "ask", now); d.Action != "allow" {
		t.Fatalf("over the default: %+v", d)
	}
	if d := Evaluate([]store.Policy{low, grant}, req("dev"), "allow", now); d.Action != "ask" {
		t.Fatalf("a default grant lifted a rule's ask: %+v", d)
	}
}

func TestConditionNumbersStayExact(t *testing.T) {
	var c Condition
	if err := json.Unmarshal([]byte(`{"arg":"n","op":"equals","value":9007199254740993}`), &c); err != nil {
		t.Fatal(err)
	}
	if n, ok := c.Value.(json.Number); !ok || n.String() != "9007199254740993" {
		t.Fatalf("value %#v", c.Value)
	}
	if err := json.Unmarshal([]byte(`{"arg":"n","op":"equals","value":1,"colour":"red"}`), &c); err == nil {
		t.Fatal("an unknown field was accepted")
	}
}
