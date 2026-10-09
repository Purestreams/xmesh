#!/bin/sh
set -eu

[ -f /.dockerenv ] || { echo 'run this test in a disposable Docker container' >&2; exit 1; }
test_dir=$(mktemp -d)
cp /etc/os-release "$test_dir/os-release"
trap 'cp "$test_dir/os-release" /etc/os-release; rm -rf "$test_dir"' EXIT HUP INT TERM
mkdir -p "$test_dir/bin" "$test_dir/assets" "$test_dir/package" /run/systemd/system /etc/systemd/system
. /etc/os-release
distribution="${ID}-${VERSION_ID}"
legacy=false
if [ "$ID" = centos ] && [ "${VERSION_ID%%.*}" = 7 ]; then legacy=true; fi

cat >"$test_dir/package/xmesh" <<'EOF'
#!/bin/sh
set -eu
case "$1" in
  version) echo v0.3.7-test ;;
  hash-password) echo password-hash ;;
  generate-secret) echo session-secret ;;
  enroll)
    shift
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --role) role=$2; shift 2 ;;
        --output) output=$2; shift 2 ;;
        --updater-output) updater_output=$2; shift 2 ;;
        *) shift ;;
      esac
    done
    [ "$(cat)" = enrollment-secret ]
    printf '{"node_id":"n1","role":"%s","credential":"node-secret"}\n' "$role" >"$output"
    printf '{"credential":"updater-secret"}\n' >"$updater_output" ;;
  *) exit 1 ;;
esac
EOF
cat >"$test_dir/package/xmesh-updater" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$UPDATER_LOG"
case "$1" in
  local) exit 0 ;;
  pair)
    shift
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --config) config=$2; shift 2 ;;
        *) shift ;;
      esac
    done
    [ "$(cat)" = pairing-secret ]
    printf '{"credential":"paired-updater-secret"}\n' >"$config" ;;
  *) exit 1 ;;
esac
EOF
printf '#!/bin/sh\nexit 0\n' >"$test_dir/package/xray"
chmod 755 "$test_dir/package/"*
case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) exit 1 ;; esac
archive="xmesh-v0.3.7-test-linux-$arch.tar.gz"
tar -C "$test_dir/package" -czf "$test_dir/assets/$archive" xmesh xmesh-updater xray
(cd "$test_dir/assets" && sha256sum "$archive" >SHA256SUMS)
cat >"$test_dir/bin/curl" <<'EOF'
#!/bin/sh
set -eu
output=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output=$2; shift 2 ;;
    *) url=$1; shift ;;
  esac
done
case "$url" in
  */healthz) exit 0 ;;
  */api/v1/self/status) cat >/dev/null; printf '{"binary_version":"v0.3.7-test"}\n' ;;
  *) cp "$FAKE_ASSETS/$(basename "$url")" "$output" ;;
esac
EOF
cat >"$test_dir/bin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$SYSTEMCTL_LOG"
EOF
printf '#!/bin/sh\nexit 0\n' >"$test_dir/bin/ss"
chmod 755 "$test_dir/bin/"*
FAKE_ASSETS="$test_dir/assets"; export FAKE_ASSETS
UPDATER_LOG="$test_dir/updater.log"; export UPDATER_LOG
SYSTEMCTL_LOG="$test_dir/systemctl.log"; export SYSTEMCTL_LOG
PATH="$test_dir/bin:$PATH"; export PATH

same_content() {
  [ "$(sha256sum "$1" | cut -d ' ' -f 1)" = "$(sha256sum "$2" | cut -d ' ' -f 1)" ]
}

verify_unit() {
  # Minimal Debian build images may omit the parser; systemd 219 cannot read cgroup v2.
  if ! command -v systemd-analyze >/dev/null 2>&1; then return; fi
  if [ "$legacy" = true ] && [ -f /sys/fs/cgroup/cgroup.controllers ]; then return; fi
  if ! SYSTEMD_LOG_LEVEL=warning systemd-analyze verify "$1" >"$test_dir/verify.log" 2>&1; then
    cat "$test_dir/verify.log" >&2
    exit 1
  fi
  if grep -Ei 'unknown (lvalue|key)|failed to parse' "$test_dir/verify.log"; then exit 1; fi
}

if ! command -v systemd-analyze >/dev/null 2>&1; then
  echo 'systemd parser check: SKIP (parser not installed); service settings are checked below'
elif [ "$legacy" = true ] && [ -f /sys/fs/cgroup/cgroup.controllers ]; then
  echo 'systemd 219 parser check: SKIP (cgroup v2 host); legacy service settings are checked below'
fi

