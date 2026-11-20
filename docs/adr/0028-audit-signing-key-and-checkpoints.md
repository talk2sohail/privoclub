# 0028. The audit signing key comes from the master key; checkpoints anyone can check

- Status: Proposed, 2026-11-20 (accept after the F12 review)
- Date: 2026-11-20
- Details: 0006 (sealer, chain, Ed25519 checkpoints), 0013 (key custody)

## Context

ADR 0006 decides that a sealer chains audit events into blocks and signs
each block with Ed25519. The key must be held outside the database, "so
even a database administrator cannot rewrite history undetected". It leaves
open where the key comes from. Privo already holds one secret outside the
database: the master key, from the environment, a file or a key provider
(ADR 0013).

## Decision

1. **Derived, never stored.** Each org's signing key is derived from the
   current master key: HKDF-SHA256, salt `privo audit signing v1`, info the
   org id, giving an Ed25519 seed. The key is never written anywhere. Its id
   is the first 16 hex characters of the SHA-256 of its public key.
2. **Rotation.** Rotating the master key rotates the signing key. Blocks
   carry the key id and the public key they were signed with. A verifier
   accepts a block only when that public key is one it trusts:
   - the current or previous derived key, for `privo audit verify`, which
     reads the master key;
   - the keys published at `GET /api/v1/audit/keys` and kept by the
     customer.
3. **What is chained.** Each event becomes the JSON of a fixed list of
   fields, in a fixed order: org, sequence number, every recorded field,
   and the SHA-256 of its payload. Each block's hash covers the previous
   block's hash, its range of sequence numbers and its events' hashes. The
   signature covers the block's hash.
4. **Checkpoints.** `GET /api/v1/audit/checkpoints` lists signed block
   heads (sequence, range, hash, previous hash, key id, signature) for a
   customer to store outside Privo: a bucket with object lock, or a
   ticket. Comparing them later shows any rewrite.
5. **Erasure and retention keep the chain.**
   - Erasing recorded arguments (GDPR) clears the payload and keeps its
     digest.
   - Retention deletes sealed events older than its limit and keeps every
     block. Verification then starts at the first block whose events
     remain.
   - Unsealed events are never deleted.

## Consequences

- Whoever holds the master key can sign. That is the same trust as
  decrypting every secret, and it is documented (ADR 0013). A key provider
  that never releases the key (a KMS) is a later step, through the same
  interface.
- A database administrator without the master key can delete or change
  rows. Verification then fails, and checkpoints stored outside show where.
- Tamper evidence lags by the seal interval, a few seconds (ADR 0006).
