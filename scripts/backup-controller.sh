#!/bin/sh
set -eu

install_dir=${1:-/opt/xmesh-docker-controller}
backup_dir=${2:-/var/backups/xmesh-controller}
if [ "$(id -u)" -ne 0 ]; then echo 'run as root' >&2; exit 1; fi
case "$install_dir" in /*) ;; *) echo 'install directory must be absolute' >&2; exit 2;; esac
case "$backup_dir" in /*) ;; *) echo 'backup directory must be absolute' >&2; exit 2;; esac
if [ ! -f "$install_dir/config/controller.json" ]; then
  echo 'Controller configuration is missing' >&2
  exit 1
fi
umask 077
mkdir -p "$backup_dir"
archive=$(mktemp "$backup_dir/xmesh-controller-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX.tar.gz")
check_dir=$(mktemp -d)
passed=false
trap 'rm -rf "$check_dir"; if [ "$passed" != true ]; then rm -f "$archive" "$archive.sha256"; fi' EXIT HUP INT TERM
if [ -f "$install_dir/data/controller-state.json" ]; then
  tar -C "$install_dir" -czf "$archive" config/controller.json data/controller-state.json
else
  tar -C "$install_dir" -czf "$archive" config/controller.json
fi
tar -xzf "$archive" -C "$check_dir"
cmp "$install_dir/config/controller.json" "$check_dir/config/controller.json"
if [ -f "$install_dir/data/controller-state.json" ]; then
  cmp "$install_dir/data/controller-state.json" "$check_dir/data/controller-state.json"
fi
archive_name=$(basename "$archive")
(cd "$backup_dir" && sha256sum "$archive_name" >"$archive_name.sha256")
passed=true
echo "verified Controller backup: $archive"
echo 'Keep the archive and checksum off-host; they contain credentials and private keys.'
