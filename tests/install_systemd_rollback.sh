#!/bin/sh
set -eu

[ -f /.dockerenv ] || { echo 'run in a disposable Docker container' >&2; exit 1; }
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
mkdir -p "$test_dir/bin" "$test_dir/assets" "$test_dir/package" /run/systemd/system /etc/xmesh /usr/local/lib/xmesh
printf '{"node_id":"n","role":"gateway","credential":"old"}\n' >/etc/xmesh/node.json
printf '#!/bin/sh\necho old-version\n' >/usr/local/bin/xmesh
printf 'old-xray\n' >/usr/local/lib/xmesh/xray
chmod 755 /usr/local/bin/xmesh /usr/local/lib/xmesh/xray
cp /usr/local/bin/xmesh "$test_dir/old-xmesh"
cp /usr/local/lib/xmesh/xray "$test_dir/old-xray"
printf '#!/bin/sh\necho v0.3.3\n' >"$test_dir/package/xmesh"
printf 'new-xray\n' >"$test_dir/package/xray"
chmod 755 "$test_dir/package/xmesh" "$test_dir/package/xray"
tar -C "$test_dir/package" -czf "$test_dir/assets/xmesh-v0.3.3-linux-amd64.tar.gz" xmesh xray
(cd "$test_dir/assets" && sha256sum xmesh-v0.3.3-linux-amd64.tar.gz >SHA256SUMS)
cat >"$test_dir/bin/curl" <<'EOF'
#!/bin/sh
set -eu
for arg do
  case "$arg" in --output) ;; *) url=$arg;; esac
done
case "$url" in
  */healthz) exit 0 ;;
  *)
    previous=''
    for arg do
      if [ "$previous" = --output ]; then output=$arg; break; fi
      previous=$arg
    done
    cp "$FAKE_ASSETS/$(basename "$url")" "$output" ;;
esac
EOF
cat >"$test_dir/bin/mv" <<'EOF'
#!/bin/sh
for arg do destination=$arg; done
if [ "$destination" = /usr/local/lib/xmesh/xray ] && [ ! -e "$FAIL_MARKER" ]; then
  touch "$FAIL_MARKER"
  exit 1
fi
exec /bin/mv "$@"
EOF
cat >"$test_dir/bin/systemctl" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 755 "$test_dir/bin/curl" "$test_dir/bin/mv" "$test_dir/bin/systemctl"
FAKE_ASSETS="$test_dir/assets"; export FAKE_ASSETS
FAIL_MARKER="$test_dir/failed-once"; export FAIL_MARKER
PATH="$test_dir/bin:$PATH"; export PATH
if sh scripts/install.sh --controller https://panel.example --release-base-url https://panel.example/releases --version v0.3.3 --role gateway >"$test_dir/output" 2>&1; then
  echo 'gateway installation unexpectedly succeeded after Xray switch failure' >&2
  exit 1
fi
test -e "$FAIL_MARKER"
cmp "$test_dir/old-xmesh" /usr/local/bin/xmesh
cmp "$test_dir/old-xray" /usr/local/lib/xmesh/xray
grep -q 'binary switch failed' "$test_dir/output"
echo 'Legacy systemd binary rollback: PASS'
