#!/bin/sh
set -eu

install_dir=${1:?usage: controller-updater.sh INSTALL_DIR}
if [ "$(id -u)" -ne 0 ]; then echo 'Controller updater must run as root' >&2; exit 1; fi
if [ ! -f "$install_dir/compose.yaml" ] || [ ! -f "$install_dir/config/controller.json" ] || [ "$(cat "$install_dir/.role" 2>/dev/null)" != controller ]; then
  echo 'Controller Docker installation is missing' >&2
  exit 1
fi
data_dir=$install_dir/data
request=$data_dir/controller-upgrade.request
running=$data_dir/controller-upgrade.running
status=$data_dir/controller-upgrade.status.json
exec 9>"$install_dir/updater/upgrade.lock"
flock -n 9 || exit 0
if [ -f "$running" ]; then
  # No other updater holds the lock: a previous run was interrupted.
  stale_status="$data_dir/.controller-upgrade-status.$$"
  printf '{"state":"failed","version":"","at":"%s"}\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$stale_status"
  chmod 0644 "$stale_status"
  mv -f "$stale_status" "$status"
  rm -f "$running"
fi
[ -f "$request" ] || exit 0
mv "$request" "$running"
version=''
finished=false
write_status() {
  state=$1
  temporary="$data_dir/.controller-upgrade-status.$$"
  printf '{"state":"%s","version":"%s","at":"%s"}\n' "$state" "$version" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$temporary"
  chmod 0644 "$temporary"
  mv -f "$temporary" "$status"
}
cleanup() {
  if [ "$finished" != true ]; then write_status failed; fi
  rm -f "$running"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
write_status running

current=$(sed -n 's/.*"release_version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$install_dir/config/controller.json" | head -n 1)
printf '%s\n' "$current" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || { echo 'invalid current release version' >&2; exit 1; }
latest_url=$(curl --fail --silent --show-error --location --max-time 30 --proto '=https' --proto-redir '=https' --output /dev/null --write-out '%{url_effective}' 'https://github.com/Purestreams/xmesh/releases/latest')
case "$latest_url" in https://github.com/Purestreams/xmesh/releases/tag/*) version=${latest_url##*/};; *) echo 'unexpected GitHub latest release URL' >&2; exit 1;; esac
printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || { echo 'invalid latest release version' >&2; exit 1; }
highest=$(printf '%s\n%s\n' "$current" "$version" | sort -V | tail -n 1)
if [ "$highest" != "$version" ] || [ "$current" = "$version" ]; then
  write_status up-to-date
  finished=true
  exit 0
fi

work=$(mktemp -d)
trap 'rm -rf "$work"; cleanup' EXIT
base="https://github.com/Purestreams/xmesh/releases/download/$version"
for name in SHA256SUMS backup-controller.sh install-docker.sh; do
  curl --fail --silent --show-error --location --retry 3 --proto '=https' --proto-redir '=https' --output "$work/$name" "$base/$name"
done
(cd "$work" && grep '  backup-controller.sh$' SHA256SUMS | sha256sum -c - && grep '  install-docker.sh$' SHA256SUMS | sha256sum -c -)
sh "$work/backup-controller.sh" "$install_dir" /var/backups/xmesh-controller
sh "$work/install-docker.sh" --role controller --version "$version" --install-dir "$install_dir"
write_status succeeded
finished=true
