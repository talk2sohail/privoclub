package gateway

import (
	"sort"
	"strings"
	"testing"

	"privo.club/privo/internal/upstream"
)

func TestPublicName(t *testing.T) {
	if got := PublicName("echo", "echo"); got != "echo__echo" {
		t.Fatalf("got %q", got)
	}
	long := PublicName("github", strings.Repeat("create_pull_request_review_", 3))
	if len(long) > MaxPublicName {
		t.Fatalf("%q is %d chars", long, len(long))
	}
	a, b := PublicName("srv", "get.issue"), PublicName("srv", "get/issue")
	if a == b || strings.ContainsAny(a, "./") {
		t.Fatalf("normalised names collide or keep bad chars: %q %q", a, b)
	}
	first, second := PublicName("srv", "a.b"), PublicName("srv", "a.b")
	if first != second {
		t.Fatal("not deterministic")
	}
}

func TestResourceURIRoundTrip(t *testing.T) {
	for _, up := range []string{"echo://note", "file:///a b/c?d=e#f", "https://x/y", "echo://100%"} {
		alias, got, ok := ParseResourceURI(ResourceURI("echo", up))
		if !ok || alias != "echo" || got != up {
			t.Fatalf("%q -> %q %q %v", up, alias, got, ok)
		}
	}
	// Expanded template URIs with raw slashes and percent-encoded segments parse cleanly.
	for _, tc := range []struct {
		uri, wantAlias, wantRaw, wantUnescaped string
	}{
		{"privo://gh/repos/acme/app", "gh", "repos/acme/app", "repos/acme/app"},
		{"privo://gh/repos/a%2Fb/app", "gh", "repos/a%2Fb/app", "repos/a/b/app"},
		{"privo://api/api://items?page=2&per_page=10", "api", "api://items?page=2&per_page=10", "api://items?page=2&per_page=10"},
	} {
		alias, raw, unescaped, ok := parseResourceURI(tc.uri)
		if !ok || alias != tc.wantAlias || raw != tc.wantRaw || unescaped != tc.wantUnescaped {
			t.Errorf("%q -> (%q, %q, %q, %v), want (%q, %q, %q, true)", tc.uri, alias, raw, unescaped, ok, tc.wantAlias, tc.wantRaw, tc.wantUnescaped)
		}
	}
	for _, bad := range []string{
		"echo://note",
		"privo://",
		"privo://echo",
		"privo://echo/",
		"privo://echo/%zz",
		"privo://../secret",
		"privo://echo?evil=1/note",
		"privo://echo#frag/note",
		"privo://echo@other/note",
		"privo://ECHO/note",
		"privo://a/note",
		"privo://echo/../other/note",
		"privo://echo/repos/%2e%2e/secret",
		"privo://echo/repos/%252e%252e/secret",
		"privo://echo/repos/..\\secret",
		"privo://echo/repos\x00/a",
	} {
		if _, _, ok := ParseResourceURI(bad); ok {
			t.Errorf("%q parsed, want rejected", bad)
		}
	}
}

func TestResourceTemplateURI(t *testing.T) {
	cases := []struct {
		alias string
		tmpl  string
		want  string
	}{
		{"github", "repos/{owner}/{repo}", "privo://github/repos/{owner}/{repo}"},
		{"docs", "file:///workspace/{+path}", "privo://docs/file:///workspace/{+path}"},
		{"api", "api://items{?page,per_page}", "privo://api/api://items{?page,per_page}"},
	}
	for _, tc := range cases {
		got := ResourceTemplateURI(tc.alias, tc.tmpl)
		if got != tc.want {
			t.Fatalf("ResourceTemplateURI(%q, %q) = %q, want %q", tc.alias, tc.tmpl, got, tc.want)
		}
		alias, tmpl, ok := ParseResourceTemplateURI(got)
		if !ok || alias != tc.alias || tmpl != tc.tmpl {
			t.Fatalf("ParseResourceTemplateURI(%q) = (%q, %q, %v), want (%q, %q, true)", got, alias, tmpl, ok, tc.alias, tc.tmpl)
		}
	}
	for _, bad := range []string{
		"repos/{owner}",
		"privo://",
		"privo://github",
		"privo://github/",
		"privo://github/../other/{id}",
		"privo://github/repos/%2e%2e/{id}",
		"privo://github?q=1/repos/{id}",
		"privo://GITHUB/repos/{id}",
		"privo://github/repos/{unclosed",
	} {
		if _, _, ok := ParseResourceTemplateURI(bad); ok {
			t.Errorf("ParseResourceTemplateURI(%q) succeeded, want false", bad)
		}
	}
}

func TestCollidingPublicNamesAreHidden(t *testing.T) {
	crafted := PublicName("srv", "x.") // "srv__x__<hash>"
	literal := strings.TrimPrefix(crafted, "srv__")
	if PublicName("srv", literal) != crafted {
		t.Skip("construction no longer collides")
	}
	items := []upstream.Item{{Alias: "srv", Name: "x."}, {Alias: "srv", Name: literal}, {Alias: "srv", Name: "ok"}}
	sort.Slice(items, func(i, j int) bool {
		return PublicName(items[i].Alias, items[i].Name) < PublicName(items[j].Alias, items[j].Name)
	})
	got := dropCollisions(items, nil)
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("collision not removed: %+v", got)
	}
}
