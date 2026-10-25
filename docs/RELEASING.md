# Releasing Pulse

The maintainer procedure for publishing a version. Version numbers follow [VERSIONING.md](VERSIONING.md). A release
is cut from `main` by pushing a `vX.Y.Z` tag; the [release workflow](../.github/workflows/release.yaml) runs the
race tests, builds and scans the image, pushes it to Docker Hub and GHCR, and creates a draft GitHub release.

## One-time setup

| What | Where |
|---|---|
| Docker Hub access token with **Read & Write** scope for `aurimasniekis/pulsed` | Repository secret `DOCKERHUB_TOKEN` (`gh secret set DOCKERHUB_TOKEN`). The Docker Hub user is `DOCKERHUB_USER` in the workflow. Without the secret the publish job fails before pushing anything. |
| Docker Hub repository `aurimasniekis/pulsed` | Created on the first push if it does not exist. Set it public and add a short description linking to the documentation. |
| GHCR package `ghcr.io/aurimasniekis/pulsed` | Created on the first push as **private**. Make it public once (package settings → visibility) and link it to the repository, so it can be pulled anonymously. |
| Private vulnerability reporting | Repository settings → Security → "Private vulnerability reporting" ([SECURITY.md](../SECURITY.md) points reporters there). |
| GitHub Pages | Settings → Pages → Source: "GitHub Actions" (the [pages workflow](../.github/workflows/pages.yaml) deploys `site/`). |

## Checklist

1. **CI.** `main` is green on GitHub for the commit to be tagged: the `ci` workflow (Go, security, web, SDKs with
   interop, image smoke) and the `e2e` workflow. `make test` includes the restore drill (`make drill` runs it alone),
   which every release must pass. Run the Docker suites CI does not: `make test-nft`, `make test-acme` and
   `make test-s3`.
2. **Changelog.** Move the `[Unreleased]` entries into `## [X.Y.Z] - YYYY-MM-DD` in [CHANGELOG.md](../CHANGELOG.md),
   list the known limitations that still hold (the release status on
   [site/security/index.md](../site/security/index.md) is the current list) and update the compare links at the
   bottom. The release workflow refuses a tag without its section and uses it as the release notes.
3. **Version references.** Update the release status on [site/security/index.md](../site/security/index.md), the FAQ,
   the "No release yet" note in [site/guide/deploy.md](../site/guide/deploy.md) and the README's deploy section, and any
   install example that names a version (`git grep -nF <previous version> -- README.md site docs`).
4. **Screenshots.** If the dashboard changed visibly, regenerate them with `make screenshots` (fictional data on a
   throwaway stack) and review every image before committing.
5. **Commit** these changes on `main` (`docs: prepare vX.Y.Z release`) and push. Wait for CI.
6. **Tag** the exact commit CI tested and push the tag:

   ```sh
   git tag -a vX.Y.Z -m "Pulse vX.Y.Z"
   git push origin vX.Y.Z
   ```

7. **Watch the release workflow.** It runs `make test`, builds the image and fails on fixable HIGH/CRITICAL Trivy
   findings, then pushes one linux/amd64 + linux/arm64 manifest with an SBOM and provenance to Docker Hub
   (`docker.io/aurimasniekis/pulsed`) and GHCR (`ghcr.io/aurimasniekis/pulsed`), scans the pushed digest, and attests
   it in both registries. The run summary prints the digest: that is what deployments pin. Release candidates
   (`vX.Y.Z-rc.N`) do not move the `latest` tag and are marked as pre-releases.
8. **Publish the GitHub release.** The workflow creates a draft with the changelog section and the image digest as
   notes. Review it and publish it.
9. **Verify what was published** from a clean machine:

   ```sh
   V=X.Y.Z
   gh attestation verify "oci://docker.io/aurimasniekis/pulsed:$V" --repo aurimasniekis/go-pulse
   gh attestation verify "oci://ghcr.io/aurimasniekis/pulsed:$V" --repo aurimasniekis/go-pulse
   docker run --rm "docker.io/aurimasniekis/pulsed:$V" version
   ```

10. **After the release**, add an empty `## [Unreleased]` section to the CHANGELOG if it is missing.

## If something goes wrong

- **The workflow fails before publishing** (tests, Trivy): fix on `main`, delete the tag
  (`git push --delete origin vX.Y.Z && git tag -d vX.Y.Z`) and tag the fixed commit. Nothing was published.
- **A published release is broken:** never move or reuse a published tag or image tag. Publish `vX.Y.(Z+1)` with the
  fix. Re-tagging would invalidate the digests and attestations deployments already pinned.
