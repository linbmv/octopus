#!/bin/bash

# Exit on errors, unset variables, and failed pipeline elements.
set -euo pipefail

# Enable error trapping
trap 'handle_error $? $LINENO' ERR

# =============================================================================
# Configuration
# =============================================================================

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${REPOSITORY_ROOT}" || {
    printf 'Failed to enter repository root: %s\n' "${REPOSITORY_ROOT}" >&2
    exit 1
}

# Project configuration
readonly APP_NAME="octopus"
readonly MAIN_DIR="./"
readonly OUTPUT_DIR="build"
readonly EXPECTED_RELEASE_ARCHIVES=(
    "octopus-darwin-arm64.zip"
    "octopus-darwin-x86_64.zip"
    "octopus-linux-arm64.zip"
    "octopus-linux-armv7.zip"
    "octopus-linux-x86.zip"
    "octopus-linux-x86_64.zip"
    "octopus-windows-x86.zip"
    "octopus-windows-x86_64.zip"
)
readonly EXPECTED_RELEASE_TARGETS="${#EXPECTED_RELEASE_ARCHIVES[@]}"

# Build metadata
readonly COMMIT_ID="$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')"
readonly BUILD_TIME="$(TZ='Asia/Shanghai' date +'%F %T %z')"
readonly GIT_AUTHOR="linbmv"
readonly BASE_GIT_VERSION="$(git describe --tags --exact-match 2>/dev/null || echo "dev-${COMMIT_ID}")"
readonly GIT_DIRTY_SUFFIX="$([ -z "$(git status --porcelain --untracked-files=normal 2>/dev/null)" ] || printf '%s' '-dirty')"
readonly GIT_VERSION="${BASE_GIT_VERSION}${GIT_DIRTY_SUFFIX}"

# Build flags
readonly LDFLAGS="-X 'github.com/bestruirui/octopus/internal/conf.Version=${GIT_VERSION}' \
                  -X 'github.com/bestruirui/octopus/internal/conf.BuildTime=${BUILD_TIME}' \
                  -X 'github.com/bestruirui/octopus/internal/conf.Author=${GIT_AUTHOR}' \
                  -X 'github.com/bestruirui/octopus/internal/conf.Commit=${COMMIT_ID}' \
                  -s -w"

# =============================================================================
# Utility Functions
# =============================================================================

log_info() {
    echo "ℹ️  $1"
}

log_success() {
    echo "✅ $1"
}

log_error() {
    echo "❌ $1" >&2
}

log_warning() {
    echo "⚠️  $1" >&2
}

log_step() {
    echo ""
    echo "🔧 $1"
    echo "────────────────────────────────────────"
}

# Error handling function
handle_error() {
    local exit_code=$1
    local line_number=$2
    log_error "Build failed at line ${line_number} with exit code ${exit_code}"
    log_error "Command that failed: $(sed -n "${line_number}p" "$0" | xargs)"
    log_error "Check the output above for more details"
    exit $exit_code
}

# =============================================================================
# Setup Functions
# =============================================================================
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

