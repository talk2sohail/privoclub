# 0013. Key custody and shared responsibility

- Status: Accepted, 2026-10-28
- Date: 2026-10-28
- Related: 0003 secrets, 0009 threat model, 0010 operations

## Context

ADR 0003 encrypts every credential under a per-org data key (DEK), wrapped by
one key-encryption key (KEK) that Privo holds. On Privo Cloud that makes Privo
the single custodian: whoever obtains the KEK and a database copy can decrypt
every customer's credentials. The owner asked how to share that responsibility
so a Privo compromise does not expose customer keys.

Two attacker positions matter, and no scheme defends both equally:

1. **Data at rest stolen** (database dump, backup, leaked master key file).
2. **Live gateway compromised** (attacker runs code inside Privo while it
   serves calls). The gateway must decrypt credentials to inject them, so a
   live attacker can see what passes through while they are in. Key custody
   limits how much, for how long, and whether the customer can see and stop it.

## Options considered

| Option | Stops (1) | Limits (2) | Effort | Developer experience | Operating cost |
| --- | --- | --- | --- | --- | --- |
| A. Privo KEK in a file or env (today) | No, if the key file leaks with the data | No | Done | Simple | Low |
| B. Privo KEK inside a cloud KMS/HSM (key never leaves it) | Yes: a dump is useless without KMS access | Partly: every unwrap is logged; access can be cut centrally | Small | Simple: one `KeyProvider` | KMS calls, cached |
| C. **Customer-managed keys (BYOK)**: each org's DEK wrapped by a key in the customer's own KMS | Yes | Yes: the customer sees every unwrap in their own logs and can revoke at once (kill switch) | Medium | Moderate: customer grants one key permission; Privo stores a key reference | Per-org KMS calls, cached |
| D. **Token-bound user keys**: each person's credentials are wrapped by a key derived from their own Privo token secret, which Privo stores only hashed | Yes, even with KEK + database | Partly: only users active during the compromise are exposed | Medium-high | Invisible to users; losing all tokens means reconnecting accounts | Re-wrap on token rotation |
| E. Split master key (Shamir, M of N owners) for recovery and rotation only | Protects the backup of the key | No | Small (vetted library) | Ceremony for recovery only, not for restarts | Low |
| F. Split key needed at runtime (customer-run key agent) | Yes | Yes | High | Poor: Privo stops when the agent is down | High |
| G. Confidential computing (Nitro Enclaves, SEV) | Yes | Strongly | High | Specialised | High |

## Decision

Adopt a **tiered custody model**, built on one change made now: keys are
resolved **per organisation through a key reference**, not one global key.

1. **Now (v1):** `org_keys` stores `(org_id, version, provider, key_ref,
   wrapped_dek)`. The vault asks a provider registry for the KEK named by
   `provider` and `key_ref`. Self-hosted installs use the local master key
   provider (option A, customer-held by definition) and ship a **Shamir
   recovery kit** (option E): at install the owner can split the master key
   into M-of-N printable shares for backup. Restarts never need the shares.
2. **Privo Cloud launch:** the default provider is a cloud KMS (option B).
   The KEK never exists outside the KMS; unwrapped DEKs are cached in memory
   for minutes only; one KMS key per cell.
3. **Business and Enterprise plans:** **customer-managed keys** (option C)
   for AWS KMS first, then GCP and Azure. Revoking the key in their account
   cuts Privo off within the cache lifetime; every unwrap appears in their
   CloudTrail or equivalent. This is the industry-standard answer buyers ask
   for in security reviews.
4. **Differentiator, after v1:** **token-bound user keys** (option D) for
   per-user credentials. A stolen database plus every Privo key still cannot
   decrypt a user's GitHub or Google tokens, because part of the key exists
   only in that user's own Privo token. Shared org credentials stay under
   options B or C. Prototype before committing.
5. Not adopted: runtime split keys (F) for availability reasons; confidential
   computing (G) revisited for regulated customers later.

## Consequences

- The per-org key reference costs one column set now and makes options B, C
  and D additions, not migrations.
- Option C requires per-org KMS calls; a warm DEK cache (minutes, bounded) and
  clear errors when a customer revokes their key keep it practical.
- Option D adds re-wrapping on token rotation and a "reconnect your accounts"
  path when every token of a user is revoked.
- Live-gateway compromise is limited, not prevented, by every option except
  G. Short DEK cache lifetimes, per-process isolation of the runner (0007) and
  audited unwraps keep exposure small and visible.

## As built (0.20.0, 2026-11-26)

For operators: [Secrets and keys](https://docs.privo.club/security/secrets/#who-holds-the-keys).

Point 1 is built, except the recovery kit: `org_keys` records each data
key's provider and key reference, and the local master key is the only
provider. Not built: the Shamir recovery kit, a cloud KMS, customer-managed
keys, token-bound user keys, and the KEK rotation command (see ADR 0003, "As
built").
