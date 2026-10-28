# Security review of milestone 1 (gateway core), 2026-10-28

An independent reviewer read the Go code after the six acceptance tests first
passed, and proved three findings with failing tests. All findings below are
fixed; each fix has a test unless noted.

| Severity | Finding | Fix | Test |
| --- | --- | --- | --- |
| High | Stored upstream headers were re-added on cross-host redirects, sending credentials to the redirect target | Upstream HTTP clients are locked to the registered origin; other origins are refused; redirects must stay on the same host and never downgrade https | `TestRedirectDoesNotLeakCredential` |
| High | An SSE upstream could name a foreign host as its message endpoint and receive credentials and tool arguments | Same origin lock | `TestSSEEndpointOnOtherHostIsRefused` |
| High | A slow, stale sync could re-enable a server an admin had just disabled; a nil cancel could hang shutdown | Syncs serialised; entries replaced only by newer rows; cancel set under the lock | `TestStaleSyncCannotReenableDisabledServer` |
| High | Local runner: loader variables (`LD_PRELOAD`, `PATH`...) bypassed the digest pin; children could read the master key from `/proc/<ppid>/environ` | Reserved variables refused at registration and spawn; the local runner now requires `PRIVO_INSECURE_LOCAL_RUNNER=i-understand`; warning when the key comes from an environment variable instead of a file. Full isolation is F13 | `TestLocalChecksDigestAndScrubsEnv`, `TestLocalRunnerNeedsAcknowledgement` |
| Medium | Accepting pins was blanket, so a definition swapped in after review could be accepted | `GET /servers/{alias}/pins` to review; `pins:accept` takes the exact (kind, name, hash) list | `TestPins`, acceptance suite |
| Medium | Upstream URLs (often with API keys in the query) and raw connect errors were shown to members and logged | Targets redacted to scheme, host and path; errors shown only to server admins; logs redacted | `TestRedaction` |
| Medium | Admins could revoke owner tokens | Only owners revoke owner or admin tokens | Not yet covered: control API tests are a follow-up |
| Medium | A list-changed flood spawned unbounded refreshes and unbounded stored definitions | One refresh in flight per server, coalesced; 2,000 items and 64 KiB per definition | Not yet covered |
| Low | Crafted upstream names could collide with another tool's public name | Colliding names are hidden and logged | `TestCollidingPublicNamesAreHidden` |
| Low | Completion audit rows could be lost on a failed flush, a client disconnect, or a send during shutdown | Flush retried with backoff; enqueue ignores caller cancellation; closed writer returns an error instead of panicking | `TestFlushRetriesInsteadOfDropping`, `TestCancelledCallerStillRecorded`, `TestSendAfterCloseDoesNotPanic` |
| Low | Header names barely validated | RFC 9110 token grammar; hop-by-hop, transport and `Mcp-*` headers reserved; values without control characters | Not yet covered |
| Low | WAL and SHM files could be created with default permissions | Owner-only umask; data directory 0700; files pre-created 0600 | `TestOpenMigratesAndSecuresFile` |

Follow-ups fixed in the same pass: wrapped DEKs now bound to their key version;
NAT64, 6to4 and Teredo ranges blocked; trailing JSON refused; audit rows record
the stable token id as well as the name; `privo token create` writes to the
right stream.

Follow-ups still open (tracked in docs/todo.md): control and upstream API
tests; `/version` exposure; runner exec-by-fd (production runner, F13).
