#!/bin/sh
set -eu

test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
mkdir -p "$test_dir/bin" "$test_dir/assets" "$test_dir/install/config" "$test_dir/install/data" "$test_dir/install/updater"
printf 'controller\n' >"$test_dir/install/.role"
printf 'compose\n' >"$test_dir/install/compose.yaml"
printf '{"release_version":"v0.2.3"}\n' >"$test_dir/install/config/controller.json"

cat >"$test_dir/bin/curl" <<'EOF'
#!/bin/sh
set -eu
out=''
url=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) out=$2; shift 2 ;;
    --write-out|--max-time|--retry|--proto|--proto-redir) shift 2 ;;
    *) url=$1; shift ;;
  esac
done
case "$url" in
  https://github.com/Purestreams/xmesh/releases/latest)
    printf 'https://github.com/Purestreams/xmesh/releases/tag/v0.2.4' ;;
  *) cp "$FAKE_RELEASE_ASSETS/$(basename "$url")" "$out" ;;
esac
EOF
cat >"$test_dir/assets/backup-controller.sh" <<'EOF'
#!/bin/sh
printf 'backup\n' >>"$FAKE_UPDATER_LOG"
EOF
cat >"$test_dir/assets/install-docker.sh" <<'EOF'
#!/bin/sh
printf 'install %s\n' "$*" >>"$FAKE_UPDATER_LOG"
EOF
chmod 755 "$test_dir/bin/curl" "$test_dir/assets/backup-controller.sh" "$test_dir/assets/install-docker.sh"
(cd "$test_dir/assets" && sha256sum backup-controller.sh install-docker.sh >SHA256SUMS)
export FAKE_RELEASE_ASSETS="$test_dir/assets" FAKE_UPDATER_LOG="$test_dir/actions"
PATH="$test_dir/bin:$PATH"; export PATH

printf 'request\n' >"$test_dir/install/data/controller-upgrade.request"
sh scripts/controller-updater.sh "$test_dir/install"
grep -q '^backup$' "$test_dir/actions"
grep -q 'install --role controller --version v0.2.4' "$test_dir/actions"
grep -q '"state":"succeeded"' "$test_dir/install/data/controller-upgrade.status.json"
test ! -e "$test_dir/install/data/controller-upgrade.request"
test ! -e "$test_dir/install/data/controller-upgrade.running"

printf 'tampered\n' >"$test_dir/assets/install-docker.sh"
printf 'request\n' >"$test_dir/install/data/controller-upgrade.request"
if sh scripts/controller-updater.sh "$test_dir/install" >"$test_dir/failure-output" 2>&1; then
  echo 'tampered installer was accepted' >&2
  exit 1
fi
grep -q '"state":"failed"' "$test_dir/install/data/controller-upgrade.status.json"
test "$(wc -l <"$test_dir/actions")" -eq 2
printf 'interrupted\n' >"$test_dir/install/data/controller-upgrade.running"
sh scripts/controller-updater.sh "$test_dir/install"
grep -q '"state":"failed"' "$test_dir/install/data/controller-upgrade.status.json"
test ! -e "$test_dir/install/data/controller-upgrade.running"
echo 'Controller one-click updater verification: PASS'
