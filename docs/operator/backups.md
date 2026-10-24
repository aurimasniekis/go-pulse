# Backups (operations)

Format, layout and the full restore procedures are in [../BACKUPS.md](../BACKUPS.md); configuration in
[configuration.md](configuration.md#backups).

## Daily checks

- The Backups page shows the catalog: status, size, what the run uploaded, and whether every referenced object
  exists ("verified").
- The `backup_stale` alert fires when the last backup failed or none succeeded for 36 h.
- `pulse backup ls` shows the same catalog through the tunnel.
- Metrics: `pulse_backups_total{result}` and `pulse_backup_uploaded_bytes_total`.

## Restore quick reference

| Situation | Do |
|---|---|
| A stream or bucket was deleted or damaged | Backups page → Restore… → paste identity → select it (Replace if it still exists) |
| Same, from a terminal | `pulse backup restore <id> --identity id.txt --streams NAME [--replace]` |
| New server | `pulsed backup restore --files`, then `init`/`up`, then `pulsed backup restore --all` ([../BACKUPS.md](../BACKUPS.md#new-server-disaster-recovery)) |

Rehearse the new-server restore on a scratch VM after major changes. The restore drill test covers the format.
