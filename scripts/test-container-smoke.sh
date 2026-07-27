#!/usr/bin/env bash

set -euo pipefail

readonly DOCKER_BIN="${DOCKER_BIN:-docker}"
readonly IMAGE_REFERENCE="${1:-}"
readonly EXPECTED_PLATFORM="${2:-}"
readonly EXPECTED_REVISION="${3:-}"
readonly EXPECTED_VERSION="${4:-}"
readonly TEST_PASSWORD="${OCTOPUS_CONTAINER_TEST_PASSWORD:-Container-Smoke-Only-2026!}"
readonly SKIP_PULL="${OCTOPUS_SMOKE_SKIP_PULL:-0}"
readonly RUN_ID="octopus-smoke-$(date +%s)-$$"
readonly VOLUME_NAME="${RUN_ID}-data"

containers=()

docker_cmd() {
    "${DOCKER_BIN}" "$@"
}

fail() {
    printf 'container smoke: %s\n' "$*" >&2
    exit 1
}

cleanup() {
    local container
    for container in "${containers[@]}"; do
        docker_cmd rm -f "${container}" >/dev/null 2>&1 || true
    done
    docker_cmd volume rm -f "${VOLUME_NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

wait_healthy() {
    local container="$1"
    local deadline=$((SECONDS + 120))
    local state
    local health

    while ((SECONDS < deadline)); do
        state="$(docker_cmd inspect --format '{{.State.Status}}' "${container}")"
        health="$(docker_cmd inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "${container}")"
        case "${health}" in
        healthy) return 0 ;;
        unhealthy)
            docker_cmd logs "${container}" >&2 || true
            fail "${container} became unhealthy"
            ;;
        esac
        if [ "${state}" = "exited" ] || [ "${state}" = "dead" ]; then
            docker_cmd logs "${container}" >&2 || true
            fail "${container} exited before becoming healthy"
        fi
        sleep 1
    done

    docker_cmd logs "${container}" >&2 || true
    fail "${container} did not become healthy"
}

start_and_verify() {
    local container="$1"
    local initialize="$2"
    local port
    local http_status
    local -a environment_args=()

    if [ "${initialize}" = "true" ]; then
        environment_args=(--env "OCTOPUS_INITIAL_ADMIN_PASSWORD=${TEST_PASSWORD}")
    fi

    containers+=("${container}")
    docker_cmd run --detach \
        --platform "${EXPECTED_PLATFORM}" \
        --name "${container}" \
        --read-only \
        --health-interval 1s --health-timeout 3s --health-retries 30 --health-start-period 1s \
        --tmpfs /tmp:rw,noexec,nosuid,nodev,size=192m,mode=1777 \
        --cap-drop ALL \
        --security-opt no-new-privileges:true \
        --mount "type=volume,source=${VOLUME_NAME},target=/app/data" \
        --publish 127.0.0.1::8080 \
        "${environment_args[@]}" \
        "${IMAGE_REFERENCE}" >/dev/null

    wait_healthy "${container}"
    port="$(docker_cmd port "${container}" 8080/tcp | awk -F: 'END {print $NF}')"
    [ -n "${port}" ] || fail "${container} has no mapped HTTP port"
    http_status="$(curl --silent --show-error --max-time 5 --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:${port}/health")"
    [ "${http_status}" = "200" ] || fail "${container} /health returned ${http_status}"
    [ "$(docker_cmd inspect --format '{{.Config.User}}' "${container}")" = "10001:10001" ] || \
        fail "${container} does not run as UID:GID 10001:10001"
    [ "$(docker_cmd inspect --format '{{.HostConfig.ReadonlyRootfs}}' "${container}")" = "true" ] || \
        fail "${container} root filesystem is writable"
    docker_cmd inspect --format '{{json .HostConfig.CapDrop}}' "${container}" | \
        grep -q 'ALL' || fail "${container} does not drop all capabilities"
    docker_cmd inspect --format '{{json .HostConfig.SecurityOpt}}' "${container}" | \
        grep -q 'no-new-privileges' || fail "${container} lacks no-new-privileges"
    if docker_cmd logs "${container}" 2>&1 | grep -Fq "${TEST_PASSWORD}"; then
        fail "${container} logged the initial administrator password"
    fi

    docker_cmd stop --time 30 "${container}" >/dev/null
    [ "$(docker_cmd inspect --format '{{.State.ExitCode}}' "${container}")" = "0" ] || \
        fail "${container} did not exit cleanly after SIGTERM"
    docker_cmd rm "${container}" >/dev/null
}

main() {
    local image_platform
    local image_revision
    local version_output

    [ "$#" -eq 4 ] || fail "usage: test-container-smoke.sh IMAGE PLATFORM REVISION VERSION"
    case "${EXPECTED_PLATFORM}" in
    linux/amd64 | linux/arm64) ;;
    *) fail "platform must be linux/amd64 or linux/arm64" ;;
    esac
    [[ "${EXPECTED_REVISION}" =~ ^[0-9a-f]{40}$ ]] || fail "revision must be a full Git SHA"
    [ -n "${EXPECTED_VERSION}" ] || fail "version must not be empty"
    case "${SKIP_PULL}" in
    0 | 1) ;;
    *) fail "OCTOPUS_SMOKE_SKIP_PULL must be 0 or 1" ;;
    esac

    docker_cmd version >/dev/null
    if [ "${SKIP_PULL}" = "0" ]; then
        docker_cmd pull --quiet --platform "${EXPECTED_PLATFORM}" "${IMAGE_REFERENCE}" >/dev/null
    fi
    image_platform="$(docker_cmd image inspect --format '{{.Os}}/{{.Architecture}}' "${IMAGE_REFERENCE}")"
    [ "${image_platform}" = "${EXPECTED_PLATFORM}" ] || \
        fail "image platform ${image_platform} does not match ${EXPECTED_PLATFORM}"
    image_revision="$(docker_cmd image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "${IMAGE_REFERENCE}")"
    [ "${image_revision}" = "${EXPECTED_REVISION}" ] || \
        fail "image revision ${image_revision} does not match ${EXPECTED_REVISION}"
    version_output="$(docker_cmd run --rm --platform "${EXPECTED_PLATFORM}" \
        --entrypoint /app/octopus "${IMAGE_REFERENCE}" version)"
    grep -Fq "Version: ${EXPECTED_VERSION}" <<<"${version_output}" || \
        fail "image binary does not report version ${EXPECTED_VERSION}"
    grep -Eq "Go Version: .* ${EXPECTED_PLATFORM}[[:space:]]*$" <<<"${version_output}" || \
        fail "image binary does not report platform ${EXPECTED_PLATFORM}"

    docker_cmd volume create "${VOLUME_NAME}" >/dev/null
    start_and_verify "${RUN_ID}-initial" true
    start_and_verify "${RUN_ID}-restart" false

    printf 'container smoke: %s on %s passed startup, health, persistence, hardening, and shutdown checks\n' \
        "${IMAGE_REFERENCE}" "${EXPECTED_PLATFORM}"
}

main "$@"
