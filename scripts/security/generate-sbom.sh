#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
readonly SYFT_BIN="${SYFT_BIN:-syft}"
readonly OUTPUT_DIRECTORY_INPUT="${1:-build/sbom}"

fail() {
    printf 'SBOM generation: %s\n' "$*" >&2
    exit 1
}

command -v "${SYFT_BIN}" >/dev/null 2>&1 || fail "syft is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"

cd "${REPOSITORY_ROOT}"
[ ! -L "${OUTPUT_DIRECTORY_INPUT}" ] || fail "output directory must not be a symbolic link"
mkdir -p "${OUTPUT_DIRECTORY_INPUT}"
readonly OUTPUT_DIRECTORY="$(cd -- "${OUTPUT_DIRECTORY_INPUT}" && pwd)"
readonly SOURCE_VERSION="${SBOM_VERSION:-$(git describe --tags --exact-match 2>/dev/null || git rev-parse HEAD)}"

# This directory is a release input, so refuse to preserve unrelated or stale
# content. A previous successful source-SBOM set may be replaced idempotently.
while IFS= read -r -d '' existing; do
    case "$(basename -- "${existing}")" in
    octopus-go.cdx.json | octopus-web.cdx.json | SHA256SUMS | SBOM-SHA256SUMS) ;;
    *) fail "output directory contains unexpected content: ${existing}" ;;
    esac
done < <(find "${OUTPUT_DIRECTORY}" -mindepth 1 -maxdepth 1 -print0)

STAGING_DIRECTORY=""
CACHE_DIRECTORY=""

cleanup() {
    if [ -n "${CACHE_DIRECTORY}" ]; then
        rm -rf -- "${CACHE_DIRECTORY}"
    fi
    if [ -n "${STAGING_DIRECTORY}" ]; then
        rm -rf -- "${STAGING_DIRECTORY}"
    fi
}
trap cleanup EXIT

STAGING_DIRECTORY="$(mktemp -d "${OUTPUT_DIRECTORY}/.octopus-sbom.XXXXXX")"
CACHE_DIRECTORY="$(mktemp -d "${TMPDIR:-/tmp}/octopus-syft-cache.XXXXXX")"
readonly STAGING_DIRECTORY CACHE_DIRECTORY
readonly GO_SBOM="${STAGING_DIRECTORY}/octopus-go.cdx.json"
readonly WEB_SBOM="${STAGING_DIRECTORY}/octopus-web.cdx.json"

export SYFT_CHECK_FOR_APP_UPDATE=false
export XDG_CACHE_HOME="${CACHE_DIRECTORY}"

"${SYFT_BIN}" scan file:go.mod \
    --quiet \
    --source-name octopus-go \
    --source-version "${SOURCE_VERSION}" \
    --output "cyclonedx-json=${GO_SBOM}"

"${SYFT_BIN}" scan file:web/pnpm-lock.yaml \
    --quiet \
    --source-name octopus-web \
    --source-version "${SOURCE_VERSION}" \
    --output "cyclonedx-json=${WEB_SBOM}"

validate_sbom() {
    local path="$1"
    jq -e '
        .bomFormat == "CycloneDX" and
        (.specVersion | type == "string") and
        ((.components // []) | length > 0)
    ' "${path}" >/dev/null || fail "invalid or empty CycloneDX document: ${path}"
}

validate_sbom "${GO_SBOM}"
validate_sbom "${WEB_SBOM}"

(
    cd "${STAGING_DIRECTORY}"
    sha256sum octopus-go.cdx.json octopus-web.cdx.json >SBOM-SHA256SUMS
    sha256sum --check --strict SBOM-SHA256SUMS >/dev/null
)

# Remove the legacy generic name only after the replacement set is complete.
rm -f -- "${OUTPUT_DIRECTORY}/SHA256SUMS"
mv -f -- \
    "${GO_SBOM}" \
    "${WEB_SBOM}" \
    "${STAGING_DIRECTORY}/SBOM-SHA256SUMS" \
    "${OUTPUT_DIRECTORY}/"

printf 'SBOM generation: wrote validated Go and frontend CycloneDX documents to %s\n' \
    "${OUTPUT_DIRECTORY}"
