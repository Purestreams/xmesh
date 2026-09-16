# XMesh

[English](README.en.md) · [下载 v0.1.0](https://github.com/Purestreams/xmesh/releases/tag/v0.1.0)

XMesh 把代理入口和实际出口分开：用户连接 Gateway，流量经过 Agent 从另一台机器访问目标；Controller 负责配置和状态，不转发业务流量。

```text
客户端 -- VMess/WS --> Gateway(Xray :8080)
                         │
                         └<-- WSS（Agent 主动连接）-- Agent --> 目标网络
Controller -- 配置/状态 --> Gateway、Agent
```

- **Controller**：管理面板、用户、节点、链路与订阅。
- **Gateway**：客户端入口。Xray 处理 VMess/WS，XMesh 将流量送入隧道。
- **Agent**：主动连接 Gateway，是实际 TCP/UDP 出口。

下面以三台 Debian/Ubuntu Linux 主机和 v0.1.0 为例；Controller、Gateway、Agent 分别部署。支持 amd64/arm64。Windows Release 只有独立可执行文件，没有节点安装器。选用 **systemd 或 Docker Compose 其中一种**，不要在同一主机运行两份相同角色。

## 1. 域名、端口和证书

先在 DNS 服务商添加记录，并等待解析生效：

| 域名示例 | 指向 | 用途 |
| --- | --- | --- |
| `panel.example.com` | Controller 公网 IP | HTTPS 面板和节点 API |
| `edge.example.com` | Gateway 公网 IP | Agent 使用 `wss://edge.example.com/tunnel`；客户端使用 `edge.example.com:8080` |

示例 Nginx 配置只监听 IPv4；需要 IPv6 时，补 `listen [::]:80` / `listen [::]:443 ssl` 并确认防火墙后再添加 AAAA 记录。开放 Controller 的 TCP 80/443、Gateway 的 TCP 80/443/8080；Agent 只需能向外连接上述两个 HTTPS 域名。80 用于申请/续期证书，443 用于 HTTPS/WSS。**不要向公网开放** Controller `8088`、Gateway `18080`（SOCKS）和 `18081`（隧道后端）；它们默认仅监听本机回环地址。客户端的 VMess/WS `:8080/proxy` 当前是明文入口，与 Agent 的加密 WSS 隧道不同；如需客户端侧 TLS，需另行设计入口与订阅配置。

三台主机先安装基础工具；仅 Controller 和 Gateway 主机需要安装 Nginx、Certbot：

```sh
sudo apt update
sudo apt install -y git wget curl tar coreutils
# 仅在 Controller 和 Gateway 主机运行：
sudo apt install -y nginx certbot
```

先为每个域名建一个仅用于取证的 HTTP 站点，并运行 Certbot。以下命令分别在 Controller 和 Gateway 主机执行，将 `DOMAIN` 设为各自的域名：

```sh
DOMAIN=panel.example.com  # Gateway 主机改成 edge.example.com
sudo mkdir -p /var/www/letsencrypt
printf 'server { listen 80; server_name %s; location ^~ /.well-known/acme-challenge/ { root /var/www/letsencrypt; } location / { return 404; } }\n' "$DOMAIN" | sudo tee /etc/nginx/conf.d/xmesh.conf
sudo nginx -t && sudo systemctl reload nginx
sudo certbot certonly --webroot -w /var/www/letsencrypt -d "$DOMAIN"
```

证书申请成功后，再按第 3、4 步将 `/etc/nginx/conf.d/xmesh.conf` 换成正式配置。不要在证书文件存在之前启用 443 配置。为 Certbot 的自动续期增加 Nginx 重载钩子，并检查续期：

```sh
printf '#!/bin/sh\nsystemctl reload nginx\n' | sudo tee /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh
sudo chmod 755 /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh
sudo certbot renew --dry-run
```

## 2. 安装 Controller

每台主机只需克隆已发布的固定 tag 一次；下面不使用 `wget | sh` 或移动的 `main` 分支：

```sh
git clone --depth 1 --branch v0.1.0 https://github.com/Purestreams/xmesh.git
cd xmesh
```

在 Controller 主机选择一种安装方式。以下命令在 Bash 中运行，密码只通过环境变量传给安装器，不写进命令行：

```bash
read -rsp '管理密码: ' XMESH_ADMIN_PASSWORD; echo
export XMESH_ADMIN_PASSWORD

# systemd：
sudo --preserve-env=XMESH_ADMIN_PASSWORD sh scripts/install-controller.sh \
  --version v0.1.0 --public-url https://panel.example.com

# 或 Docker Compose（先安装 Docker Engine 和 Compose 插件）：
# sudo --preserve-env=XMESH_ADMIN_PASSWORD sh scripts/install-docker.sh \
#   --role controller --version v0.1.0 --public-url https://panel.example.com

unset XMESH_ADMIN_PASSWORD
```

systemd 的配置和状态分别保存在 `/etc/xmesh/controller.json`、`/var/lib/xmesh-controller/`；Docker 默认保存在 `/opt/xmesh-docker-controller/config/`、`data/`。这些目录包含密钥，须限制权限并备份。安装器不会覆盖已有配置。

## 3. 为 Controller 配置 Nginx

在 Controller 主机，用以下内容替换 `/etc/nginx/conf.d/xmesh.conf`；证书域名应与 `--public-url` 一致：

```nginx
server {
    listen 80;
    server_name panel.example.com;
    location ^~ /.well-known/acme-challenge/ { root /var/www/letsencrypt; }
    location / { return 301 https://$host$request_uri; }
}
server {
    listen 443 ssl;
    server_name panel.example.com;
    ssl_certificate /etc/letsencrypt/live/panel.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/panel.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8088;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```

运行 `sudo nginx -t && sudo systemctl reload nginx`，然后访问 `https://panel.example.com/login`；`curl -fsS https://panel.example.com/healthz` 应返回 `{"status":"ok"}`。不要将 Controller 的 HTTP 8088 直接暴露到公网。

## 4. 配置 Gateway 的 WSS 入口

在 Gateway 主机，用以下内容替换该主机的 `/etc/nginx/conf.d/xmesh.conf`：

```nginx
server {
    listen 80;
    server_name edge.example.com;
    location ^~ /.well-known/acme-challenge/ { root /var/www/letsencrypt; }
    location / { return 301 https://$host$request_uri; }
}
server {
    listen 443 ssl;
    server_name edge.example.com;
    ssl_certificate /etc/letsencrypt/live/edge.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/edge.example.com/privkey.pem;

    location = /tunnel {
        proxy_pass http://127.0.0.1:18081;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
        proxy_buffering off;
    }
    location / { return 404; }
}
```

运行 `sudo nginx -t && sudo systemctl reload nginx`。安装 Gateway 前 `/tunnel` 返回 502 是正常的；安装并上线后，Nginx 才能连接本机的 `127.0.0.1:18081`。不要把这段 Nginx 配置代理到 Xray 的 8080：8080 是客户端 VMess/WS 入口。

## 5. 创建链路并安装 Gateway/Agent

登录 Controller 面板，按顺序：

1. 创建 Gateway（Public host=`edge.example.com`、VMess port=`8080`、WS path=`/proxy`）和 Agent。
2. 创建两者之间的 **Node** 关联，再创建 **Link**：URL=`wss://edge.example.com/tunnel`，HTTP Host/TLS server name=`edge.example.com`，保持 TLS verify 开启。
3. 创建 User 和 Grant，将用户授权到该 Node。节点实际运行并应用配置后，订阅才会发布可用项。
4. 对 Gateway 和 Agent 分别点击 **Generate one-time install command**，取得各自的一次性令牌（30 分钟有效）；不要把令牌写进仓库或直接敲入 shell 历史。

在 Gateway、Agent 各自的主机克隆同一 tag（见第 2 步），然后以 Bash 读取该主机的令牌并选择安装方式：

```bash
ROLE=gateway  # Agent 主机改成 agent
read -rsp '一次性令牌: ' ENROLLMENT_TOKEN; echo

# systemd：
sudo sh scripts/install.sh --controller https://panel.example.com \
  --role "$ROLE" --enrollment-token "$ENROLLMENT_TOKEN" \
  --version v0.1.0 \
  --release-base-url https://github.com/Purestreams/xmesh/releases/download

# 或 Docker Compose：
# sudo sh scripts/install-docker.sh --role "$ROLE" --version v0.1.0 \
#   --controller https://panel.example.com --enrollment-token "$ENROLLMENT_TOKEN"

unset ENROLLMENT_TOKEN
```

安装器会从 [v0.1.0 Release](https://github.com/Purestreams/xmesh/releases/tag/v0.1.0) 下载与架构匹配的包并校验 `SHA256SUMS`。Docker 方式使用主机网络，配置/数据默认放在 `/opt/xmesh-docker-<role>/config/` 和 `data/`；不需要再配置端口映射。Agent 必须能访问 Controller HTTPS 和 Gateway WSS。

## 6. 验收与排障

- 面板中的 Gateway、Agent、Link 应显示 `online/ready`，配置版本应为 `applied`；Gateway 的 Xray 应为 ready。
- systemd：`sudo systemctl status xmesh`（节点）、`sudo systemctl status xmesh-controller`（Controller）；日志使用 `sudo journalctl -u xmesh -f` 或 `sudo journalctl -u xmesh-controller -f`。
- Docker：进入对应的 `/opt/xmesh-docker-<role>` 后运行 `sudo docker compose ps`、`sudo docker compose logs -f`。
- 客户端使用面板给出的订阅；客户端地址为 `edge.example.com:8080`、路径 `/proxy`，并非 WSS 的 `/tunnel`。若 Link 离线，先检查 DNS、证书、443 防火墙和 Nginx 的 WebSocket Upgrade 转发。

高 RTT 链路还应检查主机 TCP 缓冲区，见[高延迟部署说明](docs/deployment.md#high-latency-links)。更多实现细节见[架构文档](docs/architecture.md)。

## 开发

项目使用 mise 固定 Go 1.27.1：

```sh
mise install
mise exec -- go test ./...
mise exec -- go build ./cmd/xmesh
```

请勿提交节点凭据、Controller 状态、私钥或本地配置。
