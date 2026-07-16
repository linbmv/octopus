#!/usr/bin/env bash

set -euo pipefail

DOCKER_BIN="${DOCKER_BIN:-docker}"
ALPINE_IMAGE="${ALPINE_IMAGE:-octopus-e2e:alpine-arm64}"
DEBIAN_IMAGE="${DEBIAN_IMAGE:-octopus-e2e:debian-arm64}"
TEST_PASSWORD="${OCTOPUS_CONTAINER_TEST_PASSWORD:-Container-E2E-Only-2026!}"
RUN_ID="octopus-matrix-$(date +%s)-$$"

containers=()
volumes=()
temporary_dirs=()
compose_projects=()

docker_cmd() {
    "$DOCKER_BIN" "$@"
}

cleanup() {
    local item
    for item in "${containers[@]}"; do
        docker_cmd rm -f "$item" >/dev/null 2>&1 || true
    done
    for item in "${volumes[@]}"; do
        docker_cmd volume rm -f "$item" >/dev/null 2>&1 || true
    done
    for item in "${compose_projects[@]}"; do
        docker_cmd compose --project-name "$item" down --volumes --remove-orphans >/dev/null 2>&1 || true
    done
    for item in "${temporary_dirs[@]}"; do
        rm -rf "$item"
    done
}
trap cleanup EXIT INT TERM

fail() {
    echo "container matrix: $*" >&2
    exit 1
}

wait_healthy() {
    local container="$1"
    local deadline=$((SECONDS + 120))
    local status
    while ((SECONDS < deadline)); do
        status="$(docker_cmd inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container")"
        case "$status" in
            healthy) return 0 ;;
            unhealthy)
                docker_cmd logs "$container" >&2 || true
                fail "$container became unhealthy"
                ;;
        esac
        sleep 1
    done
    docker_cmd logs "$container" >&2 || true
    fail "$container did not become healthy"
}

mapped_port() {
    docker_cmd port "$1" 8080/tcp | awk -F: 'END {print $NF}'
}

assert_runtime_hardening() {
    local container="$1"
    local expected_user="$2"
    local port
    local http_status

    wait_healthy "$container"
    port="$(mapped_port "$container")"
    [ -n "$port" ] || fail "$container has no mapped HTTP port"
    http_status="$(curl --silent --show-error --max-time 5 --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:${port}/health")"
    [ "$http_status" = 200 ] || fail "$container /health returned $http_status"

    [ "$(docker_cmd inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$container")" = true ] || fail "$container root filesystem is writable"
    [ "$(docker_cmd inspect --format '{{.Config.User}}' "$container")" = "$expected_user" ] || fail "$container image user mismatch"
    docker_cmd inspect --format '{{json .HostConfig.CapDrop}} {{json .HostConfig.SecurityOpt}} {{json .HostConfig.Tmpfs}}' "$container" |
        grep -q 'ALL' || fail "$container does not drop all capabilities"
    docker_cmd inspect --format '{{json .HostConfig.SecurityOpt}}' "$container" |
        grep -q 'no-new-privileges' || fail "$container lacks no-new-privileges"

    docker_cmd exec "$container" sh -ec '
        test -w /app/data
        test -w /tmp
        touch /tmp/octopus-write-test
        rm /tmp/octopus-write-test
        if touch /app/octopus-write-test 2>/dev/null; then
            rm -f /app/octopus-write-test
            exit 1
        fi
    ' || fail "$container filesystem write boundaries are incorrect"

    if docker_cmd logs "$container" 2>&1 | grep -Fq "$TEST_PASSWORD"; then
        fail "$container logged the initial administrator password"
    fi
}

stop_cleanly() {
    local container="$1"
    docker_cmd stop --time 30 "$container" >/dev/null
    [ "$(docker_cmd inspect --format '{{.State.ExitCode}}' "$container")" = 0 ] || fail "$container did not exit cleanly after SIGTERM"
}

run_named_volume_case() {
    local image="$1"
    local label="$2"
    local volume="${RUN_ID}-${label}-volume"
    local first="${RUN_ID}-${label}-named-1"
    local second="${RUN_ID}-${label}-named-2"

    docker_cmd volume create "$volume" >/dev/null
    volumes+=("$volume")
    containers+=("$first" "$second")

    docker_cmd run --detach --name "$first" \
        --read-only \
        --health-interval 1s --health-timeout 3s --health-retries 30 --health-start-period 1s \
        --tmpfs /tmp:rw,noexec,nosuid,nodev,size=192m,mode=1777 \
        --cap-drop ALL \
        --security-opt no-new-privileges:true \
        --mount "type=volume,source=${volume},target=/app/data" \
        --publish 127.0.0.1::8080 \
        --env "OCTOPUS_INITIAL_ADMIN_PASSWORD=${TEST_PASSWORD}" \
        "$image" >/dev/null
    assert_runtime_hardening "$first" "10001:10001"
    stop_cleanly "$first"
    docker_cmd rm "$first" >/dev/null

    docker_cmd run --detach --name "$second" \
        --read-only \
        --health-interval 1s --health-timeout 3s --health-retries 30 --health-start-period 1s \
        --tmpfs /tmp:rw,noexec,nosuid,nodev,size=192m,mode=1777 \
        --cap-drop ALL \
        --security-opt no-new-privileges:true \
        --mount "type=volume,source=${volume},target=/app/data" \
        --publish 127.0.0.1::8080 \
        "$image" >/dev/null
    assert_runtime_hardening "$second" "10001:10001"
    stop_cleanly "$second"
    docker_cmd rm "$second" >/dev/null

    echo "container matrix: $label named-volume persistence passed"
}

