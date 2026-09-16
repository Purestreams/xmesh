#!/bin/sh
set -eu

version=${1:?usage: scripts/package.sh VERSION}
case "$version" in *[!a-zA-Z0-9._-]*|'') echo 'version must contain only letters, numbers, dots, underscores, or hyphens' >&2; exit 2;; esac
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
. "$root/versions.env"
case "$(go env GOVERSION)" in go1.27.1) ;; *) echo 'packaging requires Go 1.27.1' >&2; exit 1;; esac
: "${XRAY_AMD64:?set XRAY_AMD64 to the validated linux/amd64 Xray binary}"
: "${XRAY_ARM64:?set XRAY_ARM64 to the validated linux/arm64 Xray binary}"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
mkdir -p "$root/dist"
rm -f "$root/dist"/*.tar.gz "$root/dist"/*.exe "$root/dist/SHA256SUMS" "$root/dist/install.sh" "$root/dist/install-docker.sh" "$root/dist/install-controller.sh"

for arch in amd64 arm64; do
  package="$work/$arch"
  mkdir -p "$package"
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$package/xmesh" ./cmd/xmesh
  case "$arch" in amd64) xray_path=$XRAY_AMD64;; arm64) xray_path=$XRAY_ARM64;; esac
  "$xray_path" version | head -n 1 | grep "Xray $XRAY_VERSION " >/dev/null || { echo "Xray binary for $arch is not pinned version $XRAY_VERSION" >&2; exit 1; }
  install -m 0755 "$xray_path" "$package/xray"
  tar -C "$package" -czf "$root/dist/xmesh-${version}-linux-${arch}.tar.gz" xmesh xray
done
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$root/dist/xmesh-${version}-windows-amd64.exe" ./cmd/xmesh
install -m 0755 "$root/scripts/install.sh" "$root/dist/install.sh"
install -m 0755 "$root/scripts/install-docker.sh" "$root/dist/install-docker.sh"
install -m 0755 "$root/scripts/install-controller.sh" "$root/dist/install-controller.sh"
(cd "$root/dist" && sha256sum *.tar.gz *.exe install.sh install-docker.sh install-controller.sh >SHA256SUMS)
echo "release assets written to $root/dist"
