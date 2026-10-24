# Releasing Pulse

The maintainer procedure for publishing a version. Version numbers follow [VERSIONING.md](VERSIONING.md). A release
is cut from `main` by pushing a `vX.Y.Z` tag; the [release workflow](../.github/workflows/release.yaml) runs the
race tests, builds and scans the image, and pushes it to GHCR.

## One-time setup

| What | Where |
|---|---|
| GHCR package `ghcr.io/aurimasniekis/pulsed` | Created on the first push as **private**. Make it public once (package settings → visibility) and link it to the repository, so it can be pulled anonymously. |
| Private vulnerability reporting | Repository settings → Security → "Private vulnerability reporting" ([SECURITY.md](../SECURITY.md) points reporters there). |
| GitHub Pages | Settings → Pages → Source: "GitHub Actions" (the [pages workflow](../.github/workflows/pages.yaml) deploys `site/`). |

## Checklist

1. **CI.** `main` is green on GitHub for the commit to be tagged: the `ci` workflow (Go, security, web, SDKs with
   interop, image smoke) and the `e2e` workflow. `make test` includes the restore drill (`make drill` runs it alone),
   a release gate. Run the Docker suites CI does not: `make test-nft`, `make test-acme` and `make test-s3`.
2. **Changelog.** Move the `[Unreleased]` entries into `## [X.Y.Z] - YYYY-MM-DD` in [CHANGELOG.md](../CHANGELOG.md)
   and list the known limitations that still hold (see the [acceptance checklist](PLAN.md)).
3. **Version references.** Update the status notes in `README.md`, [site/security/index.md](../site/security/index.md)
   and the FAQ, and any install example that names a version (`git grep -nF <previous version> -- README.md site docs`).
4. **Screenshots.** If the dashboard changed visibly, regenerate them with `make screenshots` (fictional data on a
   throwaway stack) and review every image before committing.
5. **Commit** these changes on `main` (`docs: prepare vX.Y.Z release`) and push. Wait for CI.
6. **Tag** the exact commit CI tested and push the tag:

   ```sh
   git tag -a vX.Y.Z -m "Pulse vX.Y.Z"
   git push origin vX.Y.Z
   ```

7. **Watch the release workflow.** It runs `make test`, builds the image and fails on fixable HIGH/CRITICAL Trivy
   findings, then pushes one linux/amd64 + linux/arm64 manifest with an SBOM and provenance, scans the pushed digest,
   and attests it. The run summary prints `ghcr.io/aurimasniekis/pulsed@sha256:…`: that digest is what deployments
   pin. Release candidates (`vX.Y.Z-rc.N`) do not move the `latest` tag.
8. **Create the GitHub release** from the tag with the changelog section as notes and the image digest.
9. **Verify what was published** from a clean machine:

   ```sh
   V=X.Y.Z
   gh attestation verify "oci://ghcr.io/aurimasniekis/pulsed:$V" --repo aurimasniekis/go-pulse
   docker run --rm "ghcr.io/aurimasniekis/pulsed:$V" version
   ```

10. **After the release**, add an empty `## [Unreleased]` section to the CHANGELOG if it is missing.

## If something goes wrong

- **The workflow fails before publishing** (tests, Trivy): fix on `main`, delete the tag
  (`git push --delete origin vX.Y.Z && git tag -d vX.Y.Z`) and tag the fixed commit. Nothing was published.
- **A published release is broken:** never move or reuse a published tag or image tag. Publish `vX.Y.(Z+1)` with the
  fix. Re-tagging would invalidate the digests and attestations deployments already pinned.
