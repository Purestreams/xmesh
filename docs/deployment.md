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

## Gateway and Agent

Create the Gateway or Agent in the Panel, create the required Gateway × Agent combination and
Link, then use **Generate one-time install command**. The command expires after 30 minutes and can
only be used once.

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

Agent Link URLs should normally be `wss://` addresses exposed by an external TLS service or CDN
and forwarded to the Gateway's loopback tunnel handler. Address, HTTP Host, and TLS server name
are independent settings. Disable TLS verification only for a deliberately controlled test link.

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
