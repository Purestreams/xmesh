#!/bin/sh
set -eu

version=''
public_url=''
listen='127.0.0.1:8088'
admin_username='admin'
release_base_url='https://github.com/Purestreams/xmesh/releases/download'
uninstall='false'
purge='false'

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) version=$2; shift 2 ;;
    --public-url) public_url=$2; shift 2 ;;
    --listen) listen=$2; shift 2 ;;
    --admin-username) admin_username=$2; shift 2 ;;
    --release-base-url) release_base_url=$2; shift 2 ;;
    --uninstall) uninstall='true'; shift ;;
    --purge) purge='true'; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then echo 'run this installer as root' >&2; exit 1; fi
if [ ! -r /etc/os-release ]; then echo 'unsupported system: /etc/os-release missing' >&2; exit 1; fi
. /etc/os-release
case "${ID:-}" in debian|ubuntu) ;; *) echo "unsupported distribution: ${ID:-unknown}" >&2; exit 1;; esac
if [ ! -d /run/systemd/system ]; then echo 'systemd is required' >&2; exit 1; fi
if [ -e /etc/systemd/system/xmesh.service ]; then echo 'a node service already uses /usr/local/bin/xmesh; install the Controller on a separate host' >&2; exit 1; fi

if [ "$uninstall" = 'true' ]; then
  systemctl disable --now xmesh-controller.service 2>/dev/null || true
  rm -f /etc/systemd/system/xmesh-controller.service /usr/local/bin/xmesh
  systemctl daemon-reload
  if [ "$purge" = 'true' ]; then rm -rf /etc/xmesh/controller.json /var/lib/xmesh-controller; fi
  echo 'xmesh Controller removed; configuration and state were preserved unless --purge was supplied'
  exit 0
fi

if [ -z "$version" ]; then echo '--version is required' >&2; exit 2; fi
case "$version" in *[!a-zA-Z0-9._-]*|'') echo 'version must contain only letters, numbers, dots, underscores, or hyphens' >&2; exit 2;; esac
case "$release_base_url" in https://*) ;; *) echo '--release-base-url must use HTTPS' >&2; exit 2;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo "unsupported architecture: $(uname -m)" >&2; exit 1;; esac
archive="xmesh-${version}-linux-${arch}.tar.gz"
base="${release_base_url%/}/${version}"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
curl --fail --location --retry 3 --proto '=https' --proto-redir '=https' --output "$work/$archive" "$base/$archive"
curl --fail --location --retry 3 --proto '=https' --proto-redir '=https' --output "$work/SHA256SUMS" "$base/SHA256SUMS"
(cd "$work" && grep "  $archive\$" SHA256SUMS | sha256sum -c -)
tar -xzf "$work/$archive" -C "$work" xmesh
"$work/xmesh" version >/dev/null

getent group xmesh >/dev/null 2>&1 || groupadd --system xmesh
id xmesh >/dev/null 2>&1 || useradd --system --gid xmesh --home-dir /var/lib/xmesh-controller --shell /usr/sbin/nologin xmesh
install -d -m 0750 -o xmesh -g xmesh /etc/xmesh /var/lib/xmesh-controller

if [ ! -f /etc/xmesh/controller.json ]; then
  if [ -z "$public_url" ]; then echo '--public-url is required for a new installation' >&2; exit 2; fi
  case "$public_url" in https://*) ;; *) echo '--public-url must use HTTPS' >&2; exit 2;; esac
  if [ -z "${XMESH_ADMIN_PASSWORD:-}" ]; then echo 'XMESH_ADMIN_PASSWORD is required for a new installation' >&2; exit 2; fi
  for value in "$public_url" "$listen" "$admin_username" "$release_base_url" "$version"; do
    if printf '%s' "$value" | grep '[\\"]' >/dev/null; then echo 'arguments must not contain backslashes or double quotes' >&2; exit 2; fi
  done
  password_hash=$(XMESH_ADMIN_PASSWORD="$XMESH_ADMIN_PASSWORD" "$work/xmesh" hash-password)
  session_secret=$("$work/xmesh" generate-secret)
  cat >"$work/controller.json" <<EOF
{
  "listen": "$listen",
  "public_url": "${public_url%/}",
  "state_path": "/var/lib/xmesh-controller/controller-state.json",
  "admin_username": "$admin_username",
  "admin_password_hash": "$password_hash",
  "session_secret": "$session_secret",
  "release_base_url": "${release_base_url%/}",
  "release_version": "$version",
  "release_dir": "/var/lib/xmesh-controller/releases",
  "node_offline_after_seconds": 45
}
EOF
  install -m 0600 -o xmesh -g xmesh "$work/controller.json" /etc/xmesh/controller.json
fi

if [ -x /usr/local/bin/xmesh ]; then cp -p /usr/local/bin/xmesh "$work/xmesh.previous"; fi
install -m 0755 "$work/xmesh" /usr/local/bin/xmesh.new
mv -f /usr/local/bin/xmesh.new /usr/local/bin/xmesh
cat >/etc/systemd/system/xmesh-controller.service <<'EOF'
[Unit]
Description=xmesh Controller
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=xmesh
Group=xmesh
ExecStart=/usr/local/bin/xmesh controller -config /etc/xmesh/controller.json
Restart=on-failure
RestartSec=3s
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/xmesh-controller
AmbientCapabilities=
CapabilityBoundingSet=

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
if ! systemctl enable --now xmesh-controller.service || ! systemctl --no-pager --full status xmesh-controller.service; then
  journalctl -u xmesh-controller.service -n 80 --no-pager || true
  if [ -x "$work/xmesh.previous" ]; then install -m 0755 "$work/xmesh.previous" /usr/local/bin/xmesh; fi
  systemctl restart xmesh-controller.service 2>/dev/null || true
  echo 'installation failed; the previous binary was restored when available' >&2
  exit 1
fi
echo 'xmesh Controller installed; logs: journalctl -u xmesh-controller.service -f'
