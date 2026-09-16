#!/bin/sh
set -eu

controller=''
role=''
token=''
version=''
release_base_url=''
uninstall='false'
purge='false'

while [ "$#" -gt 0 ]; do
  case "$1" in
    --controller) controller=$2; shift 2 ;;
    --role) role=$2; shift 2 ;;
    --enrollment-token) token=$2; shift 2 ;;
    --version) version=$2; shift 2 ;;
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
if [ -e /etc/systemd/system/xmesh-controller.service ]; then echo 'a Controller service already uses /usr/local/bin/xmesh; install nodes on separate hosts' >&2; exit 1; fi

if [ "$uninstall" = 'true' ]; then
  systemctl disable --now xmesh.service 2>/dev/null || true
  rm -f /etc/systemd/system/xmesh.service /usr/local/bin/xmesh /usr/local/lib/xmesh/xray
  systemctl daemon-reload
  if [ "$purge" = 'true' ]; then rm -rf /etc/xmesh /var/lib/xmesh; fi
  echo 'xmesh removed; identity and state were preserved unless --purge was supplied'
  exit 0
fi

case "$role" in gateway|agent) ;; *) echo '--role must be gateway or agent' >&2; exit 2;; esac
if [ -z "$controller" ] || [ -z "$version" ] || [ -z "$release_base_url" ]; then echo '--controller, --version, and --release-base-url are required' >&2; exit 2; fi
case "$controller" in https://*) ;; *) echo '--controller must use HTTPS' >&2; exit 2;; esac
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
tar -xzf "$work/$archive" -C "$work"
"$work/xmesh" version >/dev/null
if [ "$role" = gateway ] && [ ! -x "$work/xray" ]; then echo 'gateway release does not contain the pinned Xray binary' >&2; exit 1; fi

getent group xmesh >/dev/null 2>&1 || groupadd --system xmesh
id xmesh >/dev/null 2>&1 || useradd --system --gid xmesh --home-dir /var/lib/xmesh --shell /usr/sbin/nologin xmesh
install -d -m 0750 -o xmesh -g xmesh /etc/xmesh /var/lib/xmesh /usr/local/lib/xmesh
if [ ! -f /etc/xmesh/node.json ]; then
  if [ -z "$token" ]; then
    if [ ! -r /dev/tty ]; then echo 'a terminal or --enrollment-token is required for a new node' >&2; exit 2; fi
    printf 'One-time enrollment token: ' >/dev/tty
    old_stty=$(stty -g </dev/tty)
    trap 'stty "$old_stty" </dev/tty; rm -rf "$work"' EXIT HUP INT TERM
    stty -echo </dev/tty
    IFS= read -r token </dev/tty
    stty "$old_stty" </dev/tty
    trap 'rm -rf "$work"' EXIT HUP INT TERM
    printf '\n' >/dev/tty
  fi
  printf '%s' "$token" | "$work/xmesh" enroll --controller "$controller" --role "$role" --token-stdin --output /etc/xmesh/node.json
  chown xmesh:xmesh /etc/xmesh/node.json
  chmod 0600 /etc/xmesh/node.json
else
  existing_role=$(sed -n 's/.*"role"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' /etc/xmesh/node.json | head -n 1)
  if [ "$existing_role" != "$role" ]; then echo "existing identity is $existing_role, refusing to rebind as $role" >&2; exit 1; fi
fi

if [ -x /usr/local/bin/xmesh ]; then cp -p /usr/local/bin/xmesh "$work/xmesh.previous"; fi
if [ -x /usr/local/lib/xmesh/xray ]; then cp -p /usr/local/lib/xmesh/xray "$work/xray.previous"; fi
install -m 0755 "$work/xmesh" /usr/local/bin/xmesh.new
mv -f /usr/local/bin/xmesh.new /usr/local/bin/xmesh
if [ "$role" = gateway ]; then install -m 0755 "$work/xray" /usr/local/lib/xmesh/xray; fi

cat >/etc/systemd/system/xmesh.service <<EOF
[Unit]
Description=xmesh ${role}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=xmesh
Group=xmesh
ExecStart=/usr/local/bin/xmesh ${role} -config /etc/xmesh/node.json
Restart=on-failure
RestartSec=3s
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/xmesh
AmbientCapabilities=
CapabilityBoundingSet=

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
if ! systemctl enable --now xmesh.service || ! systemctl --no-pager --full status xmesh.service; then
  journalctl -u xmesh.service -n 80 --no-pager || true
  if [ -x "$work/xmesh.previous" ]; then install -m 0755 "$work/xmesh.previous" /usr/local/bin/xmesh; fi
  if [ -x "$work/xray.previous" ]; then install -m 0755 "$work/xray.previous" /usr/local/lib/xmesh/xray; fi
  systemctl restart xmesh.service 2>/dev/null || true
  echo 'installation failed; previous binaries were restored when available' >&2
  exit 1
fi
echo 'xmesh installed; logs: journalctl -u xmesh.service -f'
