#!/bin/sh
set -eu

test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
mkdir -p "$test_dir/bin" "$test_dir/assets" "$test_dir/package" "$test_dir/install/config" "$test_dir/install/data" "$test_dir/install/image"

cat >"$test_dir/bin/curl" <<'EOF'
#!/bin/sh
set -eu
out=''
url=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) out=$2; shift 2 ;;
    *) url=$1; shift ;;
  esac
done
cp "$FAKE_RELEASE_ASSETS/$(basename "$url")" "$out"
EOF
cat >"$test_dir/bin/docker" <<'EOF'
#!/bin/sh
set -eu
if [ "$1" != compose ]; then exit 1; fi
case "$2" in
  version|logs) exit 0 ;;
  up)
    count=$(cat "$FAKE_DOCKER_COUNT")
    count=$((count + 1))
    printf '%s\n' "$count" >"$FAKE_DOCKER_COUNT"
    if [ "$count" -eq 1 ]; then exit 1; fi
    exit 0 ;;
esac
exit 1
EOF
cat >"$test_dir/package/xmesh" <<'EOF'
#!/bin/sh
if [ "$1" = version ]; then echo v-test; exit 0; fi
exit 1
EOF
printf 'new-xray\n' >"$test_dir/package/xray"
printf 'new-updater\n' >"$test_dir/package/controller-updater.sh"
printf 'new-node-updater\n' >"$test_dir/package/xmesh-updater"
chmod 755 "$test_dir/bin/curl" "$test_dir/bin/docker" "$test_dir/package/xmesh" "$test_dir/package/xray" "$test_dir/package/controller-updater.sh" "$test_dir/package/xmesh-updater"
tar -C "$test_dir/package" -czf "$test_dir/assets/xmesh-v-test-linux-amd64.tar.gz" xmesh xmesh-updater xray controller-updater.sh
(cd "$test_dir/assets" && sha256sum xmesh-v-test-linux-amd64.tar.gz >SHA256SUMS)

printf 'old-compose\n' >"$test_dir/install/compose.yaml"
printf 'old-xmesh\n' >"$test_dir/install/image/xmesh"
printf 'old-xray\n' >"$test_dir/install/image/xray"
printf 'old-dockerfile\n' >"$test_dir/install/image/Dockerfile"
printf '{"release_version":"v-old"}\n' >"$test_dir/install/config/controller.json"
chmod 755 "$test_dir/install/image/xmesh" "$test_dir/install/image/xray"
printf 'controller\n' >"$test_dir/install/.role"
printf '0\n' >"$test_dir/docker-count"

export FAKE_RELEASE_ASSETS="$test_dir/assets" FAKE_DOCKER_COUNT="$test_dir/docker-count"
PATH="$test_dir/bin:$PATH"; export PATH
if sh scripts/install-docker.sh --role controller --version v-test --install-dir "$test_dir/install" --public-url https://panel.example.test >"$test_dir/output" 2>&1; then
  echo 'expected the simulated upgrade to fail' >&2
  exit 1
fi
test "$(cat "$test_dir/docker-count")" = 2
test "$(cat "$test_dir/install/compose.yaml")" = old-compose
test "$(cat "$test_dir/install/image/xmesh")" = old-xmesh
test "$(cat "$test_dir/install/image/xray")" = old-xray
test "$(cat "$test_dir/install/image/Dockerfile")" = old-dockerfile
grep -q '"release_version":"v-old"' "$test_dir/install/config/controller.json"
echo 'Docker upgrade rollback: PASS'
