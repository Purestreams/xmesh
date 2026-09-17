#!/bin/sh
set -eu

test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
mkdir -p "$test_dir/bin" "$test_dir/assets" "$test_dir/package" "$test_dir/install/config" "$test_dir/install/image"
printf '{"node_id":"n1","role":"agent","credential":"business-secret"}\n' >"$test_dir/install/config/node.json"
cp "$test_dir/install/config/node.json" "$test_dir/identity-before"
printf 'services:\n  xmesh:\n    image: xmesh-local:v0.3.2\n' >"$test_dir/install/compose.yaml"
cat >"$test_dir/package/xmesh-updater" <<'EOF'
#!/bin/sh
set -eu
if [ "$1" = hold ]; then sleep 20; exit 0; fi
[ "$1" = pair ] || exit 1
shift
while [ "$#" -gt 0 ]; do
  case "$1" in
    --config) config=$2; shift 2 ;;
    --token-stdin) token=$(cat); [ "$token" = pair-secret ]; shift ;;
    *) shift ;;
  esac
done
printf '{"credential":"updater-secret"}\n' >"$config"
EOF
chmod 755 "$test_dir/package/xmesh-updater"
tar -C "$test_dir/package" -czf "$test_dir/assets/xmesh-v0.3.3-linux-amd64.tar.gz" xmesh-updater
(cd "$test_dir/assets" && sha256sum xmesh-v0.3.3-linux-amd64.tar.gz >SHA256SUMS)
cat >"$test_dir/bin/curl" <<'EOF'
#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output=$2; shift 2 ;;
    *) url=$1; shift ;;
  esac
done
cp "$FAKE_ASSETS/$(basename "$url")" "$output"
EOF
cat >"$test_dir/bin/systemctl" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 755 "$test_dir/bin/curl" "$test_dir/bin/systemctl"
FAKE_ASSETS="$test_dir/assets"; export FAKE_ASSETS
PATH="$test_dir/bin:$PATH"; export PATH
printf 'pair-secret\n' | sh scripts/install-updater.sh --controller https://panel.example --release-base-url https://panel.example/releases --version v0.3.3 --role agent --node-id n1 --mode docker --install-dir "$test_dir/install" --token-stdin
cmp "$test_dir/identity-before" "$test_dir/install/config/node.json"
test -x "$test_dir/install/updater/xmesh-updater"
grep -q 'updater-secret' "$test_dir/install/updater/config.json"
grep -q "ExecStart=$test_dir/install/updater/xmesh-updater" /etc/systemd/system/xmesh-updater-n1.service
"$test_dir/install/updater/xmesh-updater" hold &
running_updater=$!
old_inode=$(stat -c %i "$test_dir/install/updater/xmesh-updater")
printf 'pair-secret\n' | sh scripts/install-updater.sh --controller https://panel.example --release-base-url https://panel.example/releases --version v0.3.3 --role agent --node-id n1 --mode docker --install-dir "$test_dir/install" --token-stdin
test "$old_inode" != "$(stat -c %i "$test_dir/install/updater/xmesh-updater")"
kill "$running_updater" 2>/dev/null || true
wait "$running_updater" 2>/dev/null || true
getent group xmesh >/dev/null || groupadd xmesh
mkdir -p /etc/xmesh
cp "$test_dir/identity-before" /etc/xmesh/node.json
printf 'pair-secret\n' | sh scripts/install-updater.sh --controller https://panel.example --release-base-url https://panel.example/releases --version v0.3.3 --role agent --node-id n1 --mode systemd --token-stdin
/usr/local/bin/xmesh-updater hold &
running_updater=$!
old_inode=$(stat -c %i /usr/local/bin/xmesh-updater)
printf 'pair-secret\n' | sh scripts/install-updater.sh --controller https://panel.example --release-base-url https://panel.example/releases --version v0.3.3 --role agent --node-id n1 --mode systemd --token-stdin
test "$old_inode" != "$(stat -c %i /usr/local/bin/xmesh-updater)"
kill "$running_updater" 2>/dev/null || true
wait "$running_updater" 2>/dev/null || true
printf 'tampered\n' >"$test_dir/assets/xmesh-v0.3.3-linux-amd64.tar.gz"
if printf 'pair-secret\n' | sh scripts/install-updater.sh --controller https://panel.example --release-base-url https://panel.example/releases --version v0.3.3 --role agent --node-id n1 --mode docker --install-dir "$test_dir/install" --token-stdin >"$test_dir/error" 2>&1; then
  echo 'tampered updater archive was accepted' >&2
  exit 1
fi
cmp "$test_dir/identity-before" "$test_dir/install/config/node.json"
echo 'Updater migration and checksum rejection: PASS'
