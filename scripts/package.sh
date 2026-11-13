#!/bin/sh
set -eu

version=${1:?usage: scripts/package.sh VERSION}
case "$version" in *[!a-zA-Z0-9._-]*|'') echo 'version must contain only letters, numbers, dots, underscores, or hyphens' >&2; exit 2;; esac
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
. "$root/versions.env"
if [ "$(go env GOVERSION)" != "go$GO_VERSION" ]; then echo "packaging requires Go $GO_VERSION" >&2; exit 1; fi
: "${XRAY_AMD64:?set XRAY_AMD64 to the validated linux/amd64 Xray binary}"
: "${XRAY_ARM64:?set XRAY_ARM64 to the validated linux/arm64 Xray binary}"
case "$(uname -m)" in x86_64|amd64) host_arch=amd64;; aarch64|arm64) host_arch=arm64;; *) host_arch=unknown;; esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
mkdir -p "$root/dist"
rm -f "$root/dist"/*.tar.gz "$root/dist"/*.exe "$root/dist/SHA256SUMS" "$root/dist/install.sh" "$root/dist/install-docker.sh" "$root/dist/install-updater.sh" "$root/dist/install-controller.sh" "$root/dist/backup-controller.sh" "$root/dist/THIRD_PARTY_NOTICES.md"

for arch in amd64 arm64; do
  package="$work/$arch"
  mkdir -p "$package"
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$package/xmesh" ./cmd/xmesh
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$package/xmesh-updater" ./cmd/xmesh-updater
  case "$arch" in amd64) xray_path=$XRAY_AMD64;; arm64) xray_path=$XRAY_ARM64;; esac
  magic=$(od -An -tx1 -N4 "$xray_path" | tr -d '[:space:]')
  [ "$magic" = 7f454c46 ] || { echo "Xray binary for $arch is not an ELF executable" >&2; exit 1; }
  set -- $(od -An -tu1 -j18 -N2 "$xray_path")
  case "$arch:$1:$2" in amd64:62:0|arm64:183:0) ;; *) echo "Xray binary architecture does not match $arch" >&2; exit 1;; esac
  case "$(go version "$xray_path")" in *": go$GO_VERSION") ;; *) echo "Xray binary for $arch must be built with Go $GO_VERSION" >&2; exit 1;; esac
  metadata=$(go version -m "$xray_path" | awk '$1 == "mod" || $1 == "dep" { print $2, $3, $4 }')
  for module in github.com/xtls/xray-core github.com/andybalholm/brotli github.com/klauspost/compress; do
    pinned=$(cd "$root/build/gateway-xray" && go list -m -f '{{.Path}} {{.Version}} {{.Sum}}' "$module")
    if ! printf '%s\n' "$metadata" | grep -Fx "$pinned" >/dev/null; then
      echo "Xray binary for $arch does not match the pinned $module" >&2
      exit 1
    fi
  done
  if [ "$arch" = "$host_arch" ]; then
    "$xray_path" version | head -n 1 | grep "Xray $XRAY_VERSION " >/dev/null || { echo "Xray binary for $arch is not pinned version $XRAY_VERSION" >&2; exit 1; }
  else
    echo "Verified Xray $arch source and build metadata; cannot execute on $host_arch"
  fi
  install -m 0755 "$xray_path" "$package/xray"
  install -m 0755 "$root/scripts/controller-updater.sh" "$package/controller-updater.sh"
  install -m 0644 "$root/THIRD_PARTY_NOTICES.md" "$package/THIRD_PARTY_NOTICES.md"
  tar -C "$package" -czf "$root/dist/xmesh-${version}-linux-${arch}.tar.gz" xmesh xmesh-updater xray controller-updater.sh THIRD_PARTY_NOTICES.md
done
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$root/dist/xmesh-${version}-windows-amd64.exe" ./cmd/xmesh
install -m 0755 "$root/scripts/install.sh" "$root/dist/install.sh"
install -m 0755 "$root/scripts/install-docker.sh" "$root/dist/install-docker.sh"
install -m 0755 "$root/scripts/install-updater.sh" "$root/dist/install-updater.sh"
install -m 0755 "$root/scripts/install-controller.sh" "$root/dist/install-controller.sh"
install -m 0755 "$root/scripts/backup-controller.sh" "$root/dist/backup-controller.sh"
install -m 0644 "$root/THIRD_PARTY_NOTICES.md" "$root/dist/THIRD_PARTY_NOTICES.md"
(cd "$root/dist" && sha256sum *.tar.gz *.exe install.sh install-docker.sh install-updater.sh install-controller.sh backup-controller.sh THIRD_PARTY_NOTICES.md >SHA256SUMS)
echo "release assets written to $root/dist"
