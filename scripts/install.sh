#!/bin/sh
set -eu

controller=''
role=''
token=''
version=''
release_base_url=''
uninstall='false'
purge='false'
rotate_credential='false'
vmess_port='8080'

while [ "$#" -gt 0 ]; do
  case "$1" in
    --controller) controller=$2; shift 2 ;;
    --role) role=$2; shift 2 ;;
    --enrollment-token) token=$2; shift 2 ;;
    --version) version=$2; shift 2 ;;
    --release-base-url) release_base_url=$2; shift 2 ;;
    --uninstall) uninstall='true'; shift ;;
    --purge) purge='true'; shift ;;
    --rotate-credential) rotate_credential='true'; shift ;;
    --vmess-port) vmess_port=$2; shift 2 ;;
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
  systemctl disable --now xmesh-updater.service 2>/dev/null || true
  systemctl disable --now xmesh.service 2>/dev/null || true
  rm -f /etc/systemd/system/xmesh.service /etc/systemd/system/xmesh-updater.service /usr/local/bin/xmesh /usr/local/bin/xmesh-updater /usr/local/lib/xmesh/xray
  systemctl daemon-reload
  if [ "$purge" = 'true' ]; then rm -rf /etc/xmesh /var/lib/xmesh /var/lib/xmesh-updater; fi
  echo 'xmesh removed; identity and state were preserved unless --purge was supplied'
  exit 0
fi

case "$role" in gateway|agent) ;; *) echo '--role must be gateway or agent' >&2; exit 2;; esac
if [ -z "$controller" ] || [ -z "$version" ] || [ -z "$release_base_url" ]; then echo '--controller, --version, and --release-base-url are required' >&2; exit 2; fi
case "$controller" in https://*) ;; *) echo '--controller must use HTTPS' >&2; exit 2;; esac
case "$version" in *[!a-zA-Z0-9._-]*|'') echo 'version must contain only letters, numbers, dots, underscores, or hyphens' >&2; exit 2;; esac
if [ "$role" = gateway ]; then
  case "$vmess_port" in ''|*[!0-9]*) echo '--vmess-port must be a TCP port from 1 to 65535' >&2; exit 2;; esac
  if [ "$vmess_port" -lt 1 ] || [ "$vmess_port" -gt 65535 ]; then echo '--vmess-port must be a TCP port from 1 to 65535' >&2; exit 2; fi
fi
case "$release_base_url" in https://*) ;; *) echo '--release-base-url must use HTTPS' >&2; exit 2;; esac
for tool in curl sha256sum tar mktemp; do
  command -v "$tool" >/dev/null 2>&1 || { echo "missing required tool: $tool" >&2; exit 1; }
done
curl --fail --silent --show-error --max-time 15 "${controller%/}/healthz" >/dev/null || {
  echo "Controller HTTPS health check failed: ${controller%/}/healthz" >&2
  exit 1
}
if [ ! -f /etc/xmesh/node.json ] && command -v ss >/dev/null 2>&1 && [ "$role" = gateway ]; then
  for port in "$vmess_port" 8443; do
    if ss -ltnH | awk '{print $4}' | grep -Eq ":$port\$"; then
      echo "TCP port $port is already in use" >&2
      exit 1
    fi
  done
fi

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
install -d -m 0750 -o root -g xmesh /etc/xmesh
install -d -m 0750 -o xmesh -g xmesh /var/lib/xmesh
install -d -m 0755 -o root -g root /usr/local/lib/xmesh
if [ "$rotate_credential" = true ] && [ ! -f /etc/xmesh/node.json ]; then
  echo '--rotate-credential requires an existing node identity' >&2
  exit 2
fi
if [ ! -f /etc/xmesh/node.json ] || [ "$rotate_credential" = true ]; then
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
  if [ "$rotate_credential" = true ]; then
    printf '%s' "$token" | "$work/xmesh" enroll --controller "$controller" --role "$role" --token-stdin --replace --output /etc/xmesh/node.json --updater-output /etc/xmesh/updater.json --updater-mode systemd
  else
    printf '%s' "$token" | "$work/xmesh" enroll --controller "$controller" --role "$role" --token-stdin --output /etc/xmesh/node.json --updater-output /etc/xmesh/updater.json --updater-mode systemd
  fi
  chown xmesh:xmesh /etc/xmesh/node.json
  chmod 0600 /etc/xmesh/node.json
else
  existing_role=$(sed -n 's/.*"role"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' /etc/xmesh/node.json | head -n 1)
  if [ "$existing_role" != "$role" ]; then echo "existing identity is $existing_role, refusing to rebind as $role" >&2; exit 1; fi
fi

