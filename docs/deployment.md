# Deployment

## Controller

The Controller intentionally serves HTTP only. Bind it to loopback or a private address and put
your own HTTPS reverse proxy in front of it.

1. Build with the repository-pinned toolchain: `mise exec -- go build -o bin/xmesh ./cmd/xmesh`.
2. Generate the admin password hash without putting the password on the command line:
   `XMESH_ADMIN_PASSWORD='...' bin/xmesh hash-password`.
3. Generate `session_secret` with `bin/xmesh generate-secret`.
4. Copy `configs/controller.example.json`, replace every placeholder, protect it with mode 0600,
   and start `bin/xmesh controller -config /etc/xmesh/controller.json`.
5. Configure Nginx or another reverse proxy for HTTPS. Preserve the request path and Host header.

`state_path` contains credentials and must be backed up and readable only by the Controller user.
The Controller does not terminate TLS and business traffic never passes through it.

Set `release_base_url` to the HTTPS GitHub Release download base, `release_version` to an exact
tag, and `release_dir` to a writable cache directory (for example,
`/var/lib/xmesh-controller/releases` for systemd or `/var/lib/xmesh/releases` for Docker).
The Controller serves `GET /releases/<version>/<asset>` publicly. On the first request for a
versioned asset, it downloads `SHA256SUMS` and that asset from GitHub, verifies the asset hash,
then atomically caches it on disk. Later requests use the cached copy. The download is capped at
256 MiB per asset; an upstream failure or checksum mismatch returns 502 and does not cache the
bad file. No release binary is embedded in the Controller executable. Ensure the reverse proxy
allows these paths and large downloads; for `/releases/`, set the upstream read timeout to at
least 10 minutes and disable proxy response buffering so the first slow GitHub fetch is not cut
off or spooled to the proxy's temporary disk. Size the cache for both Linux architectures and
Windows.
The Controller host must be able to reach GitHub over HTTPS, but Gateway and Agent hosts can use
the Controller URL when their GitHub connection is unreliable. Existing Controller installations
must add `release_dir` to their preserved configuration and restart; the installers only set it
for a new configuration. Hosts running the install commands need `curl`, `sha256sum` and `tar`;
Docker mode additionally needs Docker Engine with the Compose plugin.

Pushing a `v*` tag runs the release workflow: it tests the code, fetches the Xray version pinned
in `versions.env`, checks the upstream SHA-256 digest, builds Linux amd64/arm64 and Windows amd64,
and publishes the archives, installers and `SHA256SUMS` to the matching GitHub Release. Set the
Controller's `release_version` to that published tag. Do not reuse an old tag whose manifest does
not cover the installer scripts.

## Gateway and Agent

Use **Create route** in the Panel to make a Gateway, Agent, attachment and TLS-verified WSS Link
in one operation, or configure them separately below. Review the Gateway host/port/path, Agent
CIDR rules and WSS address carefully before submitting: the Panel does not currently edit those
fields after creation. Generate an install command for each node.
The Panel offers both Controller-cache and GitHub URLs, each with systemd and Docker Compose
commands. The one-time token expires after 30 minutes, can only be used once, and is entered at
the target host prompt rather than included in the shell command. The command verifies the
installer script against `SHA256SUMS`; the installer verifies the architecture-specific archive.
Generating another token for the same node revokes its previous unused token; the Panel can also
revoke an active token explicitly.
The Panel's readiness table shows enrollment, online status, applied version and runtime health;
Link readiness and grant publication remain visible in their detailed tables.

The generic installer:

- accepts Debian and Ubuntu on amd64 or arm64;
- verifies the release archive against `SHA256SUMS`;
- refuses to overwrite an existing node identity or change its role;
- installs a hardened systemd service under the dedicated `xmesh` user;
- preserves identity and state during upgrades and ordinary uninstall;
- only removes identity/state when `--purge` is explicit.

Gateway release archives include the Xray build validated for that xmesh release. The Gateway
generates one VMess/WebSocket/no-TLS inbound on the configured port (8080 by default), validates
the generated Xray configuration, and supervises that Xray process. Its internal SOCKS5 and Agent
tunnel listeners bind to loopback by default.

