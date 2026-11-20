-- +goose Up
-- ADR 0006 and 0025: the sealer gives each event a place in the org's chain
-- (chain_seq, NULL until sealed) and records the SHA-256 of its payload, so
-- a payload can be erased without breaking the chain. Blocks hold the chain.
ALTER TABLE audit_events ADD COLUMN chain_seq INTEGER;
ALTER TABLE audit_events ADD COLUMN payload_sha256 TEXT NOT NULL DEFAULT '';
CREATE INDEX audit_events_org_chain ON audit_events (org_id, chain_seq);

CREATE TABLE audit_blocks (
    org_id     TEXT NOT NULL REFERENCES orgs(id),
    seq        INTEGER NOT NULL,   -- block number, from 1
    first_seq  INTEGER NOT NULL,   -- events' chain_seq range, inclusive
    last_seq   INTEGER NOT NULL,
    prev_hash  TEXT NOT NULL,      -- hex; '' for the first block
    hash       TEXT NOT NULL,      -- hex SHA-256
    key_id     TEXT NOT NULL,
    public_key TEXT NOT NULL,      -- base64 Ed25519 public key
    signature  TEXT NOT NULL,      -- base64 Ed25519 signature of the hash
    sealed_at  TEXT NOT NULL,
    PRIMARY KEY (org_id, seq)
);

-- +goose Down
DROP TABLE audit_blocks;
DROP INDEX audit_events_org_chain;
ALTER TABLE audit_events DROP COLUMN payload_sha256;
ALTER TABLE audit_events DROP COLUMN chain_seq;
