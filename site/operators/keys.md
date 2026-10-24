# Keys and credentials

## What exists

| Secret | Where | Protects | If lost |
|---|---|---|---|
| **Master key** (32 bytes, hex or base64) | Your vault → a compose secret → `PULSE_MASTER_KEY_FILE` | Seals `keys.enc` and the stored webhook secrets; derives the key that names backup chunks | `keys.enc` cannot be opened. Every client needs new credentials and every hook a new secret. |
| `keys.enc` | `/data/pulsed/keys.enc` (and every backup) | The NATS operator, the SYS and APP accounts and their signing keys, internal users, the internal CA, the ACME account key | Restore it from a backup. It is useless without the master key. |
| Client `.creds` | Each tool's host | One client's user JWT and seed | Rotate the client; the old creds are revoked. |
| HTTP tokens, inbound hook secrets, outbound signing secrets | Shown once in the dashboard | One token or hook | Rotate it. |
| **age backup identity** | Offline only | Every backup | Old backups cannot be restored. |
| Cloudflare, S3, Telegram and Discord tokens | Compose secrets (`*_FILE`) | Their own service | Issue new ones at the provider. |

pulsed never shows a seed or a token twice. It keeps users' public keys and revocations, and hashes of HTTP tokens.

## The master key

Generate it once and keep it in your vault:

```sh
openssl rand -hex 32
```

In the prod profile it must come from a file (`PULSE_MASTER_KEY_FILE`); the plain `PULSE_MASTER_KEY` variable is
refused, as are plain variables for every other secret. It is never written to a volume and it is **not** in the
backups. Without it, `pulsed init` and `pulsed serve` refuse to start; with the wrong one, they fail closed instead of
generating new keys.

Keep an offline copy next to the age identity (a password manager entry and a printed or offline medium). For disaster
recovery you need **both**: the master key to open `keys.enc`, and the age identity to decrypt the backup.

## The key store

`pulsed init` creates `keys.enc` on first run and completes it on later runs (it is idempotent): the operator and its
signing key, the SYS and APP accounts with their signing keys, pulsed's internal users, the internal CA and the ACME
account key. The operator JWT is used in strict signing-key mode: accounts and users are signed by signing keys, never
by the identity keys themselves.

Each tool client gets its own scoped signing key in the APP account, which carries that client's permissions. When you
revoke a client, its user key is added to the APP account's revocation list and pulsed pushes the updated account to
NATS.

## Client credentials

- Created on the Clients page or with `pulse clients create`. A new client has no permissions until you add grants
  ([clients and grants](/operators/clients)).
- They expire after 365 days by default; the `client_expiry` alert fires 30 days before.
- **Rotate** issues new creds and revokes the old user key. **Revoke** is permanent.
- Store `.creds` files with mode 0600, readable only by the tool's user.

## Inspecting or moving the key set

```sh
pulsed keys export --yes-i-understand
```

prints the decrypted key set as JSON. pulsed must be stopped (the command takes the database lock to write an audit
record first). Use it only to move to other tooling. To move Pulse to a new server, restore `keys.enc` from a backup
and use the same master key ([backup and restore](/operators/backups#new-server)).