The currently validated Xray release is recorded in `versions.env`; packaging refuses a different
binary. Its generated VMess, WebSocket, SOCKS outbound, and per-user routing configuration is also
checked by an opt-in test against the real Xray executable (`XMESH_TEST_XRAY=/path/to/xray go test
./internal/gateway -run TestGeneratedConfigAcceptedByXray`).

The generator intentionally uses the schema accepted by that pinned stable release (`network:
"ws"`, VMess `settings.clients`, and SOCKS `settings.servers`). Do not update those fields from
newer online examples without advancing `XRAY_VERSION` and rerunning the full real-Xray test.

For a REALITY Agent Link, enter `reality://<gateway-public-host-or-IP>:8443/tunnel` and a reachable
TLS 1.3 target such as `<target-host>:443` in the Controller. The Controller generates a Gateway
X25519 key pair and a per-Link VLESS UUID and short ID, then sends only the public key and Link
identity to the Agent. The Gateway's existing Xray process listens on
`gateway.reality_listen` (`0.0.0.0:8443` by default), forwards authenticated connections to
the loopback tunnel handler, and forwards unauthenticated connections to the target. The Agent
embeds the pinned Xray-core implementation for REALITY; it does not spawn a helper process.
The public port in the Link URL must reach `gateway.reality_listen`. Port 443 requires a
privileged listener or external port mapping with the default unprivileged service account.
Use a target whose TLS certificate matches the configured name, and check its reachability
from the Gateway. A chosen target can change behavior over time, so monitor Link readiness.

Legacy `wss://` Links remain supported via an external TLS service or CDN forwarded to the
Gateway's loopback tunnel handler. Address, HTTP Host, and TLS server name are independent
settings. Disable TLS verification only for a deliberately controlled test link.

### High-latency links

The tunnel's smux v2 receive windows are sized for at least 50 Mbps at 400 ms RTT, but the
operating-system TCP auto-tuning ceiling must also exceed the path bandwidth-delay product. On a
Linux Agent, and on a self-managed WSS edge or reverse proxy, use at least a 16 MiB ceiling for a
100 Mbps / 400 ms path:

```text
net.ipv4.tcp_rmem = 4096 1048576 16777216
net.ipv4.tcp_wmem = 4096 1048576 16777216
```

Apply these through the host's normal sysctl configuration management and verify the effective
values under `/proc/sys/net/ipv4/tcp_rmem` and `/proc/sys/net/ipv4/tcp_wmem`. For a managed CDN,
only the Agent-side setting is under xmesh operator control. Increasing smux buffers alone cannot
compensate for a smaller kernel TCP ceiling.

Tunnel data is copied with backpressure. A write that makes no progress for 30 seconds closes
that business stream; an idle stream with no pending write is not timed out by this protection.
Set `write_stall_timeout` in a node config to adjust it. The Agent accepts at most 1024 active
business streams across its WSS sessions by default (`max_active_streams`); excess streams are
rejected immediately. The Gateway bounds each UDP association's pending queue to 64 datagrams
and 256 KiB of payload (`gateway.max_udp_queue_bytes`), and reports UDP queue drops and WSS
write-stall metrics. New streams are temporarily steered away from a same-priority WSS session
whose writes recently stalled; established streams are never migrated.

## Runtime boundaries

- The client manages its own DNS, routing, IPv6 choice, and Gateway selection.
- The Agent uses the host's system resolver and routing table. It does not modify either.
- Agent target access is checked against its allow/deny CIDRs after DNS resolution. Deny rules win.
- A selected Agent with no healthy Link fails closed; traffic never falls back to another Agent or
  to the Gateway's own network.
- Existing TCP streams are not replayed after tunnel loss. New streams use the remaining healthy
  Link in the best priority group.

## Logs and diagnosis

Use `systemctl status xmesh` and `journalctl -u xmesh -f`. The Panel separately shows desired and
applied configuration versions, Xray readiness, node status, per-Link reports, traffic direction,
TCP connections, UDP associations, RTT, and the most recent concrete error.
