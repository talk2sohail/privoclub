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
	for _, up := range []string{"echo://note", "file:///a b/c?d=e#f", "https://x/y"} {
		alias, got, ok := ParseResourceURI(ResourceURI("echo", up))
		if !ok || alias != "echo" || got != up {
			t.Fatalf("%q -> %q %q %v", up, alias, got, ok)
		}
	}
	for _, bad := range []string{"echo://note", "privo://", "privo://echo", "privo://echo/%zz"} {
		if _, _, ok := ParseResourceURI(bad); ok {
			t.Errorf("%q parsed", bad)
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
