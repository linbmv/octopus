#!/usr/bin/env bash

set -euo pipefail

readonly IMAGE_REFERENCE="${1:-}"
readonly EVIDENCE_NAME="${2:-}"
readonly OUTPUT_DIRECTORY_INPUT="${3:-build/security}"
readonly IMAGE_PLATFORM="${4:-${IMAGE_PLATFORM:-}}"
readonly TRIVY_BIN="${TRIVY_BIN:-trivy}"
readonly SYFT_BIN="${SYFT_BIN:-syft}"
readonly TRIVY_IMAGE_SOURCE="${TRIVY_IMAGE_SOURCE:-docker}"
readonly SYFT_IMAGE_SOURCE="${SYFT_IMAGE_SOURCE:-docker}"

fail() {
    printf 'container scan: %s\n' "$*" >&2
    exit 1
}

[ -n "${IMAGE_REFERENCE}" ] || fail "usage: scan-container.sh IMAGE EVIDENCE_NAME [OUTPUT_DIRECTORY] [PLATFORM]"
[[ "${EVIDENCE_NAME}" =~ ^[A-Za-z0-9._-]+$ ]] || fail "evidence name contains unsupported characters"
if [ -n "${IMAGE_PLATFORM}" ]; then
    [[ "${IMAGE_PLATFORM}" =~ ^[a-z0-9]+/[a-z0-9][a-z0-9._-]*$ ]] || \
        fail "platform must use the os/architecture form"
fi
case "${SYFT_IMAGE_SOURCE}" in
docker | registry) ;;
*) fail "SYFT_IMAGE_SOURCE must be docker or registry" ;;
esac
case "${TRIVY_IMAGE_SOURCE}" in
docker | containerd | podman | remote) ;;
*) fail "TRIVY_IMAGE_SOURCE is unsupported" ;;
esac
command -v "${TRIVY_BIN}" >/dev/null 2>&1 || fail "trivy is required"
command -v "${SYFT_BIN}" >/dev/null 2>&1 || fail "syft is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

mkdir -p "${OUTPUT_DIRECTORY_INPUT}"
readonly OUTPUT_DIRECTORY="$(cd -- "${OUTPUT_DIRECTORY_INPUT}" && pwd)"
readonly SBOM_PATH="${OUTPUT_DIRECTORY}/octopus-${EVIDENCE_NAME}.cdx.json"
readonly REPORT_PATH="${OUTPUT_DIRECTORY}/trivy-${EVIDENCE_NAME}.json"
RAW_REPORT=""
SBOM_REWRITE=""

cleanup() {
    if [ -n "${RAW_REPORT}" ]; then
        rm -f -- "${RAW_REPORT}"
    fi
    if [ -n "${SBOM_REWRITE}" ]; then
        rm -f -- "${SBOM_REWRITE}"
    fi
}
trap cleanup EXIT

RAW_REPORT="$(mktemp "${TMPDIR:-/tmp}/octopus-trivy-raw.XXXXXX.json")"
SBOM_REWRITE="$(mktemp "${TMPDIR:-/tmp}/octopus-sbom-rewrite.XXXXXX.json")"
readonly RAW_REPORT SBOM_REWRITE

# Never let a failed rerun leave apparently current evidence from an older
# image under the same deterministic evidence name.
rm -f -- "${SBOM_PATH}" "${REPORT_PATH}"

export SYFT_CHECK_FOR_APP_UPDATE=false
syft_platform_args=()
trivy_platform_args=()
# A Docker source has already been pulled with the requested platform. Passing
# --platform again makes the scanner resolve the local daemon image a second
# time and is inconsistent across Docker/Buildx versions. Keep the platform
# metadata below, but let the local image source determine the scanned image.
if [ -n "${IMAGE_PLATFORM}" ] && [ "${SYFT_IMAGE_SOURCE}" != docker ]; then
    syft_platform_args=(--platform "${IMAGE_PLATFORM}")
fi
if [ -n "${IMAGE_PLATFORM}" ] && [ "${TRIVY_IMAGE_SOURCE}" != docker ]; then
    trivy_platform_args=(--platform "${IMAGE_PLATFORM}")
fi

"${SYFT_BIN}" scan "${SYFT_IMAGE_SOURCE}:${IMAGE_REFERENCE}" \
    --quiet \
    "${syft_platform_args[@]}" \
    --source-name "octopus-${EVIDENCE_NAME}" \
    --source-version "${SBOM_VERSION:-local}" \
    --output "cyclonedx-json=${SBOM_PATH}"

# Bind the retained SBOM to the immutable registry subject and platform that
# were actually scanned. Local daemon scans retain their tag in the same field.
jq \
    --arg image_reference "${IMAGE_REFERENCE}" \
    --arg image_platform "${IMAGE_PLATFORM}" \
    '.metadata.component.properties =
        ((.metadata.component.properties // []) +
         [{name: "octopus:image-reference", value: $image_reference}] +
         (if $image_platform == "" then []
          else [{name: "octopus:image-platform", value: $image_platform}]
          end))' \
    "${SBOM_PATH}" >"${SBOM_REWRITE}"
mv -f -- "${SBOM_REWRITE}" "${SBOM_PATH}"
jq -e '.bomFormat == "CycloneDX" and ((.components // []) | length > 0)' \
    "${SBOM_PATH}" >/dev/null || fail "Syft produced an invalid or empty SBOM"

TRIVY_IMAGE_CONFIG_SCANNERS=secret "${TRIVY_BIN}" image \
    --skip-version-check \
    --image-src "${TRIVY_IMAGE_SOURCE}" \
    "${trivy_platform_args[@]}" \
    --scanners vuln,secret \
    --severity HIGH,CRITICAL \
    --timeout 15m \
    --exit-code 0 \
    --format json \
    --output "${RAW_REPORT}" \
    "${IMAGE_REFERENCE}"

# Do not retain matched content from secret findings. The evidence report keeps
# only identifiers and locations needed to investigate without reproducing a
# credential in CI artifacts.
jq '{
    SchemaVersion,
    ArtifactName,
    ArtifactType,
    Metadata: {
        OS: .Metadata.OS,
        ImageID: .Metadata.ImageID,
        DiffIDs: .Metadata.DiffIDs,
        RepoDigests: .Metadata.RepoDigests
    },
    Results: [
        .Results[]? | {
            Target,
            Class,
            Type,
            Vulnerabilities: [
                (.Vulnerabilities // [])[] | {
                    VulnerabilityID,
                    PkgName,
                    InstalledVersion,
                    FixedVersion,
                    Status,
                    Severity
                }
            ],
            Secrets: [
                (.Secrets // [])[] | {
                    RuleID,
                    Category,
                    Severity,
                    Title,
                    StartLine,
                    EndLine
                }
            ]
        }
    ]
}' "${RAW_REPORT}" >"${REPORT_PATH}"

readonly VULNERABILITY_COUNT="$(jq '[.Results[]? | .Vulnerabilities[]?] | length' "${REPORT_PATH}")"
readonly SECRET_COUNT="$(jq '[.Results[]? | .Secrets[]?] | length' "${REPORT_PATH}")"

printf 'container scan: %s components=%s high_or_critical=%s secrets=%s\n' \
    "${EVIDENCE_NAME}" \
    "$(jq '(.components // []) | length' "${SBOM_PATH}")" \
    "${VULNERABILITY_COUNT}" \
    "${SECRET_COUNT}"

[ "${VULNERABILITY_COUNT}" -eq 0 ] || fail "HIGH/CRITICAL vulnerabilities found"
[ "${SECRET_COUNT}" -eq 0 ] || fail "embedded secrets found"
