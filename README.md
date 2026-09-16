# xmesh

xmesh is a small reverse-proxy control plane and data plane made of three roles:

- `controller`: single-admin HTTP panel, desired state, subscriptions, enrollment, and status.
- `gateway`: local authenticated SOCKS5 handoff plus inbound Agent tunnels.
- `agent`: outbound tunnel client and the actual TCP/UDP network exit.

The public VMess/WebSocket listener remains an independent Xray process. Every user-facing
VMess node on a Gateway shares the same cleartext WS listener (default `:8080`). Xray hands
authenticated traffic to the Gateway's loopback-only SOCKS5 listener. Agent tunnels use
WebSocket Secure plus smux protocol version 2.

## 部署前准备

部署顺序是 Controller → 在面板创建 Gateway、Agent、Node、Link 与 Grant → 安装 Gateway/Agent。需要：

- Debian/Ubuntu Linux（amd64 或 arm64）、`wget`/`tar`/`sha256sum`、可用的 HTTPS 域名和可信证书；Docker 方式还需要 Docker Engine 与 Compose 插件。
- Controller 对外提供 HTTPS 面板和节点 API；进程本身仅提供 HTTP，应绑定 `127.0.0.1:8088`，由同机反向代理终止 TLS。不要直接暴露 8088。
- Gateway 对客户端开放明文 VMess/WebSocket（默认 8080，按需限制来源）；Agent 连接的 `/tunnel` 必须经独立的 HTTPS/WSS 反向代理或 CDN 转发到 Gateway 的 `127.0.0.1:18081`。不要把内部 SOCKS5 端口 18080 暴露到公网。
- 下方的安装器需要一个 GitHub Release 标签（示例 `v0.1.0`），其资源必须有 `xmesh-v0.1.0-linux-amd64.tar.gz`、`xmesh-v0.1.0-linux-arm64.tar.gz`、`SHA256SUMS`。每个 tar 包需包含 `xmesh` 和经过验证的 Xray 二进制。发布包另附 Windows amd64 独立可执行文件，但没有 Windows 节点安装器。CI 中的 Actions artifacts **不是**这些安装包，不能直接作为 `--version` 使用。

维护者可在仓库使用 `mise install`，取得与 `versions.env` 匹配的 Linux amd64/arm64 Xray 二进制，然后运行：

```sh
XRAY_AMD64=/path/to/xray-amd64 XRAY_ARM64=/path/to/xray-arm64 \
  mise exec -- sh scripts/package.sh v0.1.0
```

核对 `dist/` 中的两个压缩包、Windows 可执行文件与 `SHA256SUMS`，将它们及 `dist/install.sh` 上传至同名 GitHub Release。先完成发布，再运行以下安装命令；使用其他仓库发布时通过 `--release-base-url` 指向其 `/releases/download`。下载脚本前建议固定为已审核的 Git commit Raw URL，而不是长期使用移动的 `main`。

## 方式一：systemd 部署

以下命令在各自的 Debian/Ubuntu 主机上执行，示例版本与域名应替换为实际值。通过 `wget` 把 Raw 脚本保存到本机，先检查内容，再以 root 运行；不使用 `wget | sh`。

Controller 主机：

```sh
wget -q --https-only https://raw.githubusercontent.com/Purestreams/xmesh/main/scripts/install-controller.sh -O install-controller.sh
less install-controller.sh
read -r -s -p 'Admin password: ' XMESH_ADMIN_PASSWORD; echo
export XMESH_ADMIN_PASSWORD
sudo --preserve-env=XMESH_ADMIN_PASSWORD sh ./install-controller.sh \
  --version v0.1.0 --public-url https://panel.example.com
unset XMESH_ADMIN_PASSWORD
sudo systemctl status xmesh-controller
```

安装器生成 `/etc/xmesh/controller.json` 和 `/var/lib/xmesh-controller/controller-state.json`；随后配置 Nginx/Caddy 等将 `https://panel.example.com` 代理到 `127.0.0.1:8088`。保留 Host 与请求路径，做好状态文件备份。初次安装时密码不会出现在命令行；已有配置不会被安装器覆盖。

