# Versioning policy

| Interface | Version marker | Where it is defined |
|---|---|---|
| Binary / image | `vMAJOR.MINOR.PATCH[-PRERELEASE]` (SemVer 2.0.0) | git tag, `pulsed version` |
| Public HTTP API | URL prefix `/api/v1` | `api/public.openapi.json` |
| Admin HTTP API | URL prefix `/api/admin/v1` | `api/admin.openapi.json` |
| Envelope | header names in `docs/ENVELOPE.md`, `ce-specversion` 1.0 | `pkg/pulse/envelope`, `sdk/conformance` |
| SQLite schema | migration number (`0001`, …) | `internal/store/migrations/` |
| Key store | magic `PULSEKS1` + version byte | `internal/secretbox` |
| Backup format | magic `NATSARC1` | `docs/BACKUPS.md` |
| Configuration | `PULSE_*` variables | `docs/operator/configuration.md` |

## Releases

Tags `vX.Y.Z` and pre-releases `vX.Y.Z-rc.N`; the release workflow rejects anything else. Image tags drop the `v`
(`ghcr.io/aurimasniekis/pulsed:X.Y.Z`), and `latest` follows final releases only. The deployment repo pins digests.

- **MAJOR**: breaking change to any interface above (removed env var, `/api/v2`, envelope header rename, unreadable key store
  or backup version, a migration that needs manual steps).
- **MINOR**: new features, additive API changes, new env vars, new migrations.
- **PATCH**: fixes without interface changes.

Before 1.0.0, MINOR releases may break interfaces; each break is listed in `CHANGELOG.md` with upgrade steps.
