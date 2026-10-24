# Upgrades

See [../VERSIONING.md](../VERSIONING.md) for what counts as breaking. Before 1.0.0, read `CHANGELOG.md` for every minor
release.

## pulsed

1. Read the release notes; check for new `PULSE_*` variables in [configuration.md](configuration.md).
2. Take a backup: Backups page → "Back up now", and wait for "verified".
3. Pin the new image digest in the deployment repository and deploy (`docker compose up -d`). `init` runs again
   (idempotent) and pulsed applies SQLite migrations at start.
4. Check `/readyz`, the System page and the Alerts page.

Migrations only move forward. To roll back past one, restore `pulse.db` from the backup taken before the upgrade
(`pulsed backup restore --files` with pulsed stopped), then start the old image.

## NATS

The NATS image is pinned in the compose file. Patch releases are drop-in. For minor releases, take a backup first and
read the NATS release notes; the restore drill (`make drill`) runs against the NATS version in `go.mod`, so keep both
on the same version.
