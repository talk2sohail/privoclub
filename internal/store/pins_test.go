package store

import (
	"context"
	"errors"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestAnnotationOverridesPersistPerPinHash(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	org, _ := twoOrgs(t, s)

	p1 := Pin{ServerID: "srv", Kind: "tool", Name: "read_note", Hash: "h1", Definition: `{"name":"read_note"}`}
	p2 := Pin{ServerID: "srv", Kind: "tool", Name: "read_note", Hash: "h2", Definition: `{"name":"read_note","description":"rug pull"}`}
	prompt := Pin{ServerID: "srv", Kind: "prompt", Name: "greet", Hash: "hp", Definition: `{"name":"greet"}`}
	if err := s.RecordSeen(ctx, org, []Pin{p1, p2, prompt}); err != nil {
		t.Fatal(err)
	}

	k1 := PinKey{Kind: "tool", Name: "read_note", Hash: "h1"}
	k2 := PinKey{Kind: "tool", Name: "read_note", Hash: "h2"}
	kp := PinKey{Kind: "prompt", Name: "greet", Hash: "hp"}

	// Non-tool and unknown pins return ErrNotFound.
	for _, bad := range []PinKey{kp, {Kind: "tool", Name: "read_note", Hash: "unknown"}, {Kind: "tool", Name: "missing", Hash: "h1"}} {
		if err := s.SetPinAnnotations(ctx, org, "srv", bad, AnnotationOverrides{ReadOnlyHint: boolPtr(true)}); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetPinAnnotations(%+v) = %v, want ErrNotFound", bad, err)
		}
	}

	ov1 := AnnotationOverrides{ReadOnlyHint: boolPtr(false), DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(false)}
	if err := s.SetPinAnnotations(ctx, org, "srv", k1, ov1); err != nil {
		t.Fatal(err)
	}

	// Re-recording seen definitions leaves existing overrides untouched.
	if err := s.RecordSeen(ctx, org, []Pin{p1, p2}); err != nil {
		t.Fatal(err)
	}

	// PinDefinition returns overrides for k1 only, never k2.
	got1, err := s.PinDefinition(ctx, org, "srv", k1)
	if err != nil {
		t.Fatal(err)
	}
	if got1.AnnotationOverrides.IsEmpty() || *got1.AnnotationOverrides.ReadOnlyHint || !*got1.AnnotationOverrides.DestructiveHint || *got1.AnnotationOverrides.OpenWorldHint {
		t.Fatalf("k1 PinDefinition overrides = %+v", got1.AnnotationOverrides)
	}
	got2, err := s.PinDefinition(ctx, org, "srv", k2)
	if err != nil {
		t.Fatal(err)
	}
	if !got2.AnnotationOverrides.IsEmpty() {
		t.Fatalf("k2 inherited overrides: %+v", got2.AnnotationOverrides)
	}

	// ListPins populates overrides per hash.
	pins, err := s.ListPins(ctx, org, "srv")
	if err != nil {
		t.Fatal(err)
	}
	byHash := map[string]Pin{}
	for _, p := range pins {
		byHash[p.Hash] = p
	}
	if byHash["h1"].AnnotationOverrides.IsEmpty() || !byHash["h2"].AnnotationOverrides.IsEmpty() || !byHash["hp"].AnnotationOverrides.IsEmpty() {
		t.Fatalf("ListPins overrides unexpected: h1=%+v h2=%+v hp=%+v", byHash["h1"].AnnotationOverrides, byHash["h2"].AnnotationOverrides, byHash["hp"].AnnotationOverrides)
	}

	// PinAnnotationOverrides returns only non-empty overrides for the server.
	m, err := s.PinAnnotationOverrides(ctx, org, "srv")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 || m[k1].DestructiveHint == nil || !*m[k1].DestructiveHint {
		t.Fatalf("PinAnnotationOverrides = %+v", m)
	}

	// Clearing back to empty removes it from PinAnnotationOverrides.
	if err := s.SetPinAnnotations(ctx, org, "srv", k1, AnnotationOverrides{}); err != nil {
		t.Fatal(err)
	}
	m, err = s.PinAnnotationOverrides(ctx, org, "srv")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Fatalf("cleared overrides still present: %+v", m)
	}
	got1, _ = s.PinDefinition(ctx, org, "srv", k1)
	if !got1.AnnotationOverrides.IsEmpty() {
		t.Fatalf("cleared PinDefinition still has overrides: %+v", got1.AnnotationOverrides)
	}
}

func TestAnnotationOverridesIsolatedPerOrg(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	orgA, orgB := twoOrgs(t, s)

	pinA := Pin{ServerID: "srv", Kind: "tool", Name: "echo", Hash: "h1", Definition: `{"name":"echo"}`}
	pinB := Pin{ServerID: "srv", Kind: "tool", Name: "echo", Hash: "h1", Definition: `{"name":"echo"}`}
	pinOnlyA := Pin{ServerID: "srv", Kind: "tool", Name: "only_a", Hash: "ha", Definition: `{"name":"only_a"}`}
	if err := s.RecordSeen(ctx, orgA, []Pin{pinA, pinOnlyA}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSeen(ctx, orgB, []Pin{pinB}); err != nil {
		t.Fatal(err)
	}

	kEcho := PinKey{Kind: "tool", Name: "echo", Hash: "h1"}
	kOnlyA := PinKey{Kind: "tool", Name: "only_a", Hash: "ha"}

	if err := s.SetPinAnnotations(ctx, orgA, "srv", kEcho, AnnotationOverrides{DestructiveHint: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	// Org B cannot overwrite Org A's pin that only exists in Org A.
	if err := s.SetPinAnnotations(ctx, orgB, "srv", kOnlyA, AnnotationOverrides{ReadOnlyHint: boolPtr(true)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org B modified org A's pin: %v", err)
	}

	// Org B sees no overrides for shared server/tool/hash.
	mB, err := s.PinAnnotationOverrides(ctx, orgB, "srv")
	if err != nil {
		t.Fatal(err)
	}
	if len(mB) != 0 {
		t.Fatalf("org B leaked org A overrides: %+v", mB)
	}
	listB, err := s.ListPins(ctx, orgB, "srv")
	if err != nil {
		t.Fatal(err)
	}
	if len(listB) != 1 || !listB[0].AnnotationOverrides.IsEmpty() {
		t.Fatalf("org B ListPins leaked overrides: %+v", listB)
	}
	defB, err := s.PinDefinition(ctx, orgB, "srv", kEcho)
	if err != nil {
		t.Fatal(err)
	}
	if !defB.AnnotationOverrides.IsEmpty() {
		t.Fatalf("org B PinDefinition leaked overrides: %+v", defB.AnnotationOverrides)
	}
}
