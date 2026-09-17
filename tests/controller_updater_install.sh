#!/bin/sh
set -eu

if [ ! -f /.dockerenv ] || [ "$(id -u)" -ne 0 ]; then
  echo 'run this installation test as root inside a disposable Docker container' >&2
  exit 1
fi
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
mkdir -p "$test_dir/bin" "$test_dir/assets" "$test_dir/package" "$test_dir/install/config" "$test_dir/install/data" "$test_dir/install/image" /run/systemd/system /etc/systemd/system

cat >"$test_dir/bin/curl" <<'EOF'
#!/bin/sh
out=''
url=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) out=$2; shift 2 ;;
    *) url=$1; shift ;;
  esac
done
if [ -n "$out" ]; then cp "$FAKE_RELEASE_ASSETS/$(basename "$url")" "$out"; fi
EOF
cat >"$test_dir/bin/docker" <<'EOF'
#!/bin/sh
case "$1/$2" in
  compose/version|compose/up) exit 0 ;;
  compose/ps) echo test-container; exit 0 ;;
  inspect/-f) echo true; exit 0 ;;
esac
exit 1
EOF
cat >"$test_dir/bin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_SYSTEMCTL_LOG"
EOF
cat >"$test_dir/bin/sleep" <<'EOF'
#!/bin/sh
exit 0
EOF
cat >"$test_dir/package/xmesh" <<'EOF'
#!/bin/sh
if [ "$1" = version ]; then echo v0.2.3; exit 0; fi
exit 1
EOF
printf 'xray\n' >"$test_dir/package/xray"
cp scripts/controller-updater.sh "$test_dir/package/controller-updater.sh"
chmod 755 "$test_dir/bin/curl" "$test_dir/bin/docker" "$test_dir/bin/systemctl" "$test_dir/bin/sleep" "$test_dir/package/xmesh" "$test_dir/package/xray" "$test_dir/package/controller-updater.sh"
tar -C "$test_dir/package" -czf "$test_dir/assets/xmesh-v0.2.3-linux-amd64.tar.gz" xmesh xray controller-updater.sh
(cd "$test_dir/assets" && sha256sum xmesh-v0.2.3-linux-amd64.tar.gz >SHA256SUMS)
printf '{"listen":"127.0.0.1:8088","release_version":"v0.2.2"}\n' >"$test_dir/install/config/controller.json"
printf 'old-compose\n' >"$test_dir/install/compose.yaml"
printf 'old-xmesh\n' >"$test_dir/install/image/xmesh"
printf 'old-xray\n' >"$test_dir/install/image/xray"
printf 'old-dockerfile\n' >"$test_dir/install/image/Dockerfile"
chmod 755 "$test_dir/install/image/xmesh" "$test_dir/install/image/xray"
printf 'controller\n' >"$test_dir/install/.role"

export FAKE_RELEASE_ASSETS="$test_dir/assets" FAKE_SYSTEMCTL_LOG="$test_dir/systemctl.log"
PATH="$test_dir/bin:$PATH"; export PATH
sh scripts/install-docker.sh --role controller --version v0.2.3 --install-dir "$test_dir/install"
test -f "$test_dir/install/data/controller-updater.ready"
test -x "$test_dir/install/updater/controller-updater.sh"
grep -q "ExecStart=/bin/sh $test_dir/install/updater/controller-updater.sh $test_dir/install" /etc/systemd/system/xmesh-controller-updater.service
grep -q 'enable --now xmesh-controller-updater.timer' "$test_dir/systemctl.log"
grep -q '"release_version": "v0.2.3"' "$test_dir/install/config/controller.json"
echo 'Docker Controller updater installation: PASS'
