#!/bin/sh
set -eu

test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
mkdir -p "$test_dir/bin" /run/systemd/system

cat >"$test_dir/bin/curl" <<'EOF'
#!/bin/sh
for arg do url=$arg; done
case "$url" in
  */healthz) exit 0 ;;
  *) echo DOWNLOAD_REACHED >&2; exit 88 ;;
esac
EOF
cat >"$test_dir/bin/docker" <<'EOF'
#!/bin/sh
if [ "$1" = compose ] && [ "$2" = version ]; then exit 0; fi
exit 1
EOF
cat >"$test_dir/bin/ss" <<'EOF'
#!/bin/sh
if [ "$1" = -lunH ]; then
  printf 'UNCONN 0 0 0.0.0.0:%s 0.0.0.0:*\n' "${FAKE_UDP_PORT:-9999}"
else
  printf 'LISTEN 0 128 0.0.0.0:%s 0.0.0.0:*\n' "$FAKE_LISTEN_PORT"
fi
EOF
chmod 755 "$test_dir/bin/curl" "$test_dir/bin/docker" "$test_dir/bin/ss"
PATH="$test_dir/bin:$PATH"; export PATH

check_case() {
  script=$1
  busy=$2
  selected=$3
  expected=$4
	selected_hy2=${5:-0}
	vmess_off=${6:-false}
  FAKE_LISTEN_PORT=$busy; export FAKE_LISTEN_PORT
  if [ "$script" = scripts/install-docker.sh ]; then
    set -- --install-dir "$test_dir/install"
  else
    set --
  fi
	if [ "$vmess_off" = true ]; then set -- "$@" --disable-vmess; fi
  if sh "$script" --controller https://panel.example.test --role gateway --version v-test \
      --release-base-url https://releases.example.test --vmess-port "$selected" --hy2-port "$selected_hy2" \
      "$@" >"$test_dir/output" 2>&1; then
    echo "expected installer to stop at the stubbed download or port check" >&2
    exit 1
  fi
  if ! grep -Fq -- "$expected" "$test_dir/output"; then
    cat "$test_dir/output" >&2
    echo "missing expected result: $expected" >&2
    exit 1
  fi
}

for script in scripts/install-docker.sh scripts/install.sh; do
  check_case "$script" 8080 8086 DOWNLOAD_REACHED
  check_case "$script" 8086 8086 'TCP port 8086 is already in use'
  check_case "$script" 8443 8086 'TCP port 8443 is already in use'
  check_case "$script" 8080 0 '--vmess-port must be a TCP port from 1 to 65535'
  check_case "$script" 8080 65536 '--vmess-port must be a TCP port from 1 to 65535'
  FAKE_UDP_PORT=8080; export FAKE_UDP_PORT
  check_case "$script" 9999 8086 'UDP port 8080 is already in use' 8080
  FAKE_UDP_PORT=9999; export FAKE_UDP_PORT
  check_case "$script" 8080 8086 DOWNLOAD_REACHED 8080
  check_case "$script" 9999 8086 '--hy2-port must be between 0 and 65535' 65536
  check_case "$script" 8086 8086 DOWNLOAD_REACHED 0 true
done
echo 'Gateway configured port preflight: PASS'
