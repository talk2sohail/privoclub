package ids

import (
	"regexp"
	"testing"
)

var shape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNew(t *testing.T) {
	seen := map[string]bool{}
	prev := ""
	for i := 0; i < 1000; i++ {
		id := New()
		if !shape.MatchString(id) {
			t.Fatalf("bad UUIDv7 %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate %q", id)
		}
		seen[id] = true
		if prev != "" && id[:13] < prev[:13] {
			t.Fatalf("time prefix went backwards: %q after %q", id, prev)
		}
		prev = id
	}
}