install_updater_service() {
  if [ ! -f /etc/xmesh/updater.json ]; then
    echo 'updater identity is missing; generate a pairing token in the Controller panel' >&2
    return
  fi
  updater_tmp="/usr/local/bin/xmesh-updater.new.$$"
  install -m 0700 "$work/xmesh-updater" "$updater_tmp"
  mv -f "$updater_tmp" /usr/local/bin/xmesh-updater
  cat >/etc/systemd/system/xmesh-updater.service <<'EOF'
[Unit]
Description=xmesh host upgrade assistant
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/xmesh-updater run --config /etc/xmesh/updater.json
Restart=always
RestartSec=10s
ProtectHome=true
NoNewPrivileges=true
ReadWritePaths=/var/lib/xmesh-updater /usr/local/bin /usr/local/lib/xmesh

[Install]
WantedBy=multi-user.target
EOF
  install -d -m 0700 /var/lib/xmesh-updater
  systemctl daemon-reload
  systemctl enable xmesh-updater.service
  systemctl restart xmesh-updater.service
}

if [ -x /usr/local/bin/xmesh ] && [ -f /etc/xmesh/updater.json ]; then
  archive_sha=$(grep "  $archive\$" "$work/SHA256SUMS" | cut -d ' ' -f 1)
  systemctl stop xmesh-updater.service
  if ! "$work/xmesh-updater" local --config /etc/xmesh/updater.json --version "$version" --archive "$work/$archive" --sha256 "$archive_sha"; then
    systemctl restart xmesh-updater.service 2>/dev/null || true
    exit 1
  fi
  install_updater_service
  echo 'xmesh installed; logs: journalctl -u xmesh.service -f'
  exit 0
fi

if [ -x /usr/local/bin/xmesh ]; then cp -p /usr/local/bin/xmesh "$work/xmesh.previous"; fi
if [ -x /usr/local/lib/xmesh/xray ]; then cp -p /usr/local/lib/xmesh/xray "$work/xray.previous"; fi
restore_previous_binaries() {
  restore_failed=false
  if [ -f "$work/xmesh.previous" ]; then
    if ! cp -p "$work/xmesh.previous" /usr/local/bin/xmesh.restore.$$ || ! mv -f /usr/local/bin/xmesh.restore.$$ /usr/local/bin/xmesh; then restore_failed=true; fi
  else
    rm -f /usr/local/bin/xmesh || restore_failed=true
  fi
  if [ "$role" = gateway ]; then
    if [ -f "$work/xray.previous" ]; then
      if ! cp -p "$work/xray.previous" /usr/local/lib/xmesh/xray.restore.$$ || ! mv -f /usr/local/lib/xmesh/xray.restore.$$ /usr/local/lib/xmesh/xray; then restore_failed=true; fi
    else
      rm -f /usr/local/lib/xmesh/xray || restore_failed=true
    fi
  fi
  [ "$restore_failed" = false ]
}
install -m 0755 "$work/xmesh" /usr/local/bin/xmesh.new
if [ "$role" = gateway ]; then install -m 0755 "$work/xray" /usr/local/lib/xmesh/xray.new; fi
if ! mv -f /usr/local/bin/xmesh.new /usr/local/bin/xmesh || { [ "$role" = gateway ] && ! mv -f /usr/local/lib/xmesh/xray.new /usr/local/lib/xmesh/xray; }; then
  restore_previous_binaries || echo 'restoring previous binaries also failed' >&2
  systemctl restart xmesh.service 2>/dev/null || true
  echo 'binary switch failed; previous binaries were restored when available' >&2
  exit 1
fi

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
started=true
if ! systemctl enable xmesh.service || ! systemctl restart xmesh.service || ! systemctl --no-pager --full status xmesh.service; then
  started=false
fi
if [ "$started" = true ]; then
  credential=$(sed -n 's/.*"credential"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' /etc/xmesh/node.json | head -n 1)
  started=false
  attempt=0
  while [ "$attempt" -lt 30 ]; do
    status=$(printf 'header = "Authorization: Bearer %s"\n' "$credential" | curl --config - --fail --silent --show-error --max-time 5 "${controller%/}/api/v1/self/status" 2>/dev/null) || status=''
    if printf '%s' "$status" | grep -Fq "\"binary_version\":\"$version\""; then
      started=true
      break
    fi
    attempt=$((attempt + 1))
    sleep 2
  done
fi
if [ "$started" != true ]; then
  journalctl -u xmesh.service -n 80 --no-pager || true
  restore_previous_binaries || echo 'restoring previous binaries also failed' >&2
  systemctl restart xmesh.service 2>/dev/null || true
  if [ -f /etc/xmesh/updater.json ]; then systemctl restart xmesh-updater.service 2>/dev/null || true; fi
  echo 'installation or Controller status check failed; previous binaries were restored when available' >&2
  exit 1
fi
install_updater_service
echo 'xmesh installed; logs: journalctl -u xmesh.service -f'
