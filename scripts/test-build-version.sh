#!/bin/bash

set -euo pipefail

readonly REPOSITORY_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly TEST_ROOT="$(mktemp -d)"
trap 'rm -rf -- "${TEST_ROOT}"' EXIT

mkdir -p "${TEST_ROOT}/scripts"
cp "${REPOSITORY_ROOT}/scripts/build.sh" "${TEST_ROOT}/scripts/build.sh"
touch "${TEST_ROOT}/README.md"

git -C "${TEST_ROOT}" init -q
git -C "${TEST_ROOT}" config user.name "Octopus Test"
git -C "${TEST_ROOT}" config user.email "octopus-test@example.invalid"
git -C "${TEST_ROOT}" add README.md scripts/build.sh
git -C "${TEST_ROOT}" commit -qm "test fixture"

clean_version="$(bash "${TEST_ROOT}/scripts/build.sh" version)"
if [[ "${clean_version}" == *-dirty ]]; then
    printf 'clean worktree unexpectedly reported %s\n' "${clean_version}" >&2
    exit 1
fi

printf 'dirty\n' >>"${TEST_ROOT}/README.md"
dirty_version="$(bash "${TEST_ROOT}/scripts/build.sh" version)"
if [[ "${dirty_version}" != "${clean_version}-dirty" ]]; then
    printf 'dirty version %s does not extend clean version %s\n' "${dirty_version}" "${clean_version}" >&2
    exit 1
fi

printf 'build version checks passed: %s -> %s\n' "${clean_version}" "${dirty_version}"