prepare_environment() {
    log_step "Preparing build environment"

    # Check and install required commands
    log_info "Checking required commands..."

    # Check Go
    if ! command_exists go; then
        log_error "Go is not installed. Please install Go from https://golang.org/dl/"
        return 1
    fi

    local go_version=$(go version 2>/dev/null | grep -o 'go[0-9]\+\.[0-9]\+' | head -1)
    log_success "Go version: $go_version"

    # Check Node.js
    if ! command_exists node; then
        log_error "Node.js is not installed. Please install Node.js from https://nodejs.org/"
        return 1
    fi

    local node_version=$(node --version 2>/dev/null)
    log_success "Node.js version: $node_version"

    # Check pnpm
    if ! command_exists pnpm; then
        log_error "pnpm is not installed. Please install pnpm: npm install -g pnpm"
        return 1
    fi

    local pnpm_version=$(pnpm --version 2>/dev/null)
    log_success "pnpm version: $pnpm_version"

    # Check git
    if ! command_exists git; then
        log_error "git is not installed."
        return 1
    fi

    # Check zip
    if ! command_exists zip; then
        log_error "zip is not installed."
        return 1
    fi

    # Check SHA-256 implementation.
    if ! command_exists sha256sum && ! command_exists shasum; then
        log_error "sha256sum or shasum is not installed."
        return 1
    fi

    log_success "All required commands installed"

    # Create output directory and subdirectories
    log_info "Creating output directory structure: ${OUTPUT_DIR}"

    # Never follow a caller-controlled top-level symlink while resetting release
    # subdirectories later in the build.
    if [ -L "${OUTPUT_DIR}" ]; then
        log_error "Refusing to use a symbolic link as the output directory: ${OUTPUT_DIR}"
        return 1
    elif [ -e "${OUTPUT_DIR}" ]; then
        if [ -d "${OUTPUT_DIR}" ]; then
            log_success "Output directory already exists: ${OUTPUT_DIR}"
        else
            log_error "Output path exists but is not a directory: ${OUTPUT_DIR}"
            log_error "Path type: $(ls -la "${OUTPUT_DIR}" 2>/dev/null || echo 'Cannot determine type')"
            return 1
        fi
    else
        # Try to create the directory
        if ! mkdir -p "${OUTPUT_DIR}"; then
            log_error "Failed to create output directory: ${OUTPUT_DIR}"
            log_error "Current working directory: $(pwd)"
            log_error "Directory permissions: $(ls -la . 2>/dev/null || echo 'Cannot list directory')"
            return 1
        fi
        log_success "Created output directory: ${OUTPUT_DIR}"
    fi

    # Create subdirectories for organized output
    local subdirs=("bin" "docker" "archives")
    for subdir in "${subdirs[@]}"; do
        if ! mkdir -p "${OUTPUT_DIR}/${subdir}"; then
            log_error "Failed to create subdirectory: ${OUTPUT_DIR}/${subdir}"
            return 1
        fi
    done
    log_success "Created output subdirectories: bin, docker, archives"

    log_info "Downloading and verifying locked Go modules..."
    if ! go mod download; then
        log_error "Failed to download Go modules"
        return 1
    fi
    if ! go mod verify; then
        log_error "Failed to verify Go modules"
        return 1
    fi

    log_success "Build environment ready"
}

reset_release_outputs() {
    log_step "Resetting release output directories"

    # A release must be derived only from this invocation. Keeping binaries or
    # archives from an older target set can silently publish stale artifacts.
    if [ "${OUTPUT_DIR}" != "build" ]; then
        log_error "Refusing to reset an unexpected output directory: ${OUTPUT_DIR}"
        return 1
    fi

    rm -rf -- "${OUTPUT_DIR}/bin" "${OUTPUT_DIR}/docker" "${OUTPUT_DIR}/archives"
    mkdir -p "${OUTPUT_DIR}/bin" "${OUTPUT_DIR}/docker" "${OUTPUT_DIR}/archives"
    log_success "Release output directories are clean"
}

# =============================================================================
# Build Functions
# =============================================================================

build_frontend() {
    log_step "Building frontend"

    local web_dir="web"

    # Check if web directory exists
    if [ ! -d "$web_dir" ]; then
        log_error "Web directory not found: $web_dir"
        log_error "Please run this script from the project root directory"
        return 1
    fi

    # Change to web directory
    cd "$web_dir" || return 1

    # Install dependencies
    log_info "Installing frontend dependencies..."
    if ! pnpm install --frozen-lockfile; then
        log_error "Failed to install frontend dependencies"
        cd ..
        return 1
    fi
    log_success "Frontend dependencies installed"

    # Build the project
    log_info "Building frontend project..."
    if ! NEXT_PUBLIC_APP_VERSION="$GIT_VERSION" pnpm run build; then
        log_error "Failed to build frontend project"
        cd ..
        return 1
    fi
    log_success "Frontend build completed"

    # Return to original directory
    cd ..

    # Move out directory to static directory
    log_info "Moving frontend output to static directory..."
    
    # Replace generated files while preserving the tracked placeholder. This
    # keeps a clean tagged worktree clean after the release build.
    if [ -L "static/out" ]; then
        log_error "Refusing to replace a symbolic-link static/out directory"
        return 1
    fi
    mkdir -p "static/out"
    find "static/out" -mindepth 1 -maxdepth 1 ! -name README.md -exec rm -rf -- {} +

    # Move web/out to static/out
    if [ -d "${web_dir}/out" ]; then
        cp -a "${web_dir}/out/." "static/out/"
        rm -rf "${web_dir}/out"
        log_success "Copied frontend output to static/out"
    else
        log_error "Frontend output directory not found: ${web_dir}/out"
        return 1
    fi

    return 0
}

