# Backups

> Operated from the Backups page and `pulsed backup`.

## What is backed up

Every night at `PULSE_BACKUP_AT` (UTC), and on demand ("Back up now", `pulsed backup run`), pulsed backs up:

| What | How | Restore |
|---|---|---|
| `pulse.db` (clients, grants, tokens, hooks, bans, audit, alerts, rollups…) | Online `VACUUM INTO` snapshot | Offline: `pulsed backup restore --files` |
| `keys.enc` (operator/account keys, internal CA, ACME account; already sealed with the master key) | The file as is | Offline: `pulsed backup restore --files` |
| Every JetStream stream in the APP account: `EVENTS`, user streams, KV (`KV_*`) and object buckets (`OBJ_*`) | JetStream snapshot (NATSARC1), consumers included | Online: Backups page or `pulsed backup restore --all / --streams` |

Restored streams keep their sequence numbers, stored timestamps, headers, payloads and durable consumer positions, so
`DeliverByStartTime` replay is the same as before. The restore drill (`make drill`, `TestRestoreDrill` in
`internal/backup`) proves this, and every release must pass it.

The master key is **not** in the backup. It lives in ansible-vault and is needed to use `keys.enc`. The age identity
is not on the server either: you need both for disaster recovery.

## Format

### NATSARC1 (JetStream's snapshot format, NATS ≥ 2.15)

A snapshot is an S2 stream holding `NATSARC1` followed by entries. Each entry is:

```
uvarint name length · varint timestamp (unix ns) · uvarint sequence · uvarint header size · uvarint payload size
name · header bytes · payload bytes
```

The entries come in this order: `state.json` (the stream state), one `consumers/<name>` entry per consumer
(config and state JSON), the message records (the name is the subject, sequence > 0, ascending), and an all-zero
end-of-backup sentinel. The restore API stores every record with its own sequence and timestamp, and skips gaps.

### How pulsed stores it (deduplicated)

```
<prefix>chunks/<hmac>                 message records of one bucket: S2-compressed, age-encrypted
<prefix>backups/<id>/manifest.age     everything needed to rebuild each stream's archive (encrypted)
<prefix>backups/<id>/pulse.db.age     SQLite snapshot (S2 + age)
<prefix>backups/<id>/keys.enc.age     sealed key set (S2 + age)
<prefix>backups/<id>/refs.json        plaintext: id, time, size, chunk names, object keys, manifest MAC (written last)
```

- **Buckets.** Message records are grouped by fixed windows of 1024 sequence numbers, split again at 16 MiB. Records
  that have not changed serialise to the same bytes, so a full window keeps its bucket from night to night. A nightly
  run uploads only the newest window or two, plus the manifest and the SQLite file. It is still a complete
  point-in-time snapshot.
- **Names.** A chunk's name is `HMAC-SHA256(k, plaintext)`, with `k` derived from the master key (HKDF, "pulse backup
  chunk names v1"). Equal content dedupes, but the storage provider cannot test guesses.
- **Integrity.** age recipients are public: anyone with write access to the bucket could encrypt a forged object to
  them. pulsed therefore checks each chunk's HMAC name and length, each file's SHA-256 (from the manifest), and the
  manifest's MAC (in `refs.json`) before it uses them. A `--replace` restore fetches and checks every chunk *before*
  it deletes the existing stream.
- **Verification.** After each run pulsed lists the bucket and checks that every object the backup references
  exists. The catalog shows the result as "verified".
- **Retention (GFS).** pulsed keeps the newest backup of each of the last 7 days, 4 ISO weeks and 12 months, plus
  always the newest backup. It then deletes chunks that no kept backup references. Leftovers of runs that never wrote
  `refs.json` are deleted once they are a day old.

## Configuration

See [operator/configuration.md](operator/configuration.md#backups). In short:

```sh
pulsed backup keygen > backup-identity.txt   # keep OFFLINE (password manager / offline media)
# the recipient (age1…) is printed on stderr → PULSE_BACKUP_AGE_RECIPIENTS
```

Use versioned (or object-locked) buckets if you can: pulsed's prune deletes objects, so a compromised server with
the S3 key could delete backups.

## Restore

### Same server: a stream or bucket was lost or damaged

1. Backups page → pick a backup → **Restore…**
2. Paste the age identity → **Show contents** (dry run: every stream, its messages and sequence range, and whether it
   exists now).
3. Select streams. Streams that exist are skipped unless **Replace** is on; Replace deletes them first, and you
   type the backup id to confirm. Events published after the backup are then lost.
4. **Restore**. Progress and per-stream results appear under "Last restore".

CLI equivalent (pulsed may be running):

```sh
pulsed backup list --identity id.txt
pulsed backup restore --id <id> --identity id.txt --streams KV_settings --dry-run
pulsed backup restore --id <id> --identity id.txt --streams KV_settings
```

### New server (disaster recovery)

With the same master key (from ansible-vault) and S3 settings:

```sh
# 0. the container runs as uid 65532: give it a readable copy of the identity (delete it at the end)
sudo install -m 0400 -o 65532 backup-identity.txt id.txt
# 1. pulsed stopped, before `pulsed init`: restore pulse.db and keys.enc, so every client's credentials keep working
docker compose run --rm --no-deps -v ./id.txt:/tmp/id.txt:ro pulsed backup restore --id <id> --identity /tmp/id.txt --files
# 2. init re-renders nats.conf from the restored keys; start the stack (pulsed pushes the accounts to NATS)
docker compose up -d
# 3. streams
docker compose run --rm --no-deps -v ./id.txt:/tmp/id.txt:ro pulsed backup restore --id <id> --identity /tmp/id.txt --all
sudo rm id.txt
```

Restoring `EVENTS` takes the stream's full size in disk and time. Rollups resume from their checkpoints, which are in
the restored `pulse.db`, because sequence numbers are preserved.

## Limits

- Messages deleted individually inside a sequence window change that window's bucket (re-uploaded). Purges and
  discard-old only move the first sequence forward.
- An alert (`backup_stale`) fires when the last backup failed or none succeeded for 36 h.
- Backup and restore hold one lock: a backup, prune and restore never run at the same time.
