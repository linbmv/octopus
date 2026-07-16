#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
readonly COSIGN_BIN="${COSIGN_BIN:-cosign}"
readonly ARCHIVE_DIRECTORY_INPUT="${1:-build/archives}"
readonly SBOM_DIRECTORY_INPUT="${2:-build/sbom}"
readonly ARCHIVE_CHECKSUM_MANIFEST="SHA256SUMS"
readonly SBOM_CHECKSUM_MANIFEST="SBOM-SHA256SUMS"
readonly EXPECTED_ARCHIVES=(
    "octopus-darwin-arm64.zip"
    "octopus-darwin-x86_64.zip"
    "octopus-linux-arm64.zip"
    "octopus-linux-armv7.zip"
    "octopus-linux-x86.zip"
    "octopus-linux-x86_64.zip"
    "octopus-windows-x86.zip"
    "octopus-windows-x86_64.zip"
)
readonly EXPECTED_SBOMS=(
    "octopus-alpine-amd64.cdx.json"
    "octopus-alpine-arm64.cdx.json"
    "octopus-debian-amd64.cdx.json"
    "octopus-debian-arm64.cdx.json"
    "octopus-go.cdx.json"
    "octopus-web.cdx.json"
)

fail() {
    printf 'release signing: %s\n' "$*" >&2
    exit 1
}

command -v "${COSIGN_BIN}" >/dev/null 2>&1 || fail "cosign is required"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

: "${GITHUB_SERVER_URL:?GITHUB_SERVER_URL is required for keyless identity verification}"
: "${GITHUB_WORKFLOW_REF:?GITHUB_WORKFLOW_REF is required for keyless identity verification}"

cd "${REPOSITORY_ROOT}"
[ ! -L "${ARCHIVE_DIRECTORY_INPUT}" ] || fail "archive directory must not be a symbolic link"
[ ! -L "${SBOM_DIRECTORY_INPUT}" ] || fail "SBOM directory must not be a symbolic link"
readonly ARCHIVE_DIRECTORY="$(cd -- "${ARCHIVE_DIRECTORY_INPUT}" && pwd)"
readonly SBOM_DIRECTORY="$(cd -- "${SBOM_DIRECTORY_INPUT}" && pwd)"
readonly CERTIFICATE_IDENTITY="${GITHUB_SERVER_URL}/${GITHUB_WORKFLOW_REF}"
readonly OIDC_ISSUER="https://token.actions.githubusercontent.com"

remove_generated_bundles() {
    local directory="$1"
    local manifest="$2"
    shift 2

    local file
    for file in "$@" "${manifest}"; do
        rm -f -- "${directory}/${file}.sigstore.json"
    done
}

validate_directory_contents() {
    local directory="$1"
    local manifest="$2"
    shift 2

    local entry
    local expected
    local allowed
    while IFS= read -r -d '' entry; do
        allowed=0
        if [ "$(basename -- "${entry}")" = "${manifest}" ]; then
            allowed=1
        else
            for expected in "$@"; do
                if [ "$(basename -- "${entry}")" = "${expected}" ]; then
                    allowed=1
                    break
                fi
            done
        fi
        [ "${allowed}" -eq 1 ] || fail "unexpected release input: ${entry}"
    done < <(find "${directory}" -mindepth 1 -maxdepth 1 -print0)

    for expected in "$@"; do
        [ -f "${directory}/${expected}" ] && [ ! -L "${directory}/${expected}" ] || \
            fail "missing or invalid release input: ${directory}/${expected}"
    done
    [ -f "${directory}/${manifest}" ] && [ ! -L "${directory}/${manifest}" ] || \
        fail "missing or invalid checksum manifest: ${directory}/${manifest}"
}

