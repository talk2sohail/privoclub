-- +goose Up
-- ADR 0027: conditions on a call's arguments (a JSON list, '' for none), and
-- the approval a grant was made from ('' for every other rule).
ALTER TABLE policies ADD COLUMN conditions TEXT NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN grant_of TEXT NOT NULL DEFAULT '';

-- Grants made before this change (F10) are marked from their note. They
-- ranked one above the ask they were made over; a grant's priority is now the
-- rank it covers, so they cover the rank below theirs.
UPDATE policies SET grant_of = substr(note, instr(note, ' from approval ') + 15), priority = priority - 1
WHERE grant_of = '' AND action = 'allow' AND expires_at IS NOT NULL AND note LIKE 'Allowed for % by % from approval %';

-- +goose Down
ALTER TABLE policies DROP COLUMN grant_of;
ALTER TABLE policies DROP COLUMN conditions;
