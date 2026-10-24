# Keys and credentials

## What exists

| Secret | Where | Protects | If lost |
|---|---|---|---|
| Master key (32 bytes, hex or base64) | vault → `PULSE_MASTER_KEY_FILE` | Seals `keys.enc` and the stored webhook secrets; derives the backup chunk-naming key | `keys.enc` cannot be opened: every client must get new credentials. Keep it in the vault and in your offline copy. |
| `keys.enc` | `/data/pulsed/keys.enc` (and every backup) | Operator, SYS and APP accounts with their signing keys, internal users, the internal CA, the ACME account key | Restore it from a backup; it is useless without the master key. |
| Client `.creds` | each tool's host | One client's user JWT and seed | Rotate the client; the old creds are revoked. |
| HTTP tokens, hook secrets | shown once in the dashboard | One token or hook | Rotate. |
| age backup identity | offline only | Every backup | Old backups cannot be restored. Keep two offline copies. |

`pulsed` never shows a seed twice. pulsed keeps the user public key and revocations, not the seed.

## Client credentials

- Created on the Clients page or with `pulse clients create <name> --grant …`. A new client has no permissions until
  you give it grants (least privilege, ADR-0025).
- They expire after 365 days by default. The `client_expiry` alert fires 30 days before expiry.
- **Rotate**: `pulse clients rotate <name>` issues new creds and revokes the old user key. Connected tools are
  disconnected within seconds.
- **Revoke**: `pulse clients revoke <name> --yes` is permanent.

## Inspecting or moving the key set

`pulsed keys export --yes-i-understand` prints the decrypted key set (stop pulsed first; it writes an audit record).
Use it only to move to another tool. To move servers, restore `keys.enc` from a backup
(`pulsed backup restore --files`) and use the same master key.
