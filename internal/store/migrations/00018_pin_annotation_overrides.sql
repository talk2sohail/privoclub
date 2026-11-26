-- +goose Up
-- F09 B: admin overrides for a tool pin's annotations, bound to the exact
-- (org_id, server_id, kind, name, hash) row so changed definitions never
-- inherit prior overrides.
ALTER TABLE pins ADD COLUMN annotation_overrides TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE pins DROP COLUMN annotation_overrides;
