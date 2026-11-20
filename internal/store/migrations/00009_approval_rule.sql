-- +goose Up
-- F10: the rule that asked for an approval, so a scoped grant made from it
-- ranks just above that rule (ADR 0005 fatigue controls). '' when the org
-- default asked.
ALTER TABLE approvals ADD COLUMN rule_id TEXT NOT NULL DEFAULT '';
-- The reviewed definition the call was for, so a grant can be checked
-- against the rules as they are when it is made, and pinned to it.
ALTER TABLE approvals ADD COLUMN pin_hash TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE approvals DROP COLUMN pin_hash;
ALTER TABLE approvals DROP COLUMN rule_id;
