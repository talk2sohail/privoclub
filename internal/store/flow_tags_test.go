package store

import (
	"context"
	"errors"
	"testing"
)

func TestFlowTagsCRUDAndDeleteServer(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	org, _ := twoOrgs(t, s)
	addServer(t, s, org, "srv-crm", "crm", nil)
	addServer(t, s, org, "srv-slack", "slack", nil)

	// Invalid enum tag fails and writes nothing.
	if err := s.SetFlowTags(ctx, org, "srv-crm", []FlowTag{{ToolName: "", Tag: "trusted_source"}}); !errors.Is(err, ErrInvalidFlowTag) {
		t.Fatalf("invalid tag: err = %v, want ErrInvalidFlowTag", err)
	}
	// Missing server returns ErrNotFound.
	if err := s.SetFlowTags(ctx, org, "no-such-server", []FlowTag{{ToolName: "", Tag: FlowTagUntrustedSource}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing server: err = %v, want ErrNotFound", err)
	}

	// Set server-wide and tool-level tags, including duplicates that should deduplicate.
	err := s.SetFlowTags(ctx, org, "srv-crm", []FlowTag{
		{ToolName: "", Tag: FlowTagUntrustedSource},
		{ToolName: "", Tag: FlowTagUntrustedSource},
		{ToolName: "create_note", Tag: FlowTagEgressSink},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlowTags(ctx, org, "srv-slack", []FlowTag{{ToolName: "post_message", Tag: FlowTagEgressSink}}); err != nil {
		t.Fatal(err)
	}

	gotCRM, err := s.ListFlowTags(ctx, org, "srv-crm")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotCRM) != 2 || gotCRM[0].ToolName != "" || gotCRM[0].Tag != FlowTagUntrustedSource ||
		gotCRM[1].ToolName != "create_note" || gotCRM[1].Tag != FlowTagEgressSink || gotCRM[0].UpdatedAt.IsZero() {
		t.Fatalf("ListFlowTags(crm) = %+v", gotCRM)
	}

	all, err := s.OrgFlowTags(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("OrgFlowTags = %+v, want 3 entries", all)
	}

	// Replacing with empty slice clears tags for that server without touching other servers.
	if err := s.SetFlowTags(ctx, org, "srv-crm", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListFlowTags(ctx, org, "srv-crm"); len(got) != 0 {
		t.Fatalf("expected crm tags cleared, got %+v", got)
	}
	if all, _ = s.OrgFlowTags(ctx, org); len(all) != 1 || all[0].ServerID != "srv-slack" {
		t.Fatalf("slack tags affected: %+v", all)
	}

	// DeleteServer removes flow_tags rows in the same transaction.
	if err := s.DeleteServer(ctx, org, "srv-slack"); err != nil {
		t.Fatal(err)
	}
	if all, _ = s.OrgFlowTags(ctx, org); len(all) != 0 {
		t.Fatalf("deleted server kept flow tags: %+v", all)
	}
}

func TestFlowTagsIsolatedPerOrg(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	orgA, orgB := twoOrgs(t, s)
	addServer(t, s, orgA, "srv-a", "crm", nil)
	addServer(t, s, orgB, "srv-b", "crm", nil)

	if err := s.SetFlowTags(ctx, orgA, "srv-a", []FlowTag{{ToolName: "", Tag: FlowTagUntrustedSource}}); err != nil {
		t.Fatal(err)
	}
	// Org B cannot tag Org A's server ID.
	if err := s.SetFlowTags(ctx, orgB, "srv-a", []FlowTag{{ToolName: "", Tag: FlowTagEgressSink}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org SetFlowTags: err = %v, want ErrNotFound", err)
	}
	// Org B sees neither ListFlowTags for srv-a nor OrgFlowTags from Org A.
	if got, err := s.ListFlowTags(ctx, orgB, "srv-a"); err != nil || len(got) != 0 {
		t.Fatalf("orgB ListFlowTags(srv-a) = %+v, %v", got, err)
	}
	if got, err := s.OrgFlowTags(ctx, orgB); err != nil || len(got) != 0 {
		t.Fatalf("orgB OrgFlowTags = %+v, %v", got, err)
	}
}
