package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Flow tag values (ADR 0032).
const (
	FlowTagUntrustedSource = "untrusted_source"
	FlowTagEgressSink      = "egress_sink"
)

// ErrInvalidFlowTag is returned when a flow tag is outside the closed enum.
var ErrInvalidFlowTag = errors.New("store: invalid flow tag")

// ValidFlowTag reports whether tag is a valid cross-server flow tag.
func ValidFlowTag(tag string) bool {
	return tag == FlowTagUntrustedSource || tag == FlowTagEgressSink
}

// FlowTag marks a server (ToolName == "") or a specific tool on a server as
// an untrusted_source or egress_sink (ADR 0032).
type FlowTag struct {
	ServerID  string
	ToolName  string
	Tag       string
	UpdatedAt time.Time
}

// SetFlowTags replaces all cross-server flow tags for serverID within org.
// It validates every tag against the closed enum and returns ErrNotFound if the
// server does not belong to org.
func (s *Store) SetFlowTags(ctx context.Context, org OrgID, serverID string, tags []FlowTag) error {
	type key struct {
		tool string
		tag  string
	}
	seen := make(map[key]bool, len(tags))
	clean := make([]FlowTag, 0, len(tags))
	for _, t := range tags {
		if !ValidFlowTag(t.Tag) {
			return ErrInvalidFlowTag
		}
		if t.ServerID != "" && t.ServerID != serverID {
			return errors.New("store: flow tag server mismatch")
		}
		k := key{tool: t.ToolName, tag: t.Tag}
		if seen[k] {
			continue
		}
		seen[k] = true
		clean = append(clean, FlowTag{ServerID: serverID, ToolName: t.ToolName, Tag: t.Tag})
	}
	now := ts(time.Now())
	return s.tx(ctx, func(tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM servers WHERE org_id = ? AND id = ?`, org, serverID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM flow_tags WHERE org_id = ? AND server_id = ?`, org, serverID); err != nil {
			return err
		}
		for _, t := range clean {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO flow_tags (org_id, server_id, tool_name, tag, updated_at)
				VALUES (?, ?, ?, ?, ?)`,
				org, serverID, t.ToolName, t.Tag, now); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

// ListFlowTags returns the flow tags configured on serverID within org.
func (s *Store) ListFlowTags(ctx context.Context, org OrgID, serverID string) ([]FlowTag, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT server_id, tool_name, tag, updated_at
		FROM flow_tags
		WHERE org_id = ? AND server_id = ?
		ORDER BY tool_name, tag`, org, serverID)
	if err != nil {
		return nil, err
	}
	return scanFlowTags(rows)
}

// OrgFlowTags returns all flow tags configured for org across all servers.
func (s *Store) OrgFlowTags(ctx context.Context, org OrgID) ([]FlowTag, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT server_id, tool_name, tag, updated_at
		FROM flow_tags
		WHERE org_id = ?
		ORDER BY server_id, tool_name, tag`, org)
	if err != nil {
		return nil, err
	}
	return scanFlowTags(rows)
}

func scanFlowTags(rows *sql.Rows) ([]FlowTag, error) {
	defer func() { _ = rows.Close() }()
	var out []FlowTag
	for rows.Next() {
		var t FlowTag
		var updated string
		if err := rows.Scan(&t.ServerID, &t.ToolName, &t.Tag, &updated); err != nil {
			return nil, err
		}
		t.UpdatedAt = parseTS(updated)
		out = append(out, t)
	}
	return out, rows.Err()
}