get_go_arch() {
    case "$1" in
    "x86_64") echo "amd64" ;;
    "arm64") echo "arm64" ;;
    "x86") echo "386" ;;
    "armv7") echo "arm" ;;
    *)
        log_error "Unsupported architecture: $1"
        return 1
        ;;
    esac
}

build_standard() {
    local os="$1"
    local arch="$2"
    local go_arch

    if ! go_arch="$(get_go_arch "${arch}")"; then
        log_error "Failed to get Go architecture: ${arch}"
        return 1
    fi

    local output_file="${OUTPUT_DIR}/bin/${APP_NAME}-${os}-${arch}"

    log_info "Building ${os}/${arch}..."

    if ! GOOS="${os}" GOARCH="${go_arch}" CGO_ENABLED=0 \
        go build -o "${output_file}" -ldflags="${LDFLAGS}" -tags=jsoniter "${MAIN_DIR}" 2>&1; then
        log_error "Failed to build ${os}/${arch}"
        log_error "Build command: GOOS=${os} GOARCH=${go_arch} CGO_ENABLED=0 go build -o ${output_file} -ldflags=\"${LDFLAGS}\" -tags=jsoniter ${MAIN_DIR}"
        return 1
    fi

    if [ ! -f "${output_file}" ]; then
        log_error "Build completed but output file not found: ${output_file}"
        return 1
    fi

    log_success "Built ${os}/${arch} → bin/$(basename "${output_file}")"
}

# =============================================================================
# Post-build Functions
# =============================================================================

create_archives() {
    log_step "Creating distribution archives"

    local archives_dir="${OUTPUT_DIR}/archives"
    local failed=0
    local archive_count=0

    # Copy documentation files to archives directory
    if ! cp README.md LICENSE "${archives_dir}/"; then
        log_error "Failed to copy README.md and LICENSE into the archives"
        return 1
    fi

    # Archive all binaries (zip format for all platforms)
    while IFS= read -r -d '' file; do
        local basename_file
        basename_file=$(basename "$file")
        local extension=""

        # Add .exe extension for Windows binaries
        if [[ "$basename_file" == *"-windows-"* ]]; then
            extension=".exe"
        fi

        if ! cp "$file" "${archives_dir}/${APP_NAME}${extension}" 2>/dev/null; then
            log_error "Failed to copy $file to ${archives_dir}/${APP_NAME}${extension}"
            failed=1
            continue
        fi

        if (cd "${archives_dir}" && zip -q "${basename_file}.zip" "${APP_NAME}${extension}" README.md LICENSE 2>/dev/null); then
            rm -f "${archives_dir}/${APP_NAME}${extension}"
            archive_count=$((archive_count + 1))
            log_success "Archived: archives/${basename_file}.zip"
        else
            log_error "Failed to create archive: ${basename_file}.zip"
            rm -f "${archives_dir}/${APP_NAME}${extension}"
            failed=1
        fi
    done < <(find "${OUTPUT_DIR}/bin/" -name "${APP_NAME}-*" -type f -print0 2>/dev/null)

    # Cleanup documentation files from archives directory
    rm -f "${archives_dir}/README.md" "${archives_dir}/LICENSE"

    if [ "$failed" -ne 0 ] || [ "$archive_count" -ne "$EXPECTED_RELEASE_TARGETS" ]; then
        log_error "Archive creation incomplete (${archive_count}/${EXPECTED_RELEASE_TARGETS})"
        return 1
    fi

    local expected_archive
    for expected_archive in "${EXPECTED_RELEASE_ARCHIVES[@]}"; do
        if [ ! -f "${archives_dir}/${expected_archive}" ]; then
            log_error "Expected release archive is missing: ${expected_archive}"
            return 1
        fi
    done

    log_success "Created ${archive_count} archives in ${archives_dir}/"
}

