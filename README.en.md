# XMesh

[简体中文](README.md) · [Download v0.1.0](https://github.com/Purestreams/xmesh/releases/tag/v0.1.0)

XMesh separates the proxy entry point from the network exit. Clients connect to a Gateway; traffic reaches its destination through an Agent. The Controller manages configuration and status but does not carry user traffic.

```text
Client -- VMess/WS --> Gateway (Xray :8080)
                         │
                         └<-- WSS (Agent-initiated) -- Agent --> destination
Controller -- config/status --> Gateway, Agent
```

- **Controller**: admin panel, users, nodes, links, and subscriptions.
- **Gateway**: client entry point. Xray accepts VMess/WS; XMesh forwards it into the tunnel.
- **Agent**: initiates the tunnel and provides the actual TCP/UDP exit.

This guide uses three separate Debian/Ubuntu Linux hosts and release v0.1.0. Linux amd64 and arm64 are supported. The Windows release contains a standalone executable, not a node installer. Choose **either systemd or Docker Compose** per host; do not run both copies of the same role on one host.

## 1. DNS, ports, and certificates

Create these DNS records first and wait for them to resolve:

| Example name | Points to | Purpose |
| --- | --- | --- |
| `panel.example.com` | Controller public IP | HTTPS admin panel and node API |
| `edge.example.com` | Gateway public IP | Agent tunnel at `wss://edge.example.com/tunnel`; clients at `edge.example.com:8080` |

The example Nginx configuration listens on IPv4 only. For IPv6, add `listen [::]:80` / `listen [::]:443 ssl`, verify the firewall, and only then publish AAAA records. Allow inbound TCP 80/443 on the Controller and TCP 80/443/8080 on the Gateway. The Agent only needs outbound access to both HTTPS names. Port 80 is used for certificate issuance and renewal; 443 is HTTPS/WSS. **Do not expose** Controller port 8088 or Gateway ports 18080 (SOCKS) and 18081 (tunnel backend); they bind to loopback by default. The client-facing VMess/WS endpoint `:8080/proxy` is currently cleartext and separate from the encrypted Agent WSS tunnel. Client-side TLS would require a separately designed entry point and subscription configuration.

Install the base tools on all three hosts. Only the Controller and Gateway need Nginx and Certbot:

```sh
sudo apt update
sudo apt install -y git wget curl tar coreutils
# Run only on the Controller and Gateway hosts:
sudo apt install -y nginx certbot
```

First create a minimal HTTP site for certificate validation. Run this on both hosts, setting `DOMAIN` to the name for that host:

```sh
DOMAIN=panel.example.com  # Use edge.example.com on the Gateway.
sudo mkdir -p /var/www/letsencrypt
printf 'server { listen 80; server_name %s; location ^~ /.well-known/acme-challenge/ { root /var/www/letsencrypt; } location / { return 404; } }\n' "$DOMAIN" | sudo tee /etc/nginx/conf.d/xmesh.conf
sudo nginx -t && sudo systemctl reload nginx
sudo certbot certonly --webroot -w /var/www/letsencrypt -d "$DOMAIN"
```

After the certificate exists, replace `/etc/nginx/conf.d/xmesh.conf` with the appropriate complete configuration in steps 3 and 4. Do not enable the 443 configuration before the certificate files exist. Add a deploy hook to reload Nginx after automatic renewal and test renewal:

```sh
printf '#!/bin/sh\nsystemctl reload nginx\n' | sudo tee /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh
sudo chmod 755 /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh
sudo certbot renew --dry-run
```

## 2. Install the Controller

Clone the fixed release tag once on each host. This avoids piping a download into a shell or relying on the moving `main` branch:

```sh
git clone --depth 1 --branch v0.1.0 https://github.com/Purestreams/xmesh.git
cd xmesh
```

On the Controller host, choose one method. Run these commands in Bash; the password is passed through an environment variable, not on the command line:

```bash
read -rsp 'Admin password: ' XMESH_ADMIN_PASSWORD; echo
export XMESH_ADMIN_PASSWORD

# systemd:
sudo --preserve-env=XMESH_ADMIN_PASSWORD sh scripts/install-controller.sh \
  --version v0.1.0 --public-url https://panel.example.com

# Or Docker Compose (install Docker Engine and the Compose plugin first):
# sudo --preserve-env=XMESH_ADMIN_PASSWORD sh scripts/install-docker.sh \
#   --role controller --version v0.1.0 --public-url https://panel.example.com

unset XMESH_ADMIN_PASSWORD
```

With systemd, the configuration and state live in `/etc/xmesh/controller.json` and `/var/lib/xmesh-controller/`. Docker uses `/opt/xmesh-docker-controller/config/` and `data/`. These paths contain secrets: restrict access and back them up. Installers do not overwrite an existing configuration.

## 3. Configure Nginx for the Controller

On the Controller host, replace `/etc/nginx/conf.d/xmesh.conf` with the following. The certificate name must match `--public-url`:

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

Run `sudo nginx -t && sudo systemctl reload nginx`, then open `https://panel.example.com/login`. `curl -fsS https://panel.example.com/healthz` should return `{"status":"ok"}`. Never expose the Controller's HTTP port 8088 directly.

## 4. Configure the Gateway WSS entry point

On the Gateway host, replace that host's `/etc/nginx/conf.d/xmesh.conf` with:

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

Run `sudo nginx -t && sudo systemctl reload nginx`. A 502 on `/tunnel` is expected until the Gateway starts listening on `127.0.0.1:18081`. Do not proxy this path to Xray on port 8080: that port is the client VMess/WS entry point.

## 5. Create a link and install the Gateway and Agent

Sign in to the Controller and proceed in order:

1. Create a Gateway (Public host=`edge.example.com`, VMess port=`8080`, WS path=`/proxy`) and an Agent.
2. Create a **Node** association between them, then a **Link** with URL=`wss://edge.example.com/tunnel`, HTTP Host/TLS server name=`edge.example.com`, and TLS verify enabled.
3. Create a User and a Grant for that Node. A usable subscription appears after the nodes run and apply their configuration.
4. Click **Generate one-time install command** for each Gateway and Agent to get separate tokens, valid for 30 minutes. Do not put tokens in the repository or type them directly into shell history.

Clone the same tag on the Gateway and Agent hosts (step 2), then read that host's token in Bash and choose an installation method:

```bash
ROLE=gateway  # Use agent on the Agent host.
read -rsp 'One-time token: ' ENROLLMENT_TOKEN; echo

# systemd:
sudo sh scripts/install.sh --controller https://panel.example.com \
  --role "$ROLE" --enrollment-token "$ENROLLMENT_TOKEN" \
  --version v0.1.0 \
  --release-base-url https://github.com/Purestreams/xmesh/releases/download

# Or Docker Compose:
# sudo sh scripts/install-docker.sh --role "$ROLE" --version v0.1.0 \
#   --controller https://panel.example.com --enrollment-token "$ENROLLMENT_TOKEN"

unset ENROLLMENT_TOKEN
```

The installers download the matching architecture from the [v0.1.0 release](https://github.com/Purestreams/xmesh/releases/tag/v0.1.0) and verify `SHA256SUMS`. Docker uses host networking and stores configuration/data under `/opt/xmesh-docker-<role>/config/` and `data/`; no port mapping is needed. The Agent must reach the Controller over HTTPS and the Gateway over WSS.

## 6. Verify and troubleshoot

- The Controller should show the Gateway, Agent, and Link as `online/ready`, with configuration version `applied`; Gateway Xray should be ready.
- systemd: `sudo systemctl status xmesh` for nodes or `sudo systemctl status xmesh-controller` for the Controller. Follow logs with `sudo journalctl -u xmesh -f` or `sudo journalctl -u xmesh-controller -f`.
- Docker: enter the corresponding `/opt/xmesh-docker-<role>` directory and run `sudo docker compose ps` or `sudo docker compose logs -f`.
- Use the subscription from the panel. The client endpoint is `edge.example.com:8080` with path `/proxy`, **not** the WSS path `/tunnel`. If the Link stays offline, check DNS, certificate validity, port 443, and Nginx WebSocket Upgrade forwarding.

For high-RTT links, also check host TCP buffers; see [high-latency deployment](docs/deployment.md#high-latency-links). More internals are in the [architecture guide](docs/architecture.md).

## Development

mise pins Go 1.27.1:

```sh
mise install
mise exec -- go test ./...
mise exec -- go build ./cmd/xmesh
```

Do not commit node credentials, Controller state, private keys, or local configuration.
