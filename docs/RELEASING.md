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
   which every release must pass. CI does not run the Docker-backed suites, so run them yourself before tagging:
   `make test-nft`, `make test-acme` and `make test-s3`. Put their results in the release-prep PR.
2. **Changelog.** Move the `[Unreleased]` entries into `## [X.Y.Z] - YYYY-MM-DD` in [CHANGELOG.md](../CHANGELOG.md),
   list the known limitations that still hold (the release status on
   [site/security/index.md](../site/security/index.md) is the current list) and update the compare links at the
   bottom. The release workflow refuses a tag without its section and uses it as the release notes.
3. **Version references.** Update the release status on [site/security/index.md](../site/security/index.md), the FAQ,
   the README's deploy section, and any install example that names a version
   (`git grep -nF <previous version> -- README.md site docs`).
4. **Screenshots.** If the dashboard changed visibly, regenerate them with `make screenshots` (fictional data on a
   throwaway stack) and review every image before committing.
5. **Vulnerability gate.** Run it on the release-prep commit from step 6, before opening the PR. CI's `govulncheck`
   reports only reachable vulnerabilities, so it can pass while Trivy flags a vulnerable package in the shipped binaries.
   The release workflow's Trivy scan decides whether a tag is published, and this gap has already cost two tags. The
   workflow publishes linux/amd64 and linux/arm64, so scan both. Build each image with the Dockerfile's default build
   arguments, save it, and scan it with the Trivy version pinned as `TRIVY_VERSION` in
   [release.yaml](../.github/workflows/release.yaml) (the Docker tag drops the leading `v`). A plain
   `docker buildx build` covers only the host architecture; the loop passes `--platform` for each one. The Dockerfile
   cross-compiles from `$BUILDPLATFORM`, so the arm64 build does not run under emulation.

   ```sh
   TRIVY=$(sed -n 's/^  TRIVY_VERSION: v//p' .github/workflows/release.yaml)
   TMP=$(mktemp -d)
   RC=0
   for ARCH in amd64 arm64; do
     docker buildx build --platform "linux/$ARCH" --load -t "pulsed:trivy-gate-$ARCH" . || RC=1
     docker save -o "$TMP/pulsed-$ARCH.tar" "pulsed:trivy-gate-$ARCH" || RC=1
     docker run --rm -i --entrypoint sh "aquasec/trivy:$TRIVY" -c \
       'cat > /tmp/image.tar && trivy image --input /tmp/image.tar --scanners vuln --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1' \
       < "$TMP/pulsed-$ARCH.tar" || RC=1
   done
   rm -rf "$TMP"; echo "trivy gate exit: $RC"; [ "$RC" -eq 0 ]
   ```

   The image tar is streamed into the Trivy container on stdin rather than bind-mounted, because a bind mount fails
   silently when the Docker daemon cannot see the client's filesystem (a remote `DOCKER_HOST` or a private `/tmp`).
   Every failure, including the build and save steps, sets `RC=1`, and the last line exits non-zero when any step failed.

   The gate passes only when the snippet prints `trivy gate exit: 0`, and each arch's scan reports 0 vulnerabilities.
   Put that line and the per-arch summary tables in the release-prep PR (step 6). If the gate fails, fix the dependency
   or base image on the release-prep branch, commit the fix, and repeat this step.
6. **Release-prep PR.** Commit steps 2–4 on a release-prep branch (`release/vX.Y.Z`), not on `main`. Run step 5 on that
   commit, then open a PR into `main` (`docs: prepare vX.Y.Z release`). Its description includes the step 1 Docker-suite
   results and the step 5 Trivy result. Merge it, then wait for CI on `main` to be green on the merge commit.
7. **Tag** that merge commit, the one CI just tested, and push the tag. If anything else landed on `main` after the
   scan, repeat step 5 on the merge commit first:

   ```sh
   git tag -a vX.Y.Z -m "Pulse vX.Y.Z"
   git push origin vX.Y.Z
   ```

8. **Watch the release workflow.** It runs `make test`, builds the image and fails on fixable HIGH/CRITICAL Trivy
   findings, then pushes one linux/amd64 + linux/arm64 manifest with an SBOM and provenance to Docker Hub
   (`docker.io/aurimasniekis/pulsed`) and GHCR (`ghcr.io/aurimasniekis/pulsed`), scans the pushed digest, and attests
   it in both registries. The run summary prints the digest: that is what deployments pin. Release candidates
   (`vX.Y.Z-rc.N`) do not move the `latest` tag and are marked as pre-releases.
9. **Publish the GitHub release.** The workflow creates a draft with the changelog section and the image digest as
   notes. Review it and publish it.
10. **Verify what was published** from a clean machine:

    ```sh
    V=X.Y.Z
    gh attestation verify "oci://docker.io/aurimasniekis/pulsed:$V" --repo aurimasniekis/go-pulse
    gh attestation verify "oci://ghcr.io/aurimasniekis/pulsed:$V" --repo aurimasniekis/go-pulse
    docker run --rm "docker.io/aurimasniekis/pulsed:$V" version
    ```

11. **After the release**, add an empty `## [Unreleased]` section to the CHANGELOG if it is missing.

## If something goes wrong

- **The workflow fails before publishing** (tests, Trivy): fix on `main`, delete the tag
  (`git push --delete origin vX.Y.Z && git tag -d vX.Y.Z`) and tag the fixed commit. Nothing was published.
- **A published release is broken:** never move or reuse a published tag or image tag. Publish `vX.Y.(Z+1)` with the
  fix. Re-tagging would invalidate the digests and attestations deployments already pinned.
