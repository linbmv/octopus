#!/usr/bin/env bash

set -uo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
readonly GITLEAKS_BIN="${GITLEAKS_BIN:-gitleaks}"
readonly GITLEAKS_TIMEOUT_SECONDS="${GITLEAKS_TIMEOUT_SECONDS:-900}"
readonly GITLEAKS_ENFORCE_HISTORY="${GITLEAKS_ENFORCE_HISTORY:-true}"
readonly REPORT_DIRECTORY_INPUT="${1:-build/security}"

fail() {
    printf 'secret scan: %s\n' "$*" >&2
    exit 2
}

command -v git >/dev/null 2>&1 || fail "git is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"
command -v "${GITLEAKS_BIN}" >/dev/null 2>&1 || fail "gitleaks is required"
[[ "${GITLEAKS_TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || fail "GITLEAKS_TIMEOUT_SECONDS must be a positive integer"
case "${GITLEAKS_ENFORCE_HISTORY}" in
true | false) ;;
*) fail "GITLEAKS_ENFORCE_HISTORY must be true or false" ;;
esac

cd "${REPOSITORY_ROOT}" || fail "cannot enter repository root"
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || fail "not a Git work tree"

if [ "$(git rev-parse --is-shallow-repository)" = "true" ]; then
    fail "full-history scanning requires a non-shallow checkout"
fi

mkdir -p "${REPORT_DIRECTORY_INPUT}" || fail "cannot create report directory"
readonly REPORT_DIRECTORY="$(cd -- "${REPORT_DIRECTORY_INPUT}" && pwd)"
readonly TREE_REPORT="${REPORT_DIRECTORY}/gitleaks-working-tree.json"
readonly HISTORY_REPORT="${REPORT_DIRECTORY}/gitleaks-history.json"
readonly IGNORE_FILE="${REPOSITORY_ROOT}/.gitleaksignore"
readonly SNAPSHOT_DIRECTORY="$(mktemp -d "${TMPDIR:-/tmp}/octopus-gitleaks-tree.XXXXXX")"

cleanup() {
    rm -rf -- "${SNAPSHOT_DIRECTORY}"
}
trap cleanup EXIT

# A failed rerun must not upload reports produced by an older scan.
rm -f -- "${TREE_REPORT}" "${HISTORY_REPORT}"

# Scan only repository files: tracked files plus untracked, non-ignored files.
# This prevents local dependency/build caches from creating unbounded scans,
# while still covering files that would be added by the current change.
existing_repository_files() {
    local path
    while IFS= read -r -d '' path; do
        # The index legitimately contains paths deleted by the working-tree
        # change. Broken symlinks are still repository entries and are kept.
        if [ -e "${path}" ] || [ -L "${path}" ]; then
            printf '%s\0' "${path}"
        fi
    done
}

if ! git ls-files --cached --others --exclude-standard --deduplicate -z |
    existing_repository_files |
    tar --create --file=- --null --verbatim-files-from --files-from=- |
    tar --extract --file=- --directory="${SNAPSHOT_DIRECTORY}"; then
    fail "could not create the working-tree scan snapshot"
fi

run_scan() {
    local description="$1"
    shift

    "$@"
    local status=$?

    case "${status}" in
    0)
        printf 'secret scan: %s passed\n' "${description}"
        return 0
        ;;
    10)
        printf 'secret scan: %s found credential-like content; inspect the redacted JSON report\n' \
            "${description}" >&2
        return 10
        ;;
    *)
        printf 'secret scan: %s failed with tool exit code %s\n' \
            "${description}" "${status}" >&2
        return 2
        ;;
    esac
}

tree_status=0
scan_working_tree() (
    # Running from inside the snapshot keeps fingerprints repository-relative,
    # so narrowly scoped .gitleaksignore entries remain stable across machines.
    cd "${SNAPSHOT_DIRECTORY}" || return 2
    "${GITLEAKS_BIN}" dir \
        --no-banner \
        --no-color \
        --redact=100 \
        --timeout "${GITLEAKS_TIMEOUT_SECONDS}" \
        --exit-code 10 \
        --report-format json \
        --report-path "${TREE_REPORT}" \
        --gitleaks-ignore-path "${IGNORE_FILE}" \
        .
)
run_scan "working tree" scan_working_tree || tree_status=$?

history_status=0
run_scan "complete Git history" \
    "${GITLEAKS_BIN}" git \
    --no-banner \
    --no-color \
    --redact=100 \
    --timeout "${GITLEAKS_TIMEOUT_SECONDS}" \
    --exit-code 10 \
    --report-format json \
    --report-path "${HISTORY_REPORT}" \
    --gitleaks-ignore-path "${IGNORE_FILE}" \
    --log-opts="--all --full-history" \
    "${REPOSITORY_ROOT}" || history_status=$?

# Gitleaks may omit an empty report. Keep CI artifacts machine-readable.
[ -f "${TREE_REPORT}" ] || printf '[]\n' >"${TREE_REPORT}"
[ -f "${HISTORY_REPORT}" ] || printf '[]\n' >"${HISTORY_REPORT}"

if [ "${tree_status}" -eq 2 ] || [ "${history_status}" -eq 2 ]; then
    exit 2
fi
if [ "${tree_status}" -ne 0 ]; then
    exit 1
fi
if [ "${history_status}" -ne 0 ]; then
    if [ "${GITLEAKS_ENFORCE_HISTORY}" = "true" ]; then
        exit 1
    fi
    printf 'secret scan: complete Git history findings are advisory for this gate; tagged release remains fail-closed\n' >&2
fi
