# The tamper-evident audit trail

Every tool call and every admin change is recorded before it runs, with its
outcome after
([ADR 0006](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0006-audit.md)).
The everyday view is **Activity → Tool calls** and **Admin changes**; see
[Viewing activity](../basics/activity.md). This page covers how the record
proves it was not rewritten, and how to keep, export and trim it.

Tamper evidence, search, export, retention and erasure arrived in 0.16.0
([ADR 0028](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0028-audit-signing-key-and-checkpoints.md)).

## What is recorded

For each tool call: when, who called and from which client, the server and
tool, the rule's decision, who approved it, the outcome and how long it took.
For each admin change: who changed what, and when.

By default the record holds **no** call arguments and no results. To keep
them, set `record_arguments` or `record_results` with
`PATCH /api/v1/settings/audit`. Recorded arguments can be erased person by
person (below), and are never part of the CSV export.

## How the record is sealed

Every few seconds Privo seals new records into a block. Each block is linked
to the one before it and signed with a key derived from the master key; that
key never touches the database. A changed, deleted or reordered record, or a
forged block, breaks the chain.

## Check it

- On the admin site: **Activity**, under **Integrity**, **Check the record**.
- With the admin API: `GET /api/v1/audit/integrity`.
- On the host: `privo audit verify`. It prints a report and exits 1 on a
  problem:

```sh
$ privo audit verify
{
  "ok": true,
  "blocks": 913,
  "events": 18204,
  "pruned_blocks": 0,
  "unsealed": 2,
  ...
}
```

`unsealed` counts the newest records, not yet in a block.

## Keep proof outside Privo

Copy the signed checkpoints somewhere outside Privo, such as your own
storage or a ticket, on a schedule: `GET /api/v1/audit/checkpoints`, with the
public keys at `GET /api/v1/audit/keys`. Later they prove that nothing before
them was rewritten, even by someone with access to the server.

## Search and export

**Activity** filters by person, server, action, decision and date, loads
more, and exports CSV: `GET /api/v1/audit/export`, at most 10,000 rows. The
export never includes the recorded arguments.

## Retention

Those who manage the audit (owners and admins) set how long events are kept:
**Activity → Integrity → Retention**, **Keep events for** a number of days,
then **Save**. `0` keeps them for ever; otherwise at least 30 days. Whole
sealed blocks are removed within the hour, and what remains still checks.

With the admin API: `PATCH /api/v1/settings/audit` with `retention_days`.

## Erase a person's recorded arguments

Under **Erase a person's recorded arguments**, enter the person and choose
**Erase**. Privo clears the arguments recorded for their calls. Who called
what, and when, stays: the record must stay checkable.

With the admin API: `POST /api/v1/audit/erase`.
