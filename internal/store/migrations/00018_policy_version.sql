-- +goose Up
-- Per-org policy version bumped transactionally on every policy or
-- policy-default change for hot-path gateway caching (F09 A).
ALTER TABLE orgs ADD COLUMN policy_version INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE orgs DROP COLUMN policy_version;
