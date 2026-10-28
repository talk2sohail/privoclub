# F24 bootstrap token: self-review, 2026-10-29

**This was written by the session that built the change. It is not the
independent review that `CLAUDE.md` requires for changes touching auth and
secrets, and it does not replace it.** It records what was checked so the
independent reviewer can start from it and look for what it missed.

Scope: `PRIVO_BOOTSTRAP_TOKEN(_FILE)`, `privo token generate`, the install in
`store.InstallBootstrapToken` / `control.InstallBootstrapToken` / `cmd/privo`,
the `config.Secret` type, and the static root page.

| Question | Answer | Evidence |
| --- | --- | --- |
| Can the secret reach a log, an error, or an API response? | Not by any path found. The log carries the token id only; config errors name the variable and never the value; `config.Secret` prints `[redacted]` under `%v`, `%+v`, `%#v`, `%s`, slog and JSON; the API never returns it | `TestBootstrapTokenConfig`, `TestInstallBootstrapAtStartup`, `TestServeRejectsMalformedBootstrapToken`, acceptance log scan |
| Is it stored in plain text? | No. Only SHA-256 of the secret, the same as every other token | `TestBootstrapToken/only the hash...`, `assertNotOnDisk` in the acceptance test |
| Can it install twice, or come back after revocation or expiry? | No. The `bootstrap.consumed` marker is written in the same transaction as the token and never cleared | `TestInstallBootstrapToken` (second, revoked, restart), acceptance restart check |
| Can two processes both install it? | No. The check, the insert, the marker and the audit row run in one `BEGIN IMMEDIATE` transaction (`_txlock=immediate`), and the writer lease allows one gateway | Code reading; the lease has its own test |
| Can a failure leave a token without an audit record? | No. A failed audit insert rolls the token and marker back | `TestInstallBootstrapToken/a failed audit write...` |
| Does it widen what a token can do? | No. It is an owner **control** token: admin API only, refused on `/mcp` (ADR 0014). Lifetime is 24 hours, and the store refuses anything over 7 days | `TestBootstrapToken/it is a control token...`, `.../it expires...` |
| Does an administered org ignore it? | Yes, including an org whose only control token was revoked | `TestInstallBootstrapToken` (two cases), acceptance `...IgnoredWhenAlreadyAdministered` |
| Is it cleared from the environment? | Only from the process's own environment, so child processes never inherit it (like the master key). `os.Unsetenv` does not rewrite `/proc/<pid>/environ`, and Coolify and `docker inspect` still show the variable, until it is deleted | Code reading; documented in the deploy guide |
| New unauthenticated surface? | Only `GET /`: static bytes, no request data read, no version or tenant detail, hash-pinned CSP, `nosniff`, no framing | `TestRootPage` |

## Residual risk and things to look at

1. **Environment exposure.** Anyone who can read the Coolify environment can read the token while it is set. They can already read `PRIVO_MASTER_KEY`, so this adds no new class of reader, but it does add a credential that works against the admin API for up to 24 hours. Mitigation is procedural (delete the variable). A reviewer may prefer `PRIVO_BOOTSTRAP_TOKEN_FILE` as the documented path.
2. **First writer wins.** Whoever can set the variable before the first boot gets the first owner token. That is the same party who controls the deployment. It would matter in a setup where a less trusted person can edit variables but not read the master key.
3. **`privo token create` is not audited.** Tokens made from the command line leave no audit row, unlike tokens made through the API and the bootstrap install. Found while reading; tracked in F24 and not changed here.
4. **No rate limit on the admin API.** Token verification is constant time over a 256-bit secret, so guessing is not practical, but there is no limiter in front of it yet. Not new to this change.
5. **Revocation lag.** A revoked bootstrap token still works for up to 2 seconds (the verification cache, ADR 0003). The acceptance test allows for it.
6. **`/` is cacheable by Cloudflare** (`Cache-Control: no-cache` asks it to revalidate). The page holds nothing sensitive.

## Not covered

- The Coolify webhook and watch paths (no code; configuration only, not yet done).
- Behaviour under a real Coolify deploy: the 24-hour window, env handling by Coolify, and Traefik in front of `/`.

## Addendum: cache hints on list and read results (same day)

Self-review again; the request pipeline is touched, so the independent review should include it.

- **Change:** list results now set `cacheScope: "private"` and a 5-second `ttlMs`; `resources/read` forces `private` and ttl 0, replacing whatever the upstream sent.
- **Security effect:** it tightens things. Before, `resources/read` forwarded an upstream's `public` scope, which tells clients and intermediaries that per-person content may be shared. Lists went out with an empty scope, which strict clients reject.
- **Behaviour risk:** a client may reuse a list for 5 seconds, which equals the revocation bound in ADR 0003 and adds no new window.
- **Test:** `TestListAndReadResultsCarryValidCacheHints` reads the raw JSON, including the `resources/read` override.

## Addendum: `resultType` on tools/call, prompts/get and resources/read (same day)

Self-review again; this wraps every result of the three call methods, so the independent review should include it.

- **Found by:** Claude Code calling DeepWiki. From 2026-07-28 these results must carry `resultType`, and the SDK sets it only inside its own handlers, which the gateway bypasses by answering from middleware. A result therefore went out with whatever the upstream sent: nothing from a server older than 2026-07-28, and nothing from the gateway's own refusals and "not available" results. A first diagnosis said no tool call could work for such a client; that was too broad, since an upstream on 2026-07-28 sends the field itself and the relay passed it through. The acceptance fixture is such an upstream, which is why nothing caught this.
- **Change:** `typedResult` wraps those three results and writes `resultType: "complete"` for clients on 2026-07-28 or later, and removes the field for older clients, so a result looks the same whichever server produced it. The client's version is read the way the SDK reads it (`InitializeParams`, which for stateless requests comes from `_meta` or the protocol header).
- **Fails closed:** a nil result, or one marked `input_required`, becomes an error instead of being labelled `complete`. Upstream input requests were already refused by the SDK client (no elicitation or sampling handlers); this keeps it so if that changes.
- **Security effect:** none intended. Policy, approval and audit run before the result exists and are untouched. The wrapper only rewrites one top-level key of the result's JSON.
- **Behaviour risk:** the wrapper re-encodes the result (marshal, then a pass over the object's keys), a second copy of each result in memory. The gateway puts no cap on the size of an upstream's result (found while checking this; it predates the change), so the extra copy grows with whatever the upstream sends. A size limit on relayed results belongs in the independent review's scope. It also depends on SDK v1.8.0 encoding: the upgrade test is `TestEveryResultCarriesResultTypeOnNewProtocol`, which fails if the SDK starts setting the field itself and the two disagree.
- **Tests:** unit tests for field preservation, override of an upstream's value, unknown version and the fail-closed cases; the acceptance test sends 2026-07-28 requests as raw JSON (with the `Mcp-Method` and `Mcp-Name` headers) to a 2026-07-28 upstream and to a `legacy` one, for every list and call method, a not-found result and a policy refusal, and checks legacy clients get no field. The cache-hint test now runs at both versions too.

