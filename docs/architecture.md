# Architecture

See the [Chinese](../README.md#架构一览) and [English](../README.en.md#architecture-at-a-glance)
README diagrams for the overall topology.

## Data and control planes

```text
VMess/WS client -- TCP :8080 --+
                              +--> Gateway Xray
HY2 client -- QUIC/TLS :8080 --+
                                     |
                                     | Agent route: authenticated loopback SOCKS5
                                     v
                              Gateway scheduler <--> REALITY + WebSocket + smux v2 <--> Agent --> target
                                                     (Agent initiates tunnel)          system DNS/routes

Gateway Xray -- VMess/TCP or WS, or VLESS/REALITY + Vision --> external exit --> target

Gateway / Agent -- HTTPS config polling and status reports --> Controller
Node host xmesh-updater -- HTTPS task claims and results --> Controller
Public visitor -- /monitor and /api/public/monitor --> Controller public projection
                                                     (no user traffic)
```

Ports above are defaults. The Agent tunnel uses Gateway TCP 8443 by default.
The Gateway supervises its bundled Xray process; the Agent embeds Xray-core to
initiate VLESS/REALITY connections carrying WebSocket and smux v2. Existing WSS
Links remain supported with external TLS termination.

New Gateways enable VMess/WS and HY2 independently. HY2 uses QUIC with TLS 1.3,
per-Grant authentication and a persistent self-signed certificate with a SHA-256
pin. Both ingresses use the same Grant routing to an Agent or external exit.
Disabling either protocol removes its listener and subscription entries; disabling
both leaves Agent tunnels available. See [HY2](hy2.md) for client trust and deployment.

## Authorization and routes

The stable authorization model is `Node = Gateway × (Agent or external exit)` and `Grant = User × Node`. Each Grant
has separate VMess, HY2 and loopback SOCKS credentials. A Link is an internal path for one
Gateway–Agent attachment; adding direct/CDN Links never creates extra subscription nodes.
An external attachment selects one manually imported node or one node from a subscription.
The Controller resolves subscriptions and sends only the selected endpoint to the Gateway. Xray
routes that Grant directly to a VMess or VLESS outbound; an external attachment has no Agent or tunnel Link.
Resetting a subscription invalidates its URL immediately and rotates that user's
proxy credentials, including disabled Grants. Gateways revoke the old proxy
credentials when they apply the new configuration.

## Scheduling and transport

For a new TCP connection or UDP association, the Gateway chooses the lowest-numbered healthy
priority group and minimizes `(active streams + 1) / weight` within that group. The resulting
business session remains pinned to the chosen smux session. Existing sessions are not migrated
when policy or health changes.

Control messages are size-bounded, length-prefixed JSON. TCP payload is streamed without JSON or
Base64 wrapping. UDP uses a length-bounded frame that preserves one datagram and its source/target
address. The tunnel accepts complete UDP payloads up to the IPv4 UDP limit; larger frames are
rejected instead of truncated. Every SOCKS UDP association has a distinct loopback socket tied to
its authenticated TCP control connection and one pinned smux stream. The Agent keeps one UDP
socket for that association so multiple targets and their reply source addresses remain distinct.

Both tunnel peers use smux v2 with 32 KiB frames, an 8 MiB per-stream receive window, and a
32 MiB shared receive budget per smux session. The half-window exceeds the approximately 2.5 MB
bandwidth-delay product of a 50 Mbps flow at 400 ms RTT. Keepalive runs every 10 seconds with a
60-second timeout; these values apply to new sessions and do not change TCP or TLS verification.

## Controller, monitoring and upgrades

The Controller embeds the management panel and public monitor assets and persists
state in a JSON file with atomic replacement; it requires no separate frontend or
database. Node credentials, enrollment
tokens, subscription tokens, VMess UUIDs, and tunnel credentials have separate purposes. A Grant
is not published in a subscription until its Gateway reports that the matching desired revision
and Xray configuration are both applied and the corresponding ingress is ready.

The optional anonymous monitor is disabled by default. Administrators explicitly
publish nodes and Links with separate public aliases. `/api/public/monitor` uses
a dedicated allowlisted projection, excluding real addresses, internal identities,
credentials, users, usage, raw errors and upgrade details. It exposes coarse states
and measured Gateway–Agent tunnel RTT, with 24-hour history sampled about every
five minutes. External exits are not probed. See the [panel guide](panel.md#anonymous-network-monitor).

Configuration reconciliation preserves unaffected tunnels. Metadata changes do
not rebuild connections; scheduling changes govern new streams; Agent transport
changes rebuild the affected Link. Gateway Xray restarts only when its generated
configuration changes. Nodes renew a five-minute configuration lease through
successful polling and close traffic on expiry or authorization rejection. See
[runtime boundaries](deployment.md#runtime-boundaries).

The node host's independent `xmesh-updater` claims Controller tasks, verifies
release assets and executes upgrades with rollback. Serial batches wait up to two
minutes after each successful upgrade for fresh online, ready reports from both
endpoints of previously healthy Links. Recovery timeouts pause the batch; after
fixing the Link, administrators can recheck and continue remaining tasks without
repeating successful upgrades. Docker business containers do not mount the Docker
socket. See [batch node upgrades](deployment.md#batch-node-upgrades).
