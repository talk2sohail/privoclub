# Security policy

Privo holds credentials, decides which AI tool calls may run and keeps an
audit trail, so we take reports seriously and thank everyone who sends one.

## Reporting a vulnerability

**Do not open a public issue.** Report it privately through GitHub:
**Security → Report a vulnerability** on this repository (private
vulnerability reporting). Only the maintainers see it.

Please include what you found, the version (`privo version` or `/version`),
how to reproduce it, and what an attacker gains. A proof of concept against
your own install is welcome; please do not test against anyone else's,
including privo.club.

What happens next:

1. We acknowledge the report and confirm whether we can reproduce it.
2. We fix it in a private fork, with a test that fails without the fix.
3. We release the fix, publish a GitHub security advisory (with a CVE where
   it applies) and credit you, unless you prefer not to be named.

Please give us a reasonable time to release a fix before you publish.

## Supported versions

Privo is before 1.0. Fixes go into the current minor release; the previous
minor release gets security fixes while it is in the supported window
(see [ADR 0010](docs/adr/0010-operations.md), Upgrades).

## What is in scope

The gateway (`cmd/privo`, `internal/`), the admin app (`web/`), the container
image and the documentation's security advice. The threat model is
[ADR 0009](docs/adr/0009-threat-model.md); independent reviews are in
[`docs/reviews/`](docs/reviews/), with their open findings in
[`docs/todo.md`](docs/todo.md).

Out of scope: the upstream MCP servers Privo connects to, and findings that
need an administrator to act against their own install (an owner can already
change every rule).
