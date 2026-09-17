#!/bin/sh
set -eu

role=''
version=''
controller=''
token=''
public_url=''
listen='127.0.0.1:8088'
admin_username='admin'
release_base_url='https://github.com/Purestreams/xmesh/releases/download'
install_dir=''
rotate_credential=false
vmess_port='8080'

while [ "$#" -gt 0 ]; do
  case "$1" in
    --role) role=$2; shift 2 ;;
    --version) version=$2; shift 2 ;;
    --controller) controller=$2; shift 2 ;;
    --enrollment-token) token=$2; shift 2 ;;
    --public-url) public_url=$2; shift 2 ;;
    --listen) listen=$2; shift 2 ;;
    --admin-username) admin_username=$2; shift 2 ;;
    --release-base-url) release_base_url=$2; shift 2 ;;
    --install-dir) install_dir=$2; shift 2 ;;
    --vmess-port) vmess_port=$2; shift 2 ;;
    --rotate-credential) rotate_credential=true; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then echo 'run this installer as root' >&2; exit 1; fi
case "$role" in controller|gateway|agent) ;; *) echo '--role must be controller, gateway, or agent' >&2; exit 2;; esac
if [ -z "$version" ]; then echo '--version is required' >&2; exit 2; fi
if [ "$rotate_credential" = true ] && [ "$role" = controller ]; then echo 'Controller credentials are not node credentials' >&2; exit 2; fi
case "$version" in *[!a-zA-Z0-9._-]*|'') echo 'version must contain only letters, numbers, dots, underscores, or hyphens' >&2; exit 2;; esac
if [ "$role" = gateway ]; then
  case "$vmess_port" in ''|*[!0-9]*) echo '--vmess-port must be a TCP port from 1 to 65535' >&2; exit 2;; esac
  if [ "$vmess_port" -lt 1 ] || [ "$vmess_port" -gt 65535 ]; then echo '--vmess-port must be a TCP port from 1 to 65535' >&2; exit 2; fi
fi
case "$release_base_url" in https://*) ;; *) echo '--release-base-url must use HTTPS' >&2; exit 2;; esac
for tool in curl sha256sum tar mktemp; do
  command -v "$tool" >/dev/null 2>&1 || { echo "missing required tool: $tool" >&2; exit 1; }