generate_checksums() {
    log_step "Generating checksums"

    local archives_dir="${OUTPUT_DIR}/archives"
    local archive_count
    archive_count=$(find "${archives_dir}" -maxdepth 1 -name '*.zip' -type f | wc -l)
    if [ "$archive_count" -ne "$EXPECTED_RELEASE_TARGETS" ]; then
        log_error "Expected ${EXPECTED_RELEASE_TARGETS} archives, found ${archive_count}"
        return 1
    fi

    local expected_archive
    for expected_archive in "${EXPECTED_RELEASE_ARCHIVES[@]}"; do
        if [ ! -f "${archives_dir}/${expected_archive}" ]; then
            log_error "Expected release archive is missing: ${expected_archive}"
            return 1
        fi
    done

    if command_exists sha256sum; then
        if ! (cd "${archives_dir}" && sha256sum "${EXPECTED_RELEASE_ARCHIVES[@]}" >SHA256SUMS); then
            log_error "Failed to generate SHA-256 checksums"
            return 1
        fi
        if ! (cd "${archives_dir}" && sha256sum --check --strict SHA256SUMS >/dev/null); then
            log_error "Generated SHA-256 checksums failed validation"
            return 1
        fi
    elif command_exists shasum; then
        if ! (cd "${archives_dir}" && shasum -a 256 "${EXPECTED_RELEASE_ARCHIVES[@]}" >SHA256SUMS); then
            log_error "Failed to generate SHA-256 checksums"
            return 1
        fi
        if ! (cd "${archives_dir}" && shasum -a 256 --check SHA256SUMS >/dev/null); then
            log_error "Generated SHA-256 checksums failed validation"
            return 1
        fi
    else
        log_error "No SHA-256 command available"
        return 1
    fi

    log_success "Generated SHA256SUMS for ${archive_count} archives"
}

prepare_docker_binaries() {
    log_step "Preparing Docker binaries"

    local docker_dir="${OUTPUT_DIR}/docker"

    # Create docker directory under OUTPUT_DIR
    if ! mkdir -p "${docker_dir}"; then
        log_error "Failed to create docker directory: ${docker_dir}"
        log_error "Current working directory: $(pwd)"
        log_error "Directory permissions: $(ls -la . 2>/dev/null || echo 'Cannot list directory')"
        return 1
    fi

    local platforms=(
        "x86_64:linux/amd64"
        "x86:linux/386"
        "armv7:linux/arm/v7"
        "arm64:linux/arm64"
    )

    local copied_count=0
    local failed=0

    for platform in "${platforms[@]}"; do
        local arch="${platform%%:*}"
        local docker_platform="${platform#*:}"
        local binary_name="${APP_NAME}-linux-${arch}"
        local platform_dir="${docker_dir}/${docker_platform}"

        if ! mkdir -p "${platform_dir}"; then
            log_error "Failed to create directory: ${platform_dir}"
            log_error "Docker platform: ${docker_platform}"
            failed=1
            continue
        fi

        # Try to copy from binary file first
        if [ -f "${OUTPUT_DIR}/bin/${binary_name}" ]; then
            if cp "${OUTPUT_DIR}/bin/${binary_name}" "${platform_dir}/${APP_NAME}" 2>/dev/null; then
                log_success "Copied bin/${binary_name} → docker/${docker_platform}/${APP_NAME}"
                copied_count=$((copied_count + 1))
            else
                log_error "Failed to copy bin/${binary_name} to ${platform_dir}/${APP_NAME}"
                failed=1
            fi
        else
            log_error "Binary not found: bin/${binary_name}"
            failed=1
        fi
    done

    if [ "$failed" -ne 0 ] || [ "$copied_count" -ne "${#platforms[@]}" ]; then
        log_error "Docker binary preparation incomplete (${copied_count}/${#platforms[@]})"
        return 1
    fi
    log_success "Prepared ${copied_count} Docker binaries in ${docker_dir}/"
}

# =============================================================================
# Main Execution
# =============================================================================

show_usage() {
    echo "Usage: $0 <command> [os] [arch]"
    echo ""
    echo "Commands:"
    echo "  release              Build all platforms and create distribution packages"
    echo "  build <os> <arch>    Build for specific OS and architecture"
    echo "  help                 Show this help message"
    echo ""
    echo "Supported OS:"
    echo "  linux, windows, darwin, android"
    echo ""
    echo "Supported architectures:"
    echo "  x86_64, arm64, armv7, x86"
    echo ""
    echo "Examples:"
    echo "  $0 build windows x86_64"
    echo "  $0 build linux x86_64"
    echo "  $0 build android arm64"
    echo "  $0 release"
    echo "  $0 version"
}

