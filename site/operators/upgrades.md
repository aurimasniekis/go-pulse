# Upgrades

Pulse follows semantic versioning; before 1.0.0, a minor release may change interfaces, and each break is listed in the
[changelog](https://github.com/aurimasniekis/go-pulse/blob/main/CHANGELOG.md) with upgrade steps. What counts as an
interface is in [VERSIONING.md](https://github.com/aurimasniekis/go-pulse/blob/main/docs/VERSIONING.md).

Releases are tagged `vX.Y.Z` and publish the image `docker.io/aurimasniekis/pulsed:X.Y.Z`, mirrored to
`ghcr.io/aurimasniekis/pulsed:X.Y.Z` with the same digest, for linux/amd64 and linux/arm64, with an SBOM and build
provenance. The [GitHub releases](https://github.com/aurimasniekis/go-pulse/releases) list each image digest.

## Upgrading pulsed

1. Read the release notes. Check [configuration](/reference/configuration) for new or changed `PULSE_*` variables.
2. Take a backup: Backups page → **Back up now**, and wait until it shows **verified**.
3. Verify the new image and pin its digest in your deployment repository:

   ```sh
   gh attestation verify oci://docker.io/aurimasniekis/pulsed:X.Y.Z --repo aurimasniekis/go-pulse
   docker buildx imagetools inspect docker.io/aurimasniekis/pulsed:X.Y.Z   # Digest: sha256:…
   ```

   Use `docker.io/aurimasniekis/pulsed@sha256:…` (or the same digest on `ghcr.io`) in the compose file, never a moving
   tag.
4. Deploy: `docker compose up -d`. `init` runs again (idempotent; it also renews the internal NATS leaf), and pulsed
   applies SQLite migrations at start.
5. Check `curl -fsS 127.0.0.1:9464/readyz`, the System page and the Alerts page.

## Rolling back

Migrations only move forward. To roll back past a migration, stop pulsed, restore `pulse.db` from the backup taken
before the upgrade (`pulsed backup restore --files`, see [backup and restore](/operators/backups)), then start the old
image. Events in NATS are not affected by a pulsed rollback.

## Upgrading NATS

The NATS image is pinned by digest in the compose file. Patch releases are drop-in. For a minor release, take a backup
first and read the NATS release notes. Keep the NATS image and the `nats-server` version in pulsed's `go.mod` on the
same version: the restore drill (`make drill`) tests the backup format against the latter.
