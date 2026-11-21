-- +goose Up
-- The member who asked (F05 R-04): self-approval is checked by user id, not
-- only by a person name whose case may differ. '' for a token of nobody.
ALTER TABLE approvals ADD COLUMN requester_user_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE approvals DROP COLUMN requester_user_id;