verify_service() {
  service=$1
  state_dir=$2
  unit="/etc/systemd/system/$service"
  grep -q '^User=xmesh$' "$unit"
  grep -q '^NoNewPrivileges=true$' "$unit"
  grep -q '^CapabilityBoundingSet=$' "$unit"
  if [ "$legacy" = true ]; then
    grep -q '^ProtectSystem=full$' "$unit"
    grep -q '^ReadOnlyDirectories=/$' "$unit"
    grep -q "^ReadWriteDirectories=/dev /proc /sys $state_dir\$" "$unit"
    if grep -Eq '^(AmbientCapabilities|ReadWritePaths)=' "$unit"; then cat "$unit" >&2; exit 1; fi
  else
    grep -q '^ProtectSystem=strict$' "$unit"
    grep -q "^ReadWritePaths=$state_dir\$" "$unit"
    grep -q '^AmbientCapabilities=$' "$unit"
  fi
  verify_unit "$unit"
}

for role in gateway agent; do
  sh scripts/install.sh --controller https://panel.example.test --role "$role" \
    --version v0.3.7-test --release-base-url https://releases.example.test \
    --enrollment-token enrollment-secret --vmess-port 8086
  grep -q "\"role\":\"$role\"" /etc/xmesh/node.json
  test "$(stat -c %a /etc/xmesh/node.json)" = 600
  test "$(stat -c %U /etc/xmesh/node.json)" = xmesh
  verify_service xmesh.service /var/lib/xmesh
  if [ "$legacy" = true ]; then write_paths=ReadWriteDirectories; else write_paths=ReadWritePaths; fi
  grep -q "^$write_paths=/var/lib/xmesh-updater /usr/local/bin /usr/local/lib/xmesh\$" /etc/systemd/system/xmesh-updater.service
  verify_unit /etc/systemd/system/xmesh-updater.service
  cp /etc/xmesh/node.json "$test_dir/identity-before"
  sh scripts/install.sh --controller https://panel.example.test --role "$role" \
    --version v0.3.7-test --release-base-url https://releases.example.test --vmess-port 8086
  grep -q '^local ' "$UPDATER_LOG"
  same_content "$test_dir/identity-before" /etc/xmesh/node.json
  printf 'pairing-secret\n' | sh scripts/install-updater.sh --controller https://panel.example.test \
    --release-base-url https://releases.example.test --version v0.3.7-test \
    --role "$role" --node-id n1 --mode systemd --token-stdin
  same_content "$test_dir/identity-before" /etc/xmesh/node.json
  verify_unit /etc/systemd/system/xmesh-updater.service
  cp "$test_dir/assets/$archive" "$test_dir/archive-before"
  printf 'tampered\n' >"$test_dir/assets/$archive"
  if sh scripts/install.sh --controller https://panel.example.test --role "$role" \
      --version v0.3.7-test --release-base-url https://releases.example.test >"$test_dir/output" 2>&1; then
    echo 'tampered archive was accepted' >&2
    exit 1
  fi
  grep -q 'FAILED' "$test_dir/output"
  same_content "$test_dir/identity-before" /etc/xmesh/node.json
  same_content "$test_dir/package/xmesh" /usr/local/bin/xmesh
  cp "$test_dir/archive-before" "$test_dir/assets/$archive"
  sh scripts/install.sh --uninstall
  test -f /etc/xmesh/node.json
  test -f /etc/xmesh/updater.json
  test ! -e /etc/systemd/system/xmesh.service
  test ! -e /etc/systemd/system/xmesh-updater.service
  sh scripts/install.sh --uninstall --purge
  test ! -e /etc/xmesh
done

XMESH_ADMIN_PASSWORD=test-password; export XMESH_ADMIN_PASSWORD
sh scripts/install-controller.sh --version v0.3.7-test --public-url https://panel.example.test \
  --release-base-url https://releases.example.test
verify_service xmesh-controller.service /var/lib/xmesh-controller
cp /etc/xmesh/controller.json "$test_dir/controller-before"
sh scripts/install-controller.sh --version v0.3.7-test --release-base-url https://releases.example.test
same_content "$test_dir/controller-before" /etc/xmesh/controller.json
sh scripts/install-controller.sh --uninstall
test -f /etc/xmesh/controller.json
sh scripts/install-controller.sh --uninstall --purge
test ! -e /etc/xmesh/controller.json

# Check minor-version IDs, and keep unsupported distributions/versions fail-closed.
for script in scripts/install.sh scripts/install-controller.sh; do
  for version in 7.9.2009 8.5.2111; do
    printf 'ID=centos\nVERSION_ID="%s"\n' "$version" >/etc/os-release
    if sh "$script" >"$test_dir/output" 2>&1; then exit 1; fi
    if grep -q 'unsupported' "$test_dir/output"; then cat "$test_dir/output" >&2; exit 1; fi
  done
  for version in 6 9 '' 7bad; do
    printf 'ID=centos\nVERSION_ID="%s"\n' "$version" >/etc/os-release
    if sh "$script" --uninstall >"$test_dir/output" 2>&1; then exit 1; fi
    grep -q 'unsupported CentOS version:' "$test_dir/output"
  done
  printf 'ID=alpine\nVERSION_ID=3\n' >/etc/os-release
  if sh "$script" --uninstall >"$test_dir/output" 2>&1; then exit 1; fi
  grep -q 'unsupported distribution: alpine' "$test_dir/output"
done
echo "Systemd installers on $distribution: PASS"
