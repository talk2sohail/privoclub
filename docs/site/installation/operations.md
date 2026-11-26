# Backup, restore and upgrades

See [ADR 0010](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0010-operations.md)
for the decisions behind this page. The examples run the binary inside the
container with `docker exec <container> /privo ...`; on a host with the binary,
run `privo ...`.

## Backups

A backup is a consistent copy of the database (`privo.db`) taken while the
gateway runs, with no downtime. It holds token hashes, policy, audit and the
wrapped keys and credentials, and nothing in the clear. It does **not** hold
the master key, and it is useless without it: keep the key in your password
manager. Restoring with another key is refused at start with a message that
names the master key.

```sh
privo backup [--to FILE|DIR] [--keep N]    # consistent copy of the live database; no downtime
privo restore --from FILE [--force]        # into an empty data directory; --force keeps the old files aside
```

**On demand** (on Coolify this needs SSH to the server: the image has no
shell, so Coolify's terminal and Coolify's scheduled tasks, which run a
command in a shell inside the container, cannot do this):

```sh
docker exec <container> /privo backup            # into /data/backups/
docker exec <container> /privo backup --keep 7   # and keep only the newest 7
```

**On a schedule**, built in, off by default. Set these and restart:

| Variable | Default | Meaning |
| --- | --- | --- |
| `PRIVO_BACKUP_INTERVAL` | unset (off) | A snapshot every interval (`24h`, `6h`, at least `1s`). At start the gateway waits out what is left of the interval since the newest snapshot, or takes one at once if there is none |
| `PRIVO_BACKUP_KEEP` | `7` | How many of the newest snapshots to keep (1 to 1000); only files named `privo-<UTC time>.db` are ever deleted |
| `PRIVO_BACKUP_DIR` | `<data dir>/backups` | Where snapshots go |

Each backup is an admin audit event (`backup.create`, actor `system` or `cli`;
outcome `error` if it failed). A failed scheduled backup is logged, retried
within at most 5 minutes, and never stops the gateway.

!!! warning "An off-host copy is your job for now"
    The snapshots are on the same volume as the database, so they protect
    against mistakes and corruption, not against losing the server. Copy
    `/data/backups/` somewhere else on a schedule of your own (Coolify's S3
    backups of the volume, `rsync`, `restic`).

## Restore

Stop the gateway first; a restore refuses while one is running.

1. Stop the application (Coolify → Stop).
2. On the server, with the application's data volume (`docker volume ls`) and
   its image (`docker ps -a` shows its name), run the binary once as the
   image's user:
   `docker run --rm --user 65532:65532 -v <data volume>:/data -e PRIVO_DATA_DIR=/data <image> restore --from /data/backups/privo-<time>.db --force`
3. The old `privo.db`, `-wal` and `-shm` are kept beside it as
   `*.replaced-<time>`; nothing is deleted. Delete them yourself when you are
   sure.
4. Start the application. The gateway must run with the master key the backup
   was made with. With another key it exits at start and says that the master
   key cannot open the database.

The restore is tested end to end with the real binary (`test/acceptance`:
`TestBackupAndRestoreDrill`, `TestScheduledBackupsKeepTheNewestAndRestore`,
`TestRestoreWithTheWrongKeyRefusesToStart`). Do one drill into a scratch
directory (`--force` is not needed there) before you rely on it.

## Upgrades

A migration runs by itself on the first start of a new image. **Take a backup
before every upgrade with a migration**: restoring it is the way back. The
notes below say, for each version that needs it, what changes and how to go
back.

### 0.10.1: review fixes

Migration 00010 adds one column to the call statistics (the time a call waited
for an approval). Nothing to set. Going back to 0.10.0 is the older image; it
ignores the column.

### 0.10.0: activity statistics and metrics

Migration 00009 adds two tables (call statistics and the diagnostic log).
Nothing to set: statistics are kept 30 days by default
(`PRIVO_TELEMETRY_RETENTION_DAYS`, `0` turns them off) and pruned hourly.
Prometheus metrics are off ([Deploy on Coolify](coolify.md#metrics)). Going
back to 0.9.x is the older image; it ignores the two new tables.

### 0.9.0: each person's own API key; OAuth settings by hand

No migration and no new setting. Servers that take each person's own API key,
and OAuth servers that publish no sign-in metadata, can now be added
([Connecting your accounts](../basics/connections.md)). Both need GitHub
sign-in and `PRIVO_PUBLIC_URL`, like the other per-person servers. Going back
to 0.8.x is the older image; servers added with a personal key do not work
there (it does not know them) until they are deleted.

### 0.8.0: AI clients sign in with OAuth

Migration `00008` adds four tables; it also drops three columns of
`oauth_states` that 0.7.1 stopped writing and clears that table, whose rows
live ten minutes. **Nothing changes until you switch it on**
([AI clients that sign in with OAuth](../advanced/oauth-clients.md)).

Have the authorization server reviewed for security before switching it on in
production. Going back: switch it off, or run the 0.7.1 or 0.7.2 image, which
starts on the migrated database and leaves the new tables alone.

!!! warning
    Do not go back to 0.7.0 or older once 0.8.0 has run: their sign-in
    writes the columns the migration drops, so signing in would fail. Restore
    the backup instead.

### 0.7.1: sign-in no longer writes before GitHub answers

No migration and no new setting. A sign-in, setup link or invite that someone
started before the deploy and had not finished must be started again (the
cookie it travels in changed); sessions are not affected. Going back to 0.7.0
needs nothing but the older image. What changed and why:
[ADR 0016](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0016-admin-sign-in-with-github.md),
amendment 1.

### 0.7.0: tokens belong to members

Migration `00007_token_owner.sql` adds two columns (it has a down step, and
restoring the backup is the way back) and closes a hole: an owner or admin
could issue a token that named a member and use that member's own accounts
([ADR 0021](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0021-tokens-belong-to-members.md)).

!!! warning "Breaking"
    A gateway token made before 0.7.0 belongs to nobody. It keeps working on
    servers that everyone reaches with a shared credential. On a server where
    each person uses their own account it is answered with "this token does
    not belong to a member" and the address of the Tokens page; servers given
    to a team disappear from its list. Each person signs in, makes a new
    token on **Tokens** and puts it in their AI client; the old one is listed
    there as "not tied to your account" and can be revoked. Tokens issued
    with `POST /api/v1/tokens` or `privo token create` are for scripts from
    now on.

### 0.6.0: teams

Migration `00006_teams.sql` adds teams
([ADR 0020](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0020-teams-and-server-catalog.md)).
Every server that exists keeps being reachable by everyone, so nothing changes
for AI clients until an admin gives a server to a team. The migration has a
down step, and restoring the backup is the way back. Members now land on
**Start** instead of **Tokens**.