validate_os_arch() {
    local os="$1"
    local arch="$2"

    # Validate OS
    case "$os" in
    "linux" | "windows" | "darwin" | "android") ;;
    *)
        log_error "Unsupported OS: $os"
        log_error "Supported OS: linux, windows, darwin, android"
        return 1
        ;;
    esac

    # Validate architecture
    case "$arch" in
    "x86_64" | "arm64" | "armv7" | "x86") ;;
    *)
        log_error "Unsupported architecture: $arch"
        log_error "Supported architectures: x86_64, arm64, armv7, x86"
        return 1
        ;;
    esac

    return 0
}

main() {
    case "${1:-}" in
    "build")
        if [ $# -ne 3 ]; then
            log_error "Build command requires OS and architecture"
            log_error "Usage: $0 build <os> <arch>"
            show_usage
            exit 1
        fi

        local os="$2"
        local arch="$3"

        if ! validate_os_arch "$os" "$arch"; then
            exit 1
        fi

        log_step "Starting single platform build"
        echo "📦 Building ${APP_NAME} ${GIT_VERSION} (${COMMIT_ID}) for ${os}/${arch}"
        echo ""

        # Setup
        if ! prepare_environment; then
            log_error "Failed to prepare build environment"
            exit 1
        fi

        # Build frontend
        if ! build_frontend; then
            log_error "Failed to build frontend"
            exit 1
        fi

        # Build for specified platform
        log_step "Building binary"

        if ! build_standard "$os" "$arch"; then
            log_error "Failed to build ${os}/${arch}"
            exit 1
        fi

        log_step "Build completed"
        log_success "Binary ready: ${OUTPUT_DIR}/bin/${APP_NAME}-${os}-${arch}"
        ;;
    "release")
        log_step "Starting release build"
        echo "📦 Building ${APP_NAME} ${GIT_VERSION} (${COMMIT_ID})"
        echo ""

        # Setup
        if ! prepare_environment; then
            log_error "Failed to prepare build environment"
            exit 1
        fi

        if ! reset_release_outputs; then
            log_error "Failed to reset release outputs"
            exit 1
        fi

        # Build frontend
        if ! build_frontend; then
            log_error "Failed to build frontend"
            exit 1
        fi

        # Build for different platforms
        log_step "Building binaries"

        local targets=(
            "linux:x86_64"
            "linux:arm64"
            "linux:armv7"
            "linux:x86"
            "windows:x86_64"
            "windows:x86"
            "darwin:arm64"
            "darwin:x86_64"
        )
        local build_failed=0
        local target
        for target in "${targets[@]}"; do
            local target_os="${target%%:*}"
            local target_arch="${target#*:}"
            if ! build_standard "$target_os" "$target_arch"; then
                build_failed=1
            fi
        done
        if [ "$build_failed" -ne 0 ]; then
            log_error "One or more platform builds failed"
            exit 1
        fi

        # Post-processing
        if ! prepare_docker_binaries; then
            log_error "Failed to prepare Docker binaries"
            exit 1
        fi

        if ! create_archives; then
            log_error "Failed to create archives"
            exit 1
        fi

        if ! generate_checksums; then
            log_error "Failed to generate checksums"
            exit 1
        fi

        log_step "Build completed"
        log_success "All artifacts ready in ${OUTPUT_DIR}/"
        log_info "  • Binaries: ${OUTPUT_DIR}/bin/"
        log_info "  • Docker binaries: ${OUTPUT_DIR}/docker/"
        log_info "  • Archives: ${OUTPUT_DIR}/archives/"
        ;;
    "version")
        printf '%s\n' "${GIT_VERSION}"
        ;;
    "help" | "-h" | "--help")
        show_usage
        ;;
    "")
        log_error "No command specified"
        show_usage
        exit 1
        ;;
    *)
        log_error "Unknown command: $1"
        show_usage
        exit 1
        ;;
    esac
}

main "$@"