done
if ! docker compose version >/dev/null 2>&1; then echo 'Docker with the Compose plugin is required' >&2; exit 1; fi
case "$(uname -s)/$(uname -m)" in Linux/x86_64|Linux/amd64) arch=amd64;; Linux/aarch64|Linux/arm64) arch=arm64;; *) echo 'Docker deployment supports Linux amd64 and arm64 hosts' >&2; exit 1;; esac
if [ -z "$install_dir" ]; then install_dir="/opt/xmesh-docker-$role"; fi
case "$install_dir" in /*) ;; *) echo '--install-dir must be an absolute path' >&2; exit 2;; esac
case "$install_dir" in /|/etc|/opt|/var|/usr) echo '--install-dir must name a dedicated directory' >&2; exit 2;; esac
if [ "$role" != controller ]; then
  case "$install_dir" in *[!a-zA-Z0-9_./-]*) echo 'node updater requires an install directory without spaces or shell metacharacters' >&2; exit 2;; esac
fi
if [ "$role" != controller ]; then
  case "$controller" in https://*) ;; *) echo '--controller must use HTTPS' >&2; exit 2;; esac
  curl --fail --silent --show-error --max-time 15 "${controller%/}/healthz" >/dev/null || {
    echo "Controller HTTPS health check failed: ${controller%/}/healthz" >&2
    exit 1
  }
fi
if [ ! -f "$install_dir/compose.yaml" ] && command -v ss >/dev/null 2>&1; then
  if [ "$role" = controller ]; then ports=${listen##*:}; elif [ "$role" = gateway ]; then ports="$vmess_port 8443"; else ports=''; fi
  for port in $ports; do
    if ss -ltnH | awk '{print $4}' | grep -Eq ":$port\$"; then
      echo "TCP port $port is already in use; resolve the conflict before installing $role" >&2
      exit 1
    fi
  done
fi

archive="xmesh-${version}-linux-${arch}.tar.gz"
base="${release_base_url%/}/${version}"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
curl --fail --location --retry 3 --proto '=https' --proto-redir '=https' --output "$work/$archive" "$base/$archive"
curl --fail --location --retry 3 --proto '=https' --proto-redir '=https' --output "$work/SHA256SUMS" "$base/SHA256SUMS"
(cd "$work" && grep "  $archive\$" SHA256SUMS | sha256sum -c -)
tar -xzf "$work/$archive" -C "$work" xmesh xmesh-updater xray controller-updater.sh
"$work/xmesh" version >/dev/null

install -d -m 0750 "$install_dir" "$install_dir/config" "$install_dir/data" "$install_dir/image"
if [ -f "$install_dir/.role" ] && [ "$(cat "$install_dir/.role")" != "$role" ]; then
  echo "existing Docker installation has role $(cat "$install_dir/.role")" >&2
  exit 1
fi

config_name='node.json'
if [ "$role" = 'controller' ]; then
  config_name='controller.json'
  if [ ! -f "$install_dir/config/$config_name" ]; then
    if [ -z "$public_url" ]; then echo '--public-url is required for a new Controller' >&2; exit 2; fi
    case "$public_url" in https://*) ;; *) echo '--public-url must use HTTPS' >&2; exit 2;; esac
    if [ -z "${XMESH_ADMIN_PASSWORD:-}" ]; then echo 'XMESH_ADMIN_PASSWORD is required for a new Controller' >&2; exit 2; fi
    for value in "$public_url" "$listen" "$admin_username" "$release_base_url" "$version"; do
      if printf '%s' "$value" | grep '[\\"]' >/dev/null; then echo 'arguments must not contain backslashes or double quotes' >&2; exit 2; fi
    done
    password_hash=$(XMESH_ADMIN_PASSWORD="$XMESH_ADMIN_PASSWORD" "$work/xmesh" hash-password)
    session_secret=$("$work/xmesh" generate-secret)
    cat >"$install_dir/config/$config_name" <<EOF
{
  "listen": "$listen",
  "public_url": "${public_url%/}",
  "state_path": "/var/lib/xmesh/controller-state.json",
  "admin_username": "$admin_username",
  "admin_password_hash": "$password_hash",
  "session_secret": "$session_secret",
  "release_base_url": "${release_base_url%/}",
  "release_version": "$version",
  "release_dir": "/var/lib/xmesh/releases",
  "node_offline_after_seconds": 45
}
EOF
  fi
else
  if [ "$rotate_credential" = true ] && [ ! -f "$install_dir/config/$config_name" ]; then
    echo '--rotate-credential requires an existing node installation' >&2
    exit 2
  fi
  if [ ! -f "$install_dir/config/$config_name" ] || [ "$rotate_credential" = true ]; then
    if [ -z "$controller" ]; then echo '--controller is required for a new node' >&2; exit 2; fi
    case "$controller" in https://*) ;; *) echo '--controller must use HTTPS' >&2; exit 2;; esac
    if [ -z "$token" ]; then
      if [ ! -r /dev/tty ]; then echo 'a terminal or --enrollment-token is required for a new node' >&2; exit 2; fi
      printf 'One-time enrollment token: ' >/dev/tty
      old_stty=$(stty -g </dev/tty)
      trap 'stty "$old_stty" </dev/tty; rm -rf "$work"' EXIT HUP INT TERM
      stty -echo </dev/tty
      IFS= read -r token </dev/tty
      stty "$old_stty" </dev/tty
      trap 'rm -rf "$work"' EXIT HUP INT TERM
      printf '\n' >/dev/tty
    fi
    if [ "$rotate_credential" = true ]; then
      printf '%s' "$token" | "$work/xmesh" enroll --controller "$controller" --role "$role" --token-stdin --replace --output "$install_dir/config/$config_name" --updater-output "$install_dir/updater/config.json" --updater-mode docker --updater-install-dir "$install_dir"
    else
      printf '%s' "$token" | "$work/xmesh" enroll --controller "$controller" --role "$role" --token-stdin --output "$install_dir/config/$config_name" --updater-output "$install_dir/updater/config.json" --updater-mode docker --updater-install-dir "$install_dir"
    fi
  fi
fi

install_node_updater_service() {
  if [ ! -f "$install_dir/updater/config.json" ]; then
    echo 'updater identity is missing; generate a pairing token in the Controller panel' >&2
    return
  fi
  if [ ! -d /run/systemd/system ] || ! command -v systemctl >/dev/null 2>&1; then
    echo 'node is running; remote upgrades require systemd on the Docker host' >&2
    return
  fi
  node_id=$(sed -n 's/.*"node_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$install_dir/config/node.json" | head -n 1)
  case "$node_id" in ''|*[!a-zA-Z0-9_-]*) echo 'invalid node ID for updater service' >&2; exit 1;; esac
  install -d -m 0700 "$install_dir/updater"
  install -m 0700 "$work/xmesh-updater" "$install_dir/updater/xmesh-updater.new"
  mv -f "$install_dir/updater/xmesh-updater.new" "$install_dir/updater/xmesh-updater"
  cat >"/etc/systemd/system/xmesh-updater-$node_id.service" <<EOF
[Unit]
Description=xmesh host upgrade assistant for $node_id
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$install_dir/updater/xmesh-updater run --config $install_dir/updater/config.json
Restart=always
RestartSec=10s

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable "xmesh-updater-$node_id.service"
  systemctl restart "xmesh-updater-$node_id.service"
}

if [ "$role" != controller ] && [ -f "$install_dir/compose.yaml" ] && [ -x "$install_dir/image/xmesh" ] && [ -f "$install_dir/updater/config.json" ]; then
  chown -R 10001:10001 "$install_dir/config" "$install_dir/data"
  node_id=$(sed -n 's/.*"node_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$install_dir/config/node.json" | head -n 1)
  case "$node_id" in ''|*[!a-zA-Z0-9_-]*) echo 'invalid node id' >&2; exit 1;; esac
  archive_sha=$(grep "  $archive\$" "$work/SHA256SUMS" | cut -d ' ' -f 1)
  systemctl stop "xmesh-updater-$node_id.service"
  if ! "$work/xmesh-updater" local --config "$install_dir/updater/config.json" --version "$version" --archive "$work/$archive" --sha256 "$archive_sha"; then
    systemctl restart "xmesh-updater-$node_id.service" 2>/dev/null || true
    exit 1
  fi
  install_node_updater_service
  echo "xmesh $role is running from $install_dir; logs: sudo docker compose -f $install_dir/compose.yaml logs -f"
  exit 0
fi

previous="$work/previous"
had_previous=false
if [ -f "$install_dir/compose.yaml" ] && [ -x "$install_dir/image/xmesh" ]; then
  had_previous=true
  mkdir -p "$previous/image"
  cp -p "$install_dir/compose.yaml" "$previous/compose.yaml"
  cp -p "$install_dir/image/xmesh" "$previous/image/xmesh"
  cp -p "$install_dir/image/xray" "$previous/image/xray"
  cp -p "$install_dir/image/Dockerfile" "$previous/image/Dockerfile"
  if [ "$role" = controller ]; then cp -p "$install_dir/config/controller.json" "$previous/controller.json"; fi
fi

if [ "$role" = controller ] && [ "$had_previous" = true ]; then
  grep -q '"release_version"[[:space:]]*:' "$install_dir/config/controller.json" || {
    echo 'existing Controller configuration has no release_version; update it manually before upgrading' >&2
    exit 1
  }
  sed "s/\"release_version\"[[:space:]]*:[[:space:]]*\"[^\"]*\"/\"release_version\": \"$version\"/" "$install_dir/config/controller.json" >"$work/controller-updated.json"
  install -m 0600 "$work/controller-updated.json" "$install_dir/config/controller.json"
fi

install -m 0755 "$work/xmesh" "$install_dir/image/xmesh"
install -m 0755 "$work/xray" "$install_dir/image/xray"
cat >"$install_dir/image/Dockerfile" <<'EOF'
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 xmesh \
    && useradd --uid 10001 --gid xmesh --home-dir /var/lib/xmesh --shell /usr/sbin/nologin xmesh
COPY xmesh /usr/local/bin/xmesh
COPY xray /usr/local/lib/xmesh/xray
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/xmesh"]
EOF

cat >"$install_dir/compose.yaml" <<EOF
services:
  xmesh:
    build: ./image
    image: xmesh-local:$version
    network_mode: host
    restart: unless-stopped
    command: ["$role", "-config", "/etc/xmesh/$config_name"]
    volumes:
      - ./config:/etc/xmesh:ro
      - ./data:/var/lib/xmesh
EOF
chown -R 10001:10001 "$install_dir/config" "$install_dir/data"
printf '%s\n' "$role" >"$install_dir/.role"
if ! (cd "$install_dir" && docker compose up -d --build); then
  started=false
else
  started=false
  attempt=0
  while [ "$attempt" -lt 10 ]; do
    container=$(cd "$install_dir" && docker compose ps -q xmesh)
    if [ -n "$container" ] && [ "$(docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null)" = true ]; then
      started=true
    else
      started=false
      break
    fi
    attempt=$((attempt + 1))
    sleep 1
  done
fi
if [ "$started" = true ]; then
  if [ "$role" = controller ]; then
    configured_listen=$(sed -n 's/.*"listen"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$install_dir/config/controller.json" | head -n 1)
    case "$configured_listen" in *:*) configured_port=${configured_listen##*:};; *) configured_port='';; esac
    if [ -z "$configured_port" ] || ! curl --fail --silent --show-error --max-time 10 "http://127.0.0.1:$configured_port/healthz" >/dev/null; then started=false; fi
  else
    credential=$(sed -n 's/.*"credential"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$install_dir/config/node.json" | head -n 1)
    started=false
    attempt=0
    while [ "$attempt" -lt 30 ]; do
      status=$(printf 'header = "Authorization: Bearer %s"\n' "$credential" | curl --config - --fail --silent --show-error --max-time 5 "${controller%/}/api/v1/self/status" 2>/dev/null) || status=''
      if printf '%s' "$status" | grep -Fq "\"binary_version\":\"$version\""; then
        started=true
        break
      fi
      attempt=$((attempt + 1))
      sleep 2
    done
  fi
fi
if [ "$started" != true ]; then
  echo 'new container failed its startup or Controller status check' >&2
  (cd "$install_dir" && docker compose logs --tail=50) >&2 || true
  if [ "$had_previous" = true ]; then
    cp -p "$previous/compose.yaml" "$install_dir/compose.yaml"
    cp -p "$previous/image/xmesh" "$install_dir/image/xmesh"
    cp -p "$previous/image/xray" "$install_dir/image/xray"
    cp -p "$previous/image/Dockerfile" "$install_dir/image/Dockerfile"
    if [ "$role" = controller ]; then cp -p "$previous/controller.json" "$install_dir/config/controller.json"; fi
    (cd "$install_dir" && docker compose up -d --build) || echo 'automatic rollback failed; inspect the previous installation' >&2
    echo 'previous image and Compose definition restored' >&2
  fi
  if [ "$role" != controller ] && [ -f "$install_dir/updater/config.json" ] && command -v systemctl >/dev/null 2>&1; then
    node_id=$(sed -n 's/.*"node_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$install_dir/config/node.json" | head -n 1)
    case "$node_id" in ''|*[!a-zA-Z0-9_-]*) ;; *) systemctl restart "xmesh-updater-$node_id.service" 2>/dev/null || true;; esac
  fi
  exit 1
fi
if [ "$role" = controller ]; then
  rm -f "$install_dir/data/controller-updater.ready"
  if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1 && command -v flock >/dev/null 2>&1 && command -v sort >/dev/null 2>&1; then
    case "$install_dir" in
      *[!a-zA-Z0-9_./-]*) echo 'Controller one-click updater requires an install directory without spaces or shell metacharacters' >&2 ;;
      *)
        install -d -m 0700 "$install_dir/updater"
        install -m 0700 "$work/controller-updater.sh" "$install_dir/updater/controller-updater.sh.new"
        mv -f "$install_dir/updater/controller-updater.sh.new" "$install_dir/updater/controller-updater.sh"
        cat >/etc/systemd/system/xmesh-controller-updater.service <<EOF
[Unit]
Description=xmesh Controller release updater
After=docker.service network-online.target

[Service]
Type=oneshot
ExecStart=/bin/sh $install_dir/updater/controller-updater.sh $install_dir
EOF
        cat >/etc/systemd/system/xmesh-controller-updater.timer <<'EOF'
[Unit]
Description=Check for approved xmesh Controller upgrade requests

[Timer]
OnBootSec=30s
OnUnitActiveSec=15s
AccuracySec=5s

[Install]
WantedBy=timers.target
EOF
        if systemctl daemon-reload && systemctl enable --now xmesh-controller-updater.timer; then
          printf 'ready\n' >"$install_dir/data/controller-updater.ready"
          chmod 0644 "$install_dir/data/controller-updater.ready"
        else
          echo 'Controller is running, but one-click updater timer could not be enabled' >&2
        fi
        ;;
    esac
  else
    echo 'Controller is running; one-click upgrades require systemd, flock, and sort on this host' >&2
  fi
fi
if [ "$role" != controller ]; then install_node_updater_service; fi
echo "xmesh $role is running from $install_dir; logs: sudo docker compose -f $install_dir/compose.yaml logs -f"
