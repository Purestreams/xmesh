#!/bin/sh
set -eu

controller=''
release_base_url='https://github.com/Purestreams/xmesh/releases/download'
version=''
role=''
node_id=''
mode=''
install_dir=''
token_stdin=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --controller) controller=$2; shift 2 ;;
    --release-base-url) release_base_url=$2; shift 2 ;;
    --version) version=$2; shift 2 ;;
    --role) role=$2; shift 2 ;;
    --node-id) node_id=$2; shift 2 ;;
    --mode) mode=$2; shift 2 ;;
    --install-dir) install_dir=$2; shift 2 ;;
    --token-stdin) token_stdin=true; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ "$(id -u)" -eq 0 ] || { echo 'run as root' >&2; exit 1; }
case "$controller" in https://*) ;; *) echo 'Controller must use HTTPS' >&2; exit 2;; esac
case "$release_base_url" in https://*) ;; *) echo 'release source must use HTTPS' >&2; exit 2;; esac
case "$version" in v[0-9]* ) ;; *) echo 'invalid version' >&2; exit 2;; esac
case "$version" in *[!a-zA-Z0-9._-]* ) echo 'invalid version' >&2; exit 2;; esac
case "$role" in gateway|agent) ;; *) echo 'invalid role' >&2; exit 2;; esac
case "$node_id" in ''|*[!a-zA-Z0-9_-]*) echo 'invalid node ID' >&2; exit 2;; esac
case "$mode" in systemd|docker) ;; *) echo 'invalid mode' >&2; exit 2;; esac
if [ "$mode" = docker ]; then
  [ -n "$install_dir" ] || install_dir="/opt/xmesh-docker-$role"
  case "$install_dir" in /*) ;; *) echo 'Docker install directory must be absolute' >&2; exit 2;; esac
  case "$install_dir" in *[!a-zA-Z0-9_./-]*|/|/etc|/opt|/var|/usr) echo 'unsafe Docker install directory' >&2; exit 2;; esac
  node_config="$install_dir/config/node.json"
  updater_config="$install_dir/updater/config.json"
  updater_bin="$install_dir/updater/xmesh-updater"
  service="xmesh-updater-$node_id.service"
else
  node_config='/etc/xmesh/node.json'
  updater_config='/etc/xmesh/updater.json'
  updater_bin='/usr/local/bin/xmesh-updater'
  service='xmesh-updater.service'
fi
[ -f "$node_config" ] || { echo 'existing node identity was not found' >&2; exit 1; }
if [ "$mode" = systemd ]; then chown root:xmesh /etc/xmesh; chmod 0750 /etc/xmesh; fi
existing_id=$(sed -n 's/.*"node_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$node_config" | head -n 1)
existing_role=$(sed -n 's/.*"role"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$node_config" | head -n 1)
[ "$existing_id" = "$node_id" ] && [ "$existing_role" = "$role" ] || { echo 'node identity mismatch' >&2; exit 1; }
for tool in curl sha256sum tar systemctl; do command -v "$tool" >/dev/null 2>&1 || { echo "missing $tool" >&2; exit 1; }; done
case "$(uname -m)" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo 'unsupported architecture' >&2; exit 1;; esac
archive="xmesh-$version-linux-$arch.tar.gz"
base="${release_base_url%/}/$version"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
curl --fail --location --retry 3 --proto '=https' --proto-redir '=https' --output "$work/$archive" "$base/$archive"
curl --fail --location --retry 3 --proto '=https' --proto-redir '=https' --output "$work/SHA256SUMS" "$base/SHA256SUMS"
(cd "$work" && grep "  $archive\$" SHA256SUMS | sha256sum -c -)
tar -xzf "$work/$archive" -C "$work" xmesh-updater
if [ "$mode" = docker ]; then install -d -m 0700 "$install_dir/updater"; fi
updater_tmp="$updater_bin.new.$$"
install -m 0700 "$work/xmesh-updater" "$updater_tmp"
mv -f "$updater_tmp" "$updater_bin"
if [ "$token_stdin" = true ]; then
  IFS= read -r token
else
  [ -r /dev/tty ] || { echo 'terminal or --token-stdin required' >&2; exit 1; }
  printf 'Updater pairing token: ' >/dev/tty
  old_stty=$(stty -g </dev/tty)
  stty -echo </dev/tty
  IFS= read -r token </dev/tty
  stty "$old_stty" </dev/tty
  printf '\n' >/dev/tty
fi
if [ "$mode" = docker ]; then
  printf '%s' "$token" | "$updater_bin" pair --config "$updater_config" --mode docker --install-dir "$install_dir" --controller "$controller" --node-id "$node_id" --role "$role" --token-stdin
else
  printf '%s' "$token" | "$updater_bin" pair --config "$updater_config" --mode systemd --controller "$controller" --node-id "$node_id" --role "$role" --token-stdin
fi
cat >"/etc/systemd/system/$service" <<EOF
[Unit]
Description=xmesh host upgrade assistant for $node_id
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$updater_bin run --config $updater_config
Restart=always
RestartSec=10s

[Install]
WantedBy=multi-user.target
EOF
if [ "$mode" = systemd ]; then install -d -m 0700 /var/lib/xmesh-updater; fi
systemctl daemon-reload
systemctl enable "$service"
systemctl restart "$service"
echo "updater installed; status: systemctl status $service"