run_bind_mount_case() {
    local image="$1"
    local label="$2"
    local bind_dir
    local container="${RUN_ID}-${label}-bind"

    bind_dir="$(mktemp -d "/tmp/${RUN_ID}-${label}-bind.XXXXXX")"
    temporary_dirs+=("$bind_dir")
    chmod 0700 "$bind_dir"
    chown 10001:10001 "$bind_dir"
    containers+=("$container")

    docker_cmd run --detach --name "$container" \
        --read-only \
        --health-interval 1s --health-timeout 3s --health-retries 30 --health-start-period 1s \
        --tmpfs /tmp:rw,noexec,nosuid,nodev,size=192m,mode=1777 \
        --cap-drop ALL \
        --security-opt no-new-privileges:true \
        --mount "type=bind,source=${bind_dir},target=/app/data" \
        --publish 127.0.0.1::8080 \
        --env "OCTOPUS_INITIAL_ADMIN_PASSWORD=${TEST_PASSWORD}" \
        "$image" >/dev/null
    assert_runtime_hardening "$container" "10001:10001"
    stop_cleanly "$container"
    docker_cmd rm "$container" >/dev/null

    [ -f "$bind_dir/data.db" ] || fail "$label bind mount did not persist SQLite data"
    echo "container matrix: $label prepared bind mount passed"
}

run_root_owned_bind_recovery_case() {
    local image="$1"
    local label="$2"
    local bind_dir
    local failed_output
    local container="${RUN_ID}-${label}-root-owned-bind"

    bind_dir="$(mktemp -d "/tmp/${RUN_ID}-${label}-root-owned-bind.XXXXXX")"
    temporary_dirs+=("$bind_dir")
    chmod 0700 "$bind_dir"
    chown 0:0 "$bind_dir"

    failed_output="$(mktemp "/tmp/${RUN_ID}-${label}-expected-failure.XXXXXX")"
    temporary_dirs+=("$failed_output")
    if docker_cmd run --rm \
        --mount "type=bind,source=${bind_dir},target=/app/data" \
        "$image" >"$failed_output" 2>&1; then
        fail "$label unexpectedly accepted an unprepared root-owned bind mount"
    fi
    grep -q 'not writable' "$failed_output" || fail "$label did not explain the bind-mount ownership failure"

    chown -R 10001:10001 "$bind_dir"
    containers+=("$container")
    docker_cmd run --detach --name "$container" \
        --read-only \
        --health-interval 1s --health-timeout 3s --health-retries 30 --health-start-period 1s \
        --tmpfs /tmp:rw,noexec,nosuid,nodev,size=192m,mode=1777 \
        --cap-drop ALL \
        --security-opt no-new-privileges:true \
        --mount "type=bind,source=${bind_dir},target=/app/data" \
        --publish 127.0.0.1::8080 \
        --env "OCTOPUS_INITIAL_ADMIN_PASSWORD=${TEST_PASSWORD}" \
        "$image" >/dev/null
    assert_runtime_hardening "$container" "10001:10001"
    [ "$(stat -c '%u:%g' "$bind_dir")" = "10001:10001" ] || fail "$label documented host ownership repair did not persist"
    stop_cleanly "$container"
    docker_cmd rm "$container" >/dev/null

    echo "container matrix: $label root-owned empty bind rejection and owner repair passed"
}

run_compose_case() {
    local project="${RUN_ID}-compose"
    local container

    compose_projects+=("$project")
    OCTOPUS_IMAGE="$DEBIAN_IMAGE" \
        OCTOPUS_BIND_ADDRESS=127.0.0.1 \
        OCTOPUS_HOST_PORT=0 \
        docker_cmd compose --project-name "$project" config --quiet
    OCTOPUS_IMAGE="$DEBIAN_IMAGE" \
        OCTOPUS_BIND_ADDRESS=127.0.0.1 \
        OCTOPUS_HOST_PORT=0 \
        docker_cmd compose --project-name "$project" up --detach --wait --wait-timeout 120

    container="$(docker_cmd compose --project-name "$project" ps --quiet octopus)"
    [ -n "$container" ] || fail "Compose did not create the octopus service"
    containers+=("$container")
    assert_runtime_hardening "$container" "10001:10001"

    docker_cmd compose --project-name "$project" stop --timeout 30 >/dev/null
    [ "$(docker_cmd inspect --format '{{.State.ExitCode}}' "$container")" = 0 ] || fail "Compose service did not stop cleanly"
    docker_cmd compose --project-name "$project" down --volumes --remove-orphans >/dev/null
    echo "container matrix: Compose isolation and hardening passed"
}

main() {
    docker_cmd version >/dev/null
    run_named_volume_case "$ALPINE_IMAGE" alpine
    run_bind_mount_case "$ALPINE_IMAGE" alpine
    run_root_owned_bind_recovery_case "$ALPINE_IMAGE" alpine
    run_named_volume_case "$DEBIAN_IMAGE" debian
    run_bind_mount_case "$DEBIAN_IMAGE" debian
    run_root_owned_bind_recovery_case "$DEBIAN_IMAGE" debian
    run_compose_case
    echo "container matrix: all cases passed"
}

main "$@"