validate_archive_manifest() {
    local manifest="${ARCHIVE_DIRECTORY}/${ARCHIVE_CHECKSUM_MANIFEST}"
    local -a manifest_files=()
    awk '
        NF != 2 || length($1) != 64 || $1 ~ /[^0-9a-fA-F]/ { exit 1 }
    ' "${manifest}" || fail "archive checksum manifest has invalid syntax"

    mapfile -t manifest_files < <(
        awk '{ name=$2; sub(/^\\*/, "", name); sub(/^\.\//, "", name); print name }' \
            "${manifest}"
    )
    [ "${#manifest_files[@]}" -eq "${#EXPECTED_ARCHIVES[@]}" ] || \
        fail "archive checksum manifest must contain exactly ${#EXPECTED_ARCHIVES[@]} entries"

    local expected
    local candidate
    local occurrences
    for expected in "${EXPECTED_ARCHIVES[@]}"; do
        occurrences=0
        for candidate in "${manifest_files[@]}"; do
            if [ "${candidate}" = "${expected}" ]; then
                occurrences=$((occurrences + 1))
            fi
        done
        [ "${occurrences}" -eq 1 ] || fail "archive checksum manifest does not uniquely cover ${expected}"
    done
}

remove_generated_bundles "${ARCHIVE_DIRECTORY}" "${ARCHIVE_CHECKSUM_MANIFEST}" "${EXPECTED_ARCHIVES[@]}"
remove_generated_bundles "${SBOM_DIRECTORY}" "${SBOM_CHECKSUM_MANIFEST}" "${EXPECTED_SBOMS[@]}"
validate_directory_contents "${ARCHIVE_DIRECTORY}" "${ARCHIVE_CHECKSUM_MANIFEST}" "${EXPECTED_ARCHIVES[@]}"
validate_directory_contents "${SBOM_DIRECTORY}" "${SBOM_CHECKSUM_MANIFEST}" "${EXPECTED_SBOMS[@]}"
validate_archive_manifest

for file in "${EXPECTED_SBOMS[@]}"; do
    jq -e '
        .bomFormat == "CycloneDX" and
        (.specVersion | type == "string") and
        ((.components // []) | length > 0)
    ' "${SBOM_DIRECTORY}/${file}" >/dev/null || fail "invalid or empty CycloneDX document: ${file}"
done

(
    cd "${ARCHIVE_DIRECTORY}"
    sha256sum --check --strict "${ARCHIVE_CHECKSUM_MANIFEST}" >/dev/null
)

# Container-gate jobs add their SBOMs after the source SBOM job. Rebuild one
# deterministic manifest that covers every CycloneDX document being released.
(
    cd "${SBOM_DIRECTORY}"
    sha256sum "${EXPECTED_SBOMS[@]}" >"${SBOM_CHECKSUM_MANIFEST}"
    sha256sum --check --strict "${SBOM_CHECKSUM_MANIFEST}" >/dev/null
)

release_files=()
for file in "${EXPECTED_ARCHIVES[@]}"; do
    release_files+=("${ARCHIVE_DIRECTORY}/${file}")
done
release_files+=("${ARCHIVE_DIRECTORY}/${ARCHIVE_CHECKSUM_MANIFEST}")
for file in "${EXPECTED_SBOMS[@]}"; do
    release_files+=("${SBOM_DIRECTORY}/${file}")
done
release_files+=("${SBOM_DIRECTORY}/${SBOM_CHECKSUM_MANIFEST}")

for file in "${release_files[@]}"; do
    bundle="${file}.sigstore.json"
    "${COSIGN_BIN}" sign-blob --yes --bundle "${bundle}" "${file}" >/dev/null
    "${COSIGN_BIN}" verify-blob \
        --bundle "${bundle}" \
        --certificate-identity "${CERTIFICATE_IDENTITY}" \
        --certificate-oidc-issuer "${OIDC_ISSUER}" \
        "${file}" >/dev/null
    [ -s "${bundle}" ] || fail "cosign did not create a bundle for ${file}"
done

printf 'release signing: signed and verified %s release files\n' "${#release_files[@]}"
