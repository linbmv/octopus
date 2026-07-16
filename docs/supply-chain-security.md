# Supply-chain security gates

This project treats source, release archives, SBOMs, and container images as
separate supply-chain subjects. A tag is publishable only after the source gate,
the MySQL/PostgreSQL database gate, and all exact-digest Debian/Alpine,
amd64/arm64 image gates succeed.

## Pinned tooling and inputs

The workflows use immutable full commit SHAs for every GitHub Action. Tool
versions are explicit rather than floating:

- Gitleaks 8.30.1
- Syft 1.46.0
- Trivy 0.70.0
- Cosign 3.0.6
- golangci-lint 2.12.2, Staticcheck 2026.1, deadcode 0.48.0,
  actionlint 1.7.12, and govulncheck 1.6.0
- Go 1.26.5, Node.js 24, and pnpm 9.15.9

The Alpine 3.22 and Distroless Debian 12 debug-nonroot multi-platform base
images are pinned by OCI index digest in their Dockerfiles. The Distroless
debug variant supplies only the BusyBox commands needed by the existing
non-root entrypoint, health check, and operational diagnostics; it avoids the
large general-purpose `apt` runtime surface. Updating either base image is a
reviewed dependency change and must pass all four container gates again.

## Secret scanning

Run the same scan used by CI from a complete, non-shallow checkout:

```bash
gitleaks version
scripts/security/scan-secrets.sh build/security
```

The script creates a bounded snapshot containing tracked files plus untracked,
non-ignored files. This covers the proposed tree without scanning local build
caches. It separately scans every reachable branch/tag and the complete Git
history with `--all --full-history`. Both JSON reports are fully redacted.

`.gitleaksignore` contains only exact fingerprints for synthetic test fixtures
and a documented shell-variable false positive. It does not suppress the
credential-like findings in historical README revisions. The branch quality
gate blocks current-tree findings and retains the complete-history report as an
advisory artifact, so inherited history does not permanently hide new CI
regressions. The tagged-release gate still fails closed on either current-tree
or history findings until those credentials are revoked and an authorized
history-remediation procedure is completed. Do not add a blanket path, rule,
or commit allowlist to make either report appear clean.

Ignored local operator/session directories are not CI inputs and must never be
uploaded as workflow artifacts. If a local scan of such a directory finds a
credential, revoke it and securely remove the local artifact; do not copy it
into a baseline.

## SBOM generation

Generate the source dependency inventories with:

```bash
syft version
scripts/security/generate-sbom.sh build/sbom
(cd build/sbom && sha256sum --check --strict SBOM-SHA256SUMS)
```

The script emits and validates two CycloneDX JSON documents:

- `octopus-go.cdx.json`, derived from `go.mod`;
- `octopus-web.cdx.json`, derived from `web/pnpm-lock.yaml`.

The tagged release additionally generates one image SBOM for every combination
of Alpine/Debian and amd64/arm64. Each image SBOM is generated from the exact
registry digest produced by that platform's one and only build; it is not
generated from a similar local rebuild. Release publication requires exactly
all four image SBOMs plus the two source SBOMs. Empty, malformed, duplicated, or
unexpected documents fail the workflow. Each image SBOM carries explicit
`octopus:image-reference` and `octopus:image-platform` properties, and publish
requires them to match the downloaded digest evidence before signing.
The unified SBOM checksum asset is named `SBOM-SHA256SUMS`; the distinct name
prevents it and its Sigstore bundle from colliding with the archives'
`SHA256SUMS` assets on GitHub Releases.

## Vulnerability and container policy

Quality and release workflows run a Trivy filesystem scan for dependency and
deployment misconfiguration findings. After source verification and the real
database matrix pass, the release workflow builds each supported
distribution/architecture image exactly once. It pushes that single-platform
image under a run-unique staging tag, resolves the resulting immutable digest,
and makes Syft and Trivy scan `IMAGE@sha256:...` directly from GHCR. The scan
covers OS and language packages, filesystem contents, image configuration, and
embedded secrets. Any HIGH or CRITICAL vulnerability (including an unfixed
finding) or secret finding blocks official-tag promotion. There is no
repository-wide vulnerability ignore file.

With a final image loaded in the local Docker daemon, reproduce the combined
SBOM/secret/vulnerability gate with:

```bash
scripts/security/scan-container.sh \
  octopus-e2e:debian-arm64 debian-arm64 build/security
```

To reproduce the tagged workflow against an immutable registry subject, use:

```bash
SYFT_IMAGE_SOURCE=registry \
TRIVY_IMAGE_SOURCE=remote \
scripts/security/scan-container.sh \
  ghcr.io/linbmv/octopus@sha256:DIGEST \
  debian-arm64 build/security linux/arm64
```

The retained Trivy JSON is deliberately sanitized: it keeps vulnerability
identifiers, package versions, and secret rule locations, but never matched
secret content. The unsanitized temporary report is deleted on every exit.

An exception must identify the exact vulnerability, affected component,
expiry, compensating control, and approver. It must be narrower than a package
or directory and must be removed on expiry. Adding an exception merely to pass
a release is prohibited.

## Signing, attestations, and publication order

The release workflow has three permission boundaries:

1. `verify` has read-only repository access and builds archives/source SBOMs.
2. `database-matrix` also has read-only repository access. Only after both jobs
   pass does `container-gate` receive `packages: write`; it can push run-unique
   staging platform tags but has no content, attestation, or OIDC write access.
