#!/bin/sh
set -eu

readonly APP_DIR="/app"
readonly DATA_DIR="${APP_DIR}/data"
readonly DEFAULT_UID="10001"
readonly DEFAULT_GID="10001"

fail() {
    echo "octopus container: $*" >&2
    exit 1
}

validate_id() {
    label="$1"
    value="$2"
    case "$value" in
        ''|*[!0-9]*) fail "$label must be a non-negative integer, got '$value'" ;;
    esac
}

check_data_directory() {
    expected_uid="$1"
    expected_gid="$2"

    if [ ! -d "$DATA_DIR" ]; then
        fail "$DATA_DIR is missing; mount a writable named volume or prepared bind directory"
    fi

    if [ ! -w "$DATA_DIR" ]; then
        fail "$DATA_DIR is not writable by UID:GID ${expected_uid}:${expected_gid}; use a named volume or run 'chown -R ${expected_uid}:${expected_gid} HOST_DATA_DIR' once on the host"
    fi
}

umask 077
current_uid="$(id -u)"
current_gid="$(id -g)"

if [ "$current_uid" -ne 0 ]; then
    if [ "${PUID+x}" = x ] && [ "$PUID" != "$current_uid" ]; then
        fail "PUID=$PUID cannot be applied by non-root UID $current_uid; use Docker's --user option and prepare the data directory ownership"
    fi
    if [ "${PGID+x}" = x ] && [ "$PGID" != "$current_gid" ]; then
        fail "PGID=$PGID cannot be applied by non-root GID $current_gid; use Docker's --user option and prepare the data directory ownership"
    fi

    check_data_directory "$current_uid" "$current_gid"
    cd "$APP_DIR"
    exec "$APP_DIR/octopus" start "$@"
fi

# Root execution is opt-in (for example, --user 0). Preserve the historical
# PUID/PGID escape hatch, but only adjust the mount point itself. Recursively
# changing /app on every boot is both slow and unsafe for large bind mounts.
PUID="${PUID:-$DEFAULT_UID}"
PGID="${PGID:-$DEFAULT_GID}"
validate_id PUID "$PUID"
validate_id PGID "$PGID"

if [ ! -d "$DATA_DIR" ]; then
    mkdir -p "$DATA_DIR"
fi

owner="$(stat -c '%u:%g' "$DATA_DIR")"
if [ "$owner" != "$PUID:$PGID" ]; then
    chown "$PUID:$PGID" "$DATA_DIR"
fi

cd "$APP_DIR"
if command -v su-exec >/dev/null 2>&1; then
    if ! su-exec "$PUID:$PGID" sh -c 'test -w "$1"' sh "$DATA_DIR"; then
        fail "$DATA_DIR is not writable by UID:GID $PUID:$PGID; fix ownership of existing bind-mount contents on the host"
    fi
    exec su-exec "$PUID:$PGID" "$APP_DIR/octopus" start "$@"
elif command -v gosu >/dev/null 2>&1; then
    if ! gosu "$PUID:$PGID" sh -c 'test -w "$1"' sh "$DATA_DIR"; then
        fail "$DATA_DIR is not writable by UID:GID $PUID:$PGID; fix ownership of existing bind-mount contents on the host"
    fi
    exec gosu "$PUID:$PGID" "$APP_DIR/octopus" start "$@"
fi

fail "root mode requires su-exec or gosu so the service can drop privileges"
