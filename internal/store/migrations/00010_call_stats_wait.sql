-- +goose Up
-- Review of 2026-11-15, T-10: the time a call was held for a person's
-- approval, kept out of latency. Its own migration because 00009 shipped in
-- 0.10.0 without it.
ALTER TABLE call_stats ADD COLUMN wait_us INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE call_stats DROP COLUMN wait_us;