3. `publish` receives `contents: write`, `packages: write`, `id-token: write`,
   and `attestations: write`. It never executes a Dockerfile or rebuilds an
   image; it can only assemble already-scanned platform digests, attest them,
   and promote the verified index digests to official tags.

Platform staging tags use this form:

```text
staging-GITHUB_SHA-GITHUB_RUN_ID-GITHUB_RUN_ATTEMPT-VARIANT-ARCH
staging-index-GITHUB_SHA-GITHUB_RUN_ID-GITHUB_RUN_ATTEMPT-VARIANT
```

The full commit SHA plus run identity makes each platform and assembled-index
staging name unique. A tag is only a registry reachability and audit anchor. For
a platform image, the digest recorded in the short-lived evidence artifact is
authoritative; for an assembled index, the inspected and signed index digest is
authoritative. `publish` validates the tag, commit, run, variant, platform,
digest syntax, sanitized zero-finding Trivy report, and non-empty SBOM before
consuming any platform input.

Both the pull-request quality gate and tagged-release verification install the
lockfile-pinned Playwright Chromium build and run the browser E2E suite. The tag
workflow serves `static/out`, the exact frontend tree embedded in the release
binaries, directly from its canonical repository path with
`OCTOPUS_E2E_SKIP_BUILD=1`; it neither depends on a temporary `web/out` symlink
nor silently builds a different frontend for the browser test. The E2E launcher
fails immediately when `static/out/index.html` is missing. Failure traces are
retained as short-lived CI artifacts. The tag workflow also requires an empty
Git status both immediately before the release build and after build/E2E, so a
test or generator cannot silently change the tagged source being packaged.

After source, database, and all four exact-digest image gates succeed, `publish`
performs these operations in order:

1. verify the eight-archive SHA-256 manifest, the two-source-SBOM manifest, all
   four digest evidence documents, all four zero-finding sanitized Trivy
   reports, and exactly six CycloneDX documents;
2. use `docker buildx imagetools create` to assemble two run-unique staging
   indexes from the recorded amd64/arm64 child digests, then inspect each raw
   index and prove it contains exactly those two scanned platform subjects;
3. keyless-sign every archive, checksum manifest, and SBOM with Cosign, verify
   every bundle against the exact workflow identity, generate GitHub provenance,
   and attach the Go/frontend SBOM attestations to archives;
4. keyless-sign both final index digests, attach the two corresponding exact
   platform SBOMs, and verify the signatures and CycloneDX attestations;
5. publish GitHub provenance for both final index digests and independently
   verify it with GitHub CLI;
6. promote the already signed index digests to the version and `latest` official
   tags with `imagetools create --prefer-index=false`, then resolve every
   promoted tag and require byte-for-byte digest equality;
7. upload the signed archives, checksum manifests, SBOMs, and Sigstore bundles
   to the GitHub Release.

There is deliberately no Docker build in `publish`. This removes the previous
time-of-check/time-of-use gap in which a second build (notably a live Alpine
`apk add`) could differ from the image that passed the gate.

The expected Fulcio certificate identity for tag `TAG` is:

```text
https://github.com/linbmv/octopus/.github/workflows/release.yaml@refs/tags/TAG
```

The expected OIDC issuer is always:

```text
https://token.actions.githubusercontent.com
```

No long-lived signing key is stored in repository secrets.

## Consumer verification

Verify a downloaded archive before extracting or running it:

```bash
sha256sum --check --strict SHA256SUMS
cosign verify-blob \
  --bundle octopus-linux-x86_64.zip.sigstore.json \
  --certificate-identity \
    'https://github.com/linbmv/octopus/.github/workflows/release.yaml@refs/tags/TAG' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  octopus-linux-x86_64.zip
gh attestation verify octopus-linux-x86_64.zip \
  --repo linbmv/octopus \
  --cert-identity \
    'https://github.com/linbmv/octopus/.github/workflows/release.yaml@refs/tags/TAG' \
  --source-ref refs/tags/TAG \
  --deny-self-hosted-runners
```

Resolve a container tag to an immutable digest, then verify that digest:

```bash
IMAGE='ghcr.io/linbmv/octopus@sha256:REPLACE_WITH_RESOLVED_DIGEST'
IDENTITY='https://github.com/linbmv/octopus/.github/workflows/release.yaml@refs/tags/TAG'
ISSUER='https://token.actions.githubusercontent.com'

cosign verify \
  --certificate-identity "${IDENTITY}" \
  --certificate-oidc-issuer "${ISSUER}" \
  "${IMAGE}"
cosign verify-attestation \
  --type cyclonedx \
  --certificate-identity "${IDENTITY}" \
  --certificate-oidc-issuer "${ISSUER}" \
  "${IMAGE}"
gh attestation verify "oci://${IMAGE}" \
  --repo linbmv/octopus \
  --cert-identity "${IDENTITY}" \
  --source-ref refs/tags/TAG \
  --deny-self-hosted-runners
```

Never verify a mutable tag as the final subject, relax the certificate identity
to an organization-wide regular expression, or use Cosign's insecure skip
flags.

## Local verification limits

Workflow syntax, scripts, current-tree/history scanning, source SBOM generation,
and filesystem scanning can be verified locally. Container builds/scans require
a Docker-compatible daemon. Keyless signing requires the GitHub Actions OIDC
identity. A local tool-version check is not evidence that either external gate
has passed; only a successful tagged workflow and independently verified
attestations provide that evidence.