在面板创建 Gateway、Agent 和两者的 Node 关联，再创建 Link（例如 `wss://edge.example.com/tunnel`，勾选 TLS 验证），最后创建 User 与 Grant。面板的 **Generate one-time install command** 会为每台 Gateway/Agent 生成一次性、30 分钟有效的安装命令；它通过 `curl` 下载 Release 的 `install.sh`，该脚本会校验下载包的 SHA256。不要公开或保存包含令牌的命令。没有安装 `curl` 时，可以通过 Raw 预先下载、审核节点脚本，再用面板生成的令牌运行（Gateway 主机上将 `gateway` 替换为 `agent` 即为 Agent 安装）：

```sh
wget -q --https-only https://raw.githubusercontent.com/Purestreams/xmesh/main/scripts/install.sh -O install.sh
less install.sh
read -r -s -p 'One-time token: ' ENROLLMENT_TOKEN; echo
sudo sh ./install.sh --controller https://panel.example.com --role gateway \
  --enrollment-token "$ENROLLMENT_TOKEN" --version v0.1.0 \
  --release-base-url https://github.com/Purestreams/xmesh/releases/download
unset ENROLLMENT_TOKEN
```

节点安装完成后检查 `sudo systemctl status xmesh`、`sudo journalctl -u xmesh -f`。Gateway 的 Xray 默认对外监听 8080；WSS 反代需要在 Gateway 主机上把 `/tunnel` 转发到 `127.0.0.1:18081`。确保 Agent 能访问 Controller HTTPS 与 WSS 地址，Gateway 能访问 Controller HTTPS。

## 方式二：Docker Compose 部署

Docker 脚本同样要求上述 GitHub Release 资源；它在 Linux amd64/arm64 主机上生成本地镜像、配置与 Compose 文件，使用 `network_mode: host`，所以与 systemd 方式使用相同的端口和 WSS 反代，不需要特权容器。三种角色应使用独立目录，通常部署在各自的主机；不要在同一主机同时启动 systemd 与 Docker 版本占用相同端口。

在每台主机下载、审核脚本：

```sh
wget -q --https-only https://raw.githubusercontent.com/Purestreams/xmesh/main/scripts/install-docker.sh -O install-docker.sh
less install-docker.sh
```

Controller：

```sh
read -r -s -p 'Admin password: ' XMESH_ADMIN_PASSWORD; echo
export XMESH_ADMIN_PASSWORD
sudo --preserve-env=XMESH_ADMIN_PASSWORD sh ./install-docker.sh \
  --role controller --version v0.1.0 --public-url https://panel.example.com
unset XMESH_ADMIN_PASSWORD
cd /opt/xmesh-docker-controller && sudo docker compose logs -f
```

配置 HTTPS 反向代理后，在面板创建 Gateway/Agent 并为每台生成一次性令牌。分别在相应主机执行：

```sh
read -r -s -p 'One-time token: ' ENROLLMENT_TOKEN; echo
sudo sh ./install-docker.sh --role gateway --version v0.1.0 \
  --controller https://panel.example.com --enrollment-token "$ENROLLMENT_TOKEN"
unset ENROLLMENT_TOKEN
# 在 Agent 主机以同样方式读取 Agent 令牌，然后将 --role 换为 agent。
```

Gateway 主机还需配置可信 HTTPS/WSS 反代到 `127.0.0.1:18081`，并开放用户连接的 VMess 端口。安装目录分别为 `/opt/xmesh-docker-controller`、`/opt/xmesh-docker-gateway`、`/opt/xmesh-docker-agent`；`config/` 中有身份凭据，`data/` 是持久状态，务必备份并限制权限。查看状态用 `cd /opt/xmesh-docker-<role> && sudo docker compose ps`；更新时以同一 `--role`、新 `--version` 重跑脚本，已有配置与数据会保留，但 Controller 的 `release_version` 仍需在其配置中手动更新。停止可在安装目录运行 `sudo docker compose down`，不要加 `-v` 删除数据。

安装器不会自动调整主机内核的 TCP 缓冲区；400 ms RTT 链路的设置见[高延迟部署说明](docs/deployment.md#high-latency-links)。详细的信任边界与运行方式参见[部署文档](docs/deployment.md)和[架构](docs/architecture.md)。

## Development

The repository pins Go 1.27.1 with mise:

```sh
mise install
mise exec -- go test ./...
mise exec -- go build ./cmd/xmesh
```

Never commit generated node credentials, enrollment scripts, runtime state, TLS private keys,
or local configuration. See `.gitignore`.

See [deployment](docs/deployment.md) and [architecture](docs/architecture.md) for the operational
model and trust boundaries.
