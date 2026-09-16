#!/bin/sh
set -eu

role=''
version=''
controller=''
token=''
public_url=''
listen='127.0.0.1:8088'
admin_username='admin'
release_base_url='https://github.com/Purestreams/xmesh/releases/download'
install_dir=''

while [ "$#" -gt 0 ]; do
  case "$1" in
    --role) role=$2; shift 2 ;;
    --version) version=$2; shift 2 ;;
    --controller) controller=$2; shift 2 ;;
    --enrollment-token) token=$2; shift 2 ;;
    --public-url) public_url=$2; shift 2 ;;
    --listen) listen=$2; shift 2 ;;
    --admin-username) admin_username=$2; shift 2 ;;
    --release-base-url) release_base_url=$2; shift 2 ;;
    --install-dir) install_dir=$2; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then echo 'run this installer as root' >&2; exit 1; fi
case "$role" in controller|gateway|agent) ;; *) echo '--role must be controller, gateway, or agent' >&2; exit 2;; esac
if [ -z "$version" ]; then echo '--version is required' >&2; exit 2; fi
case "$version" in *[!a-zA-Z0-9._-]*|'') echo 'version must contain only letters, numbers, dots, underscores, or hyphens' >&2; exit 2;; esac
case "$release_base_url" in https://*) ;; *) echo '--release-base-url must use HTTPS' >&2; exit 2;; esac
if ! docker compose version >/dev/null 2>&1; then echo 'Docker with the Compose plugin is required' >&2; exit 1; fi
case "$(uname -s)/$(uname -m)" in Linux/x86_64|Linux/amd64) arch=amd64;; Linux/aarch64|Linux/arm64) arch=arm64;; *) echo 'Docker deployment supports Linux amd64 and arm64 hosts' >&2; exit 1;; esac
if [ -z "$install_dir" ]; then install_dir="/opt/xmesh-docker-$role"; fi
case "$install_dir" in /*) ;; *) echo '--install-dir must be an absolute path' >&2; exit 2;; esac
case "$install_dir" in /|/etc|/opt|/var|/usr) echo '--install-dir must name a dedicated directory' >&2; exit 2;; esac

archive="xmesh-${version}-linux-${arch}.tar.gz"
base="${release_base_url%/}/${version}"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
wget -q --https-only "$base/$archive" -O "$work/$archive"
wget -q --https-only "$base/SHA256SUMS" -O "$work/SHA256SUMS"
(cd "$work" && grep "  $archive\$" SHA256SUMS | sha256sum -c -)
tar -xzf "$work/$archive" -C "$work" xmesh xray
"$work/xmesh" version >/dev/null

install -d -m 0750 "$install_dir" "$install_dir/config" "$install_dir/data" "$install_dir/image"
if [ -f "$install_dir/.role" ] && [ "$(cat "$install_dir/.role")" != "$role" ]; then
  echo "existing Docker installation has role $(cat "$install_dir/.role")" >&2
  exit 1
fi

config_name='node.json'
if [ "$role" = 'controller' ]; then
  config_name='controller.json'
  if [ ! -f "$install_dir/config/$config_name" ]; then
    if [ -z "$public_url" ]; then echo '--public-url is required for a new Controller' >&2; exit 2; fi
    case "$public_url" in https://*) ;; *) echo '--public-url must use HTTPS' >&2; exit 2;; esac
    if [ -z "${XMESH_ADMIN_PASSWORD:-}" ]; then echo 'XMESH_ADMIN_PASSWORD is required for a new Controller' >&2; exit 2; fi
    for value in "$public_url" "$listen" "$admin_username" "$release_base_url" "$version"; do
      if printf '%s' "$value" | grep '[\\"]' >/dev/null; then echo 'arguments must not contain backslashes or double quotes' >&2; exit 2; fi
    done
    password_hash=$(XMESH_ADMIN_PASSWORD="$XMESH_ADMIN_PASSWORD" "$work/xmesh" hash-password)
    session_secret=$("$work/xmesh" generate-secret)
    cat >"$install_dir/config/$config_name" <<EOF
{
  "listen": "$listen",
  "public_url": "${public_url%/}",
  "state_path": "/var/lib/xmesh/controller-state.json",
  "admin_username": "$admin_username",
  "admin_password_hash": "$password_hash",
  "session_secret": "$session_secret",
  "release_base_url": "${release_base_url%/}",
  "release_version": "$version",
  "node_offline_after_seconds": 45
}
EOF
  fi
else
  if [ ! -f "$install_dir/config/$config_name" ]; then
    if [ -z "$controller" ] || [ -z "$token" ]; then echo '--controller and --enrollment-token are required for a new node' >&2; exit 2; fi
    case "$controller" in https://*) ;; *) echo '--controller must use HTTPS' >&2; exit 2;; esac
    "$work/xmesh" enroll --controller "$controller" --role "$role" --token "$token" --output "$install_dir/config/$config_name"
  fi
fi

install -m 0755 "$work/xmesh" "$install_dir/image/xmesh"
install -m 0755 "$work/xray" "$install_dir/image/xray"
cat >"$install_dir/image/Dockerfile" <<'EOF'
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 xmesh \
    && useradd --uid 10001 --gid xmesh --home-dir /var/lib/xmesh --shell /usr/sbin/nologin xmesh
COPY xmesh /usr/local/bin/xmesh
COPY xray /usr/local/lib/xmesh/xray
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/xmesh"]
EOF

cat >"$install_dir/compose.yaml" <<EOF
services:
  xmesh:
    build: ./image
    image: xmesh-local:$version
    network_mode: host
    restart: unless-stopped
    command: ["$role", "-config", "/etc/xmesh/$config_name"]
    volumes:
      - ./config:/etc/xmesh:ro
      - ./data:/var/lib/xmesh
EOF
chown -R 10001:10001 "$install_dir/config" "$install_dir/data"
printf '%s\n' "$role" >"$install_dir/.role"
(cd "$install_dir" && docker compose up -d --build)
echo "xmesh $role is running from $install_dir; logs: cd $install_dir && docker compose logs -f"
