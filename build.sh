#!/bin/bash
# Build every packaged executable and its checksum before replacing outputs.

set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"

if command -v sha256sum >/dev/null 2>&1; then
    checksum=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
    checksum=(shasum -a 256)
else
    echo "A SHA256 utility (sha256sum or shasum) is required." >&2
    exit 1
fi

mkdir -p bin
# Staging inside the working tree makes later targets look dirty to Go's VCS
# stamping even when the source checkout is clean. Git's metadata directory is
# excluded from that check, including when this script runs in a linked worktree.
build_dir=$(mktemp -d "$(git rev-parse --git-path padlock-build).XXXXXX")
trap 'rm -rf -- "$build_dir"' EXIT

version=$(git describe --tags --always --dirty)
echo "Building Padlock $version with $(go version)..."

build_target() {
    local platform=$1 target_os=$2 target_arch=$3 executable=padlock
    if [[ "$target_os" == windows ]]; then
        executable=padlock.exe
    fi
    mkdir -p "$build_dir/$platform"
    echo "Building $platform..."
    CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" GOARM=7 \
        GOAMD64=v1 GOARM64=v8.0 go build -trimpath -buildvcs=true \
        -o "$build_dir/$platform/$executable" ./cmd/padlock
    chmod 755 "$build_dir/$platform/$executable"
    (cd "$build_dir/$platform" && "${checksum[@]}" "$executable" > "$executable.sha256.txt")
}

build_target macos-arm64 darwin arm64
build_target macos-amd64 darwin amd64
build_target windows-arm64 windows arm64
build_target windows-amd64 windows amd64
build_target linux-arm64 linux arm64
build_target linux-amd64 linux amd64
build_target linux-armv7 linux arm

# Preserve the existing macOS ARM64 convenience executable, using the same build.
cp "$build_dir/macos-arm64/padlock" "$build_dir/padlock"
(cd "$build_dir" && "${checksum[@]}" padlock > padlock.sha256.txt)

# Compilation or checksum failures above leave all existing packages intact.
for platform in macos-arm64 macos-amd64 windows-arm64 windows-amd64 linux-arm64 linux-amd64 linux-armv7; do
    mkdir -p "bin/$platform"
    mv -- "$build_dir/$platform/"* "bin/$platform/"
done
mv -- "$build_dir/padlock" "$build_dir/padlock.sha256.txt" bin/
echo "Packaged executables and SHA256 files updated in bin/."
