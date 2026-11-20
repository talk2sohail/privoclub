-- +goose Up
-- ADR 0024: conditions on a call's arguments (a JSON list, '' for none), and
-- the approval a grant was made from ('' for every other rule).
ALTER TABLE policies ADD COLUMN conditions TEXT NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN grant_of TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE policies DROP COLUMN grant_of;
ALTER TABLE policies DROP COLUMN conditions;
