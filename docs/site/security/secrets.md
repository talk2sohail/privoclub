# Secrets and keys

How Privo keeps the credentials it holds, and the keys that protect them: a
summary of
[ADR 0003](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0003-secrets-and-authentication.md)
and
[ADR 0013](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0013-key-custody.md).
Envelope encryption and hashed tokens have been there from the first release.

## What Privo encrypts

With your organisation's data key, Privo encrypts:

- the header and environment values given to a server, such as a shared API
  key;
- each person's connection to a server: their OAuth grant, or their own API
  key;
- the OAuth client secret entered for a server;
- the arguments of calls waiting for approval, and the arguments the audit
  records when recording is on;
- the signing secret of Privo's own OAuth server, and sign-ins and connections
  in progress.

What Privo never needs to read back, it keeps only as a hash, so not even the
database can give it away:

- personal tokens: only the SHA-256 of their 256-bit random secret;
- AI clients' OAuth tokens: only their signature;
- sessions, setup links and invite links.

## How the encryption works

1. The **master key**, 32 random bytes, is the key-encryption key. It never
   touches the database.
2. Each organisation has a **data key**, stored only wrapped (encrypted) by
   the master key and bound to the organisation.
3. Each credential is encrypted with AES-256-GCM under **its own key**,
   derived from the data key with HKDF. The ciphertext is bound to its
   organisation, credential, owner, server, purpose and versions: copied to
   another row, person or an older version, it fails to open.

An opened data key stays in memory for at most five minutes. A failure to open
never says why, so it tells an attacker nothing.

## The master key

- Make it with `openssl rand -base64 32`.
- Give it as a file, with `PRIVO_MASTER_KEY_FILE`: a mounted secret, for
  example. `PRIVO_MASTER_KEY` also works, but an environment variable stays
  readable in `/proc/<pid>/environ` for the life of the process and shows in
  `docker inspect`. Privo logs a warning when the key arrives that way.
- Once it has read them, Privo removes the key variables from its own
  environment, so the processes it starts never inherit them. The
  configuration file never accepts the key itself, only the path of its file.
- Privo does not start without a key, or with a key that cannot open its
  database, and says which.

The same goes for the other secret settings: prefer
`PRIVO_GITHUB_CLIENT_SECRET_FILE` and `PRIVO_BOOTSTRAP_TOKEN_FILE`.
`privo config show` never prints a secret. Every setting is in the
[configuration reference](../installation/configuration.md);
[Deploy on Coolify](../installation/coolify.md) shows the file mount.

!!! warning "Back up the master key"
    Without the master key, every stored credential is unreadable and Privo
    does not start on that database. Keep a copy in your password manager,
    apart from your data backups: a backup holds nothing in the clear and is
    useless without the key
    ([Backup, restore and upgrades](../installation/operations.md)).

**Planned:** a recovery kit that splits the key into printable shares, any
agreed number of which rebuild it (ADR 0013).

## Changing the master key

A change of master key cannot be completed yet. With the new key in
`PRIVO_MASTER_KEY_FILE` and the old one in `PRIVO_MASTER_KEY_PREVIOUS_FILE`:

- Privo starts, and opens data keys wrapped with either key, so the change
  locks no one out;
- the audit's signing key moves to the new key, which vouches for the old one,
  so earlier blocks keep verifying.

Nothing re-wraps the organisation's data key under the new key yet: the
command that will do it is planned. Until it exists, the old key still opens
everything.

!!! warning
    After a change, keep the old key set as `PRIVO_MASTER_KEY_PREVIOUS_FILE`,
    and keep it backed up. Without it, Privo does not start and the stored
    credentials cannot be opened. Change the key only when you must: today a
    change adds a key to protect and retires none.

### If the master key leaks

A leaked key opens every credential in any copy of the database, earlier or
later, because the data key it wraps does not change. It can also sign audit
blocks: checkpoints you copied outside Privo before the leak still prove what
came before.

1. Rotate the credentials themselves, at their providers. Have each person
   disconnect their accounts, which revokes their grants at the provider, and
   connect again. Replace the shared keys, and have people replace their own
   API keys.
2. Keep the database and its backups out of reach. Re-keying the data, so the
   leaked key opens nothing new, needs the planned rotation command.

## Personal tokens

A personal token looks like `privo_pat_<id>_<secret><checksum>`:

- the id finds the token, and is safe to log;
- only the SHA-256 of the secret, 256 random bits, is stored;
- the checksum lets secret scanners recognise a leaked token.

A token is shown once, when it is made.

## Never logged, never returned

- Through the admin API, secret values are write-only. A server's page shows
  which headers are set, never their values.
- Tests check that no secret reaches the logs, the audit, an error message or
  an API answer, and that the tables for sign-in and connections have no
  column for a plain secret.
- Addresses in diagnostics and error messages lose their query string, user
  name and password, which often carry keys. A server address with a user name
  and password is refused: put keys in headers.
- Removing known secrets from tool results is not built, and would never be a
  security boundary: an encoded or split secret slips through.

## Who holds the keys

On a self-hosted install, you do: the master key lives on your host, and the
Privo project never sees it. **Planned** (ADR 0013):

- for Privo Cloud, a cloud KMS, so the key never leaves it;
- customer-managed keys, so you see every use of your key in your own cloud's
  logs and can cut Privo off;
- keys bound to each person's own token, so a stolen database and every
  Privo key still cannot open their accounts.

Each organisation's data key already records which provider and key wrap it,
so these come as additions, not migrations.
