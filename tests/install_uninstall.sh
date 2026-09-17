#!/bin/sh
set -eu

[ -f /.dockerenv ] || { echo 'run this test in a disposable Docker container' >&2; exit 1; }
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
mkdir -p "$test_dir/bin" /run/systemd/system /etc/systemd/system /etc/xmesh /var/lib/xmesh-updater
cat >"$test_dir/bin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$SYSTEMCTL_LOG"
EOF
chmod 755 "$test_dir/bin/systemctl"
SYSTEMCTL_LOG="$test_dir/systemctl.log"; export SYSTEMCTL_LOG
PATH="$test_dir/bin:$PATH"; export PATH
touch /etc/systemd/system/xmesh.service /etc/systemd/system/xmesh-updater.service
touch /usr/local/bin/xmesh /usr/local/bin/xmesh-updater /etc/xmesh/node.json /etc/xmesh/updater.json /var/lib/xmesh-updater/job
sh scripts/install.sh --uninstall
grep -q '^disable --now xmesh-updater.service$' "$SYSTEMCTL_LOG"
grep -q '^disable --now xmesh.service$' "$SYSTEMCTL_LOG"
test ! -e /etc/systemd/system/xmesh-updater.service
test ! -e /usr/local/bin/xmesh-updater
test -e /etc/xmesh/updater.json
test -e /var/lib/xmesh-updater/job
sh scripts/install.sh --uninstall --purge
test ! -e /etc/xmesh/updater.json
test ! -e /var/lib/xmesh-updater
echo 'Systemd node and updater uninstall: PASS'
