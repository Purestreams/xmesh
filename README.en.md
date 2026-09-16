# XMesh

[简体中文](README.md) · [Download v0.2.0](https://github.com/Purestreams/xmesh/releases/tag/v0.2.0)

## What is it for?

Some machines are suitable as the actual proxy exit (proxy endpoint) but have no public IP, or sit behind NAT, so clients cannot connect to them directly. XMesh lets these machines serve as proxy exits: deploy a publicly reachable Gateway elsewhere, and the Agent on the private machine initiates a REALITY tunnel to it. The Agent needs neither a public IP nor an inbound port mapping.

Clients still connect to the Gateway. It forwards authenticated traffic through the tunnel to the Agent, which accesses the destination network. The Controller manages configuration, authorization, and status; it does not carry user traffic. **The Gateway still needs an address reachable by clients; XMesh does not eliminate the need for every public entry point.**

```text
Client -- VMess/WS --> Gateway (public entry) -- tunnel traffic --> Agent (no public IP needed) --> destination
                            ^--------------- Agent initiates REALITY ---------------
Controller -- config/status --> Gateway, Agent
```

- **Controller**: admin panel, users, nodes, links, and subscriptions.
- **Gateway**: client entry point. Xray accepts VMess/WS; XMesh forwards it into the tunnel.
- **Agent**: initiates the tunnel and provides the actual TCP/UDP exit.

This guide uses three separate Debian/Ubuntu Linux hosts and release v0.2.0. Linux amd64 and arm64 are supported. The Windows release contains a standalone executable, not a node installer. Choose **either systemd or Docker Compose** per host; do not run both copies of the same role on one host. New links use REALITY; existing `wss://` links remain supported but need a separately managed TLS termination service.

## 1. DNS, ports, and certificates

Create these DNS records first and wait for them to resolve:

| Example name | Points to | Purpose |
| --- | --- | --- |
| `panel.example.com` | Controller public IP | HTTPS admin panel and node API |
| `edge.example.com` | Gateway public IP | Agent tunnel at `reality://edge.example.com:8443/tunnel`; clients at `edge.example.com:8080` |

Allow inbound TCP 80/443 on the Controller and TCP 8080/8443 on the Gateway. The Agent needs outbound access to Controller HTTPS, the Gateway REALITY port, and the destination network it serves. Controller port 80 is for certificate issuance and renewal. Gateway's bundled Xray listens for REALITY directly on 8443; no Gateway Nginx or certificate for `edge.example.com` is required. **Do not expose** Controller port 8088 or Gateway ports 18080 (SOCKS) and 18081 (tunnel backend); they bind to loopback by default. The client-facing VMess/WS endpoint `:8080/proxy` is currently cleartext and separate from the encrypted Agent REALITY tunnel. Client-side TLS would require a separately designed entry point and subscription configuration.

Install the base tools on all three hosts. Only the Controller needs Nginx and Certbot:

```sh
sudo apt update
sudo apt install -y git wget curl tar coreutils
# Run only on the Controller host:
sudo apt install -y nginx certbot
```

First create a minimal HTTP site for the Controller certificate validation. Run this only on the Controller host:

```sh
DOMAIN=panel.example.com
sudo mkdir -p /var/www/letsencrypt
printf 'server { listen 80; server_name %s; location ^~ /.well-known/acme-challenge/ { root /var/www/letsencrypt; } location / { return 404; } }\n' "$DOMAIN" | sudo tee /etc/nginx/conf.d/xmesh.conf
sudo nginx -t && sudo systemctl reload nginx
sudo certbot certonly --webroot -w /var/www/letsencrypt -d "$DOMAIN"
```

After the certificate exists, replace `/etc/nginx/conf.d/xmesh.conf` with the complete configuration in step 3. Do not enable the 443 configuration before the certificate files exist. Add a deploy hook to reload Nginx after automatic renewal and test renewal:

```sh
printf '#!/bin/sh\nsystemctl reload nginx\n' | sudo tee /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh
sudo chmod 755 /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh
sudo certbot renew --dry-run
```

## 2. Install the Controller

Clone the fixed release tag once on each host. This avoids piping a download into a shell or relying on the moving `main` branch:

```sh
git clone --depth 1 --branch v0.2.0 https://github.com/Purestreams/xmesh.git
cd xmesh
```

On the Controller host, choose one method. Run these commands in Bash; the password is passed through an environment variable, not on the command line:

```bash
read -rsp 'Admin password: ' XMESH_ADMIN_PASSWORD; echo
export XMESH_ADMIN_PASSWORD

# systemd:
sudo --preserve-env=XMESH_ADMIN_PASSWORD sh scripts/install-controller.sh \
  --version v0.2.0 --public-url https://panel.example.com

# Or Docker Compose (install Docker Engine and the Compose plugin first):
# sudo --preserve-env=XMESH_ADMIN_PASSWORD sh scripts/install-docker.sh \
#   --role controller --version v0.2.0 --public-url https://panel.example.com

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

## 4. Prepare the Gateway REALITY entry point

Gateway's bundled Xray accepts Agent REALITY connections on `0.0.0.0:8443` and passes authenticated traffic to the loopback-only tunnel handler. Allow TCP 8443 through the firewall. There is no need to install Nginx on the Gateway, expose port 18081, or obtain a certificate for `edge.example.com`. The default systemd service runs as an unprivileged user; using port 443 instead requires a port mapping or explicit privileged-listener setup.

The Controller also needs a **REALITY target**, such as `www.example.com:443`, that is reachable from the Gateway and supports TLS 1.3. Xray uses that site as the handshake target; its certificate must match its actual hostname. `edge.example.com` in the Link URL is the Gateway address, not the camouflage SNI. Do not select a target you are not authorized to use or cannot reliably reach.

## 5. Create a link and install the Gateway and Agent

Sign in to the Controller and proceed in order:

1. Create a Gateway (Public host=`edge.example.com`, VMess port=`8080`, WS path=`/proxy`) and an Agent.
2. Create a **Node** association between them, then a **Link** with URL=`reality://edge.example.com:8443/tunnel` and REALITY target=`www.example.com:443`. The Controller generates an X25519 Gateway key pair plus a per-Link VLESS UUID and short ID, and sends the required public parameters to the Agent; no certificate fingerprint needs to be copied manually. The panel's **Create route** action can create the Gateway, Agent, Node, and Link together.
3. Create a User and a Grant for that Node. A usable subscription appears after the nodes run and apply their configuration.
4. Click **Generate one-time install command** for each Gateway and Agent to get separate tokens, valid for 30 minutes. Do not put tokens in the repository or type them directly into shell history.

Clone the same tag on the Gateway and Agent hosts (step 2), then read that host's token in Bash and choose an installation method:

```bash
ROLE=gateway  # Use agent on the Agent host.
read -rsp 'One-time token: ' ENROLLMENT_TOKEN; echo

# systemd:
sudo sh scripts/install.sh --controller https://panel.example.com \
  --role "$ROLE" --enrollment-token "$ENROLLMENT_TOKEN" \
  --version v0.2.0 \
  --release-base-url https://github.com/Purestreams/xmesh/releases/download

# Or Docker Compose:
# sudo sh scripts/install-docker.sh --role "$ROLE" --version v0.2.0 \
#   --controller https://panel.example.com --enrollment-token "$ENROLLMENT_TOKEN"

unset ENROLLMENT_TOKEN
```

The installers download the matching architecture from the [v0.2.0 release](https://github.com/Purestreams/xmesh/releases/tag/v0.2.0) and verify `SHA256SUMS`. Docker uses host networking and stores configuration/data under `/opt/xmesh-docker-<role>/config/` and `data/`; no port mapping is needed. The Agent must reach the Controller over HTTPS and the Gateway's REALITY port. The panel also offers install links via the Controller's on-demand release cache when nodes have unreliable GitHub access; the Controller itself must be able to reach GitHub.

## 6. Verify and troubleshoot

- The Controller should show the Gateway, Agent, and Link as `online/ready`, with configuration version `applied`; Gateway Xray should be ready.
- systemd: `sudo systemctl status xmesh` for nodes or `sudo systemctl status xmesh-controller` for the Controller. Follow logs with `sudo journalctl -u xmesh -f` or `sudo journalctl -u xmesh-controller -f`.
- Docker: enter the corresponding `/opt/xmesh-docker-<role>` directory and run `sudo docker compose ps` or `sudo docker compose logs -f`.
- Use the subscription from the panel. The client endpoint is `edge.example.com:8080` with path `/proxy`, **not** the Agent's REALITY port 8443. If the Link stays offline, check the Gateway 8443 firewall, TLS 1.3 reachability of the REALITY target, applied Gateway/Agent configuration, and Xray status.

For high-RTT links, also check host TCP buffers; see [high-latency deployment](docs/deployment.md#high-latency-links). More internals are in the [architecture guide](docs/architecture.md).

## Development

mise pins Go 1.27.1:

```sh
mise install
mise exec -- go test ./...
mise exec -- go build ./cmd/xmesh
```

Do not commit node credentials, Controller state, private keys, or local configuration.

For a Docker-based end-to-end check, run `pwsh tests/reality/multicontainer.ps1`. It starts separate Controller, Gateway, Agent, client, and target containers and verifies TCP/UDP echo traffic through REALITY.
