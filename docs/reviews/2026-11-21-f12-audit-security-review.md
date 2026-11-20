# Security review: F12 audit chain (2026-11-21)

Scope: the F12 audit trail (ADR 0028, numbered 0025 when reviewed). This
covers the sealer, block signing, verification, retention, erasure, search,
export and the `privo audit verify` command. A separate read-only review pass
was run, as CLAUDE.md requires for changes that touch the request pipeline.
The fixes were made while porting onto main (0.16.0).

The question: can someone with write access to the database, but without the
master key, change the history while verification still reports intact?

## Findings

1. **Medium: unsigned deletion passed as retention.** Verification accepted
   blocks with no events as "pruned" whenever no earlier event remained. So
   deleting the oldest events, or every event, passed, even with retention
   off.
   **Fixed:** every deletion by retention is a prune record (`audit_prunes`),
   signed like a block. Events may be missing only up to the latest signed
   record, and records must be in order and signed by a trusted key.
2. **Medium: events outside every block.** A row with a `chain_seq` past the
   last block, or below 1, was never checked by any block. Search and exports
   still showed it.
   **Fixed:** verification fails when any sealed event claims a place outside
   the blocks.
3. **Medium: backdated events.** A row inserted now but dated months back was
   sealed into the next block. Signed and verified, it looked like old
   history.
   **Fixed:** each block's seal time is part of its signed hash. An event
   dated more than 5 minutes before the previous block was sealed fails
   verification. Every event is written as it happens; a completion waits in
   a queue for seconds at most.

## Notes, also fixed

- **Erasure of an unsealed event.** The sealer recomputed the digest of the
  empty payload. Now the digest of what was there is kept.
- **Master key rotation.** Blocks signed with the old key would stop
  verifying once that key was removed. During a rotation the new key now
  endorses the old one (`audit_keys`).
- **Retention floor.** Retention keeps at least 30 days, and the API refuses
  1 to 29 days.
- **What only outside checkpoints show.** Two things are now stated in
  ADR 0028:
  - the newest blocks dropped whole, with their events;
  - a rewrite by whoever holds the master key.

## While porting onto main

- Main records the member of a call as `audit_events.user_id`. The chain now
  covers that field.
- Erasure removes the recorded arguments of every call that ran as the
  member. This includes calls from AI clients that signed in with OAuth,
  whose actor is not one of the member's tokens.
  Test: `TestErasureCoversTheMembersOAuthClients`.
