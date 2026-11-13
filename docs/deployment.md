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
Subscription authentication uses hashes. Their recoverable values, needed to redisplay existing
subscription links, are stored as AES-GCM ciphertext using a key derived from `session_secret`.
The first upgraded Controller start migrates legacy plaintext tokens without changing URLs.
Back up `session_secret` separately and retain it when restoring state; changing it without a
token migration makes encrypted subscriptions unreadable. Controllers predating this migration
cannot read the encrypted subscription fields; restore the matching old backup when downgrading.
Old backups still contain plaintext.
VMess/HY2 authentication secrets and TLS/REALITY private keys must remain recoverable to provision
nodes; they cannot be replaced with verification hashes. Protect state, configuration and backups
with filesystem permissions and encrypted storage.

Public node API requests are limited before authentication to 100 requests/second (burst 200)
per TCP peer, enrollment/pairing to 1/second (burst 10), and subscriptions to 5/second (burst 20).
There are at most 64 concurrent requests across these routes and 4096 tracked source buckets.
Excess requests return 429 with `Retry-After`. Forwarded-IP headers are ignored. Behind a reverse
proxy or shared NAT, these limits and the login failure bucket apply to the shared peer address.
Five failed logins block that address for 15 minutes; configure additional per-client limiting
and access controls at the proxy and allow only trusted administrators to reach the login page.
The Controller does not terminate TLS and business traffic never passes through it.

Rebuild XMesh, `xmesh-updater` and the bundled Gateway Xray using the Go version pinned in
`versions.env`. Go 1.27.4 includes the HTTP server fix and retains the eMudhra certificate
update. The Gateway Xray build uses the source and patched dependency versions locked in
`build/gateway-xray/go.mod` and verified by `go.sum`. Update host CA certificates as well.
Changing source pins or the host Go installation does not patch previously built executables.

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
Release redirects are restricted to the configured HTTPS origin. For `github.com`, the
explicit GitHub asset hosts `release-assets.githubusercontent.com`, `objects.githubusercontent.com`
and `github-releases.githubusercontent.com` are also allowed. Custom mirrors cannot redirect
to another origin. `SHA256SUMS` detects corruption but is not a publisher signature: the
configured release source and its administrators remain trusted to supply executable code.
Protect `release_base_url` and the release account accordingly.
The Controller host must be able to reach GitHub over HTTPS, but Gateway and Agent hosts can use
the Controller URL when their GitHub connection is unreliable. Existing Controller installations
must add `release_dir` to their preserved configuration and restart; the installers only set it
for a new configuration. Hosts running the install commands need `curl`, `sha256sum` and `tar`;
Docker mode additionally needs Docker Engine with the Compose plugin.

Pushing a `v*` tag runs the release workflow: it tests the code, builds the pinned Xray source
with the patched toolchain, verifies the embedded source version, builds Linux amd64/arm64 and Windows amd64,
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

Gateway nodes can optionally run an embedded Agent in the same process. Enable
it from the Gateway panel, then select that exit in connection management for
its own Gateway or any other Gateway. No Agent install command, extra enrollment,
service, or updater is needed. The Agent panel manages its CIDR policy. Disabling
the embedded Agent closes its tunnels and unpublishes affected grants while
preserving route configuration. Self connections use the local tunnel listener;
remote connections use the configured Gateway Link transport. Upgrade the owning
Gateway to upgrade both capabilities. For this feature, upgrade Controller first,
then Gateway, to v0.4.5 or newer.

The generic installer:

- accepts Debian, Ubuntu and CentOS 7/8 on amd64 or arm64;
- verifies the release archive against `SHA256SUMS`;
- refuses to overwrite an existing node identity or change its role;
- installs a hardened systemd service under the dedicated `xmesh` user;
- preserves identity and state during upgrades and ordinary uninstall;
- only removes identity/state when `--purge` is explicit.

CentOS 7 uses systemd 219-compatible filesystem directives, including a read-only root
with writable API filesystems and node state. CentOS 8, Debian and Ubuntu retain
`ProtectSystem=strict`. Install `ca-certificates`, `curl`, `tar`, `coreutils`, `iproute`
and `shadow-utils` using yum (CentOS 7) or dnf (CentOS 8) before running the installer.
For a Gateway with firewalld, allow its configured VMess TCP port and REALITY TCP port
(8443 by default) in the active network zone. The installer does not change firewall rules.

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

For a manual REALITY Agent Link, enter `reality://<gateway-public-host-or-IP>:<port>/tunnel` and a reachable
TLS 1.3 target such as `<target-host>:443` in the Controller. The Controller generates a Gateway
X25519 key pair and a per-Link VLESS UUID and short ID, then sends only the public key and Link
identity to the Agent. The Gateway's existing Xray process listens on
the port set in **Network → Gateway nodes → Edit and delete → REALITY tunnel port (TCP)**,
keeping the bind address from `gateway.reality_listen`. Leaving the field blank uses the
node's local setting (`0.0.0.0:8443` by default). Xray forwards authenticated connections to
the loopback tunnel handler, and forwards unauthenticated connections to the target. The Agent
embeds the pinned Xray-core implementation for REALITY; it does not spawn a helper process.
Upgrade the Gateway to a build that supports port control before changing an installed node.
Open the new TCP port in the host firewall and cloud security group before saving. The change
restarts Xray when REALITY is in use, so existing connections can briefly disconnect.
Automatic Links switch after the Gateway reports the new configuration applied.
If the new port is occupied, Gateway reports the bind error and keeps the old port
for a port-only change. For manual Links or NAT mappings, verify that the
public port reaches the effective listener. Port 443 requires bind privileges or external
port mapping with the default unprivileged service account.
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

Gateway addresses can be left blank at creation (including full-route setup).
Gateway queries `https://api.ip.sb/ip`, `https://api64.ipify.org` and
`https://ifconfig.co/ip` over direct HTTPS, without environment proxies or managed
exits. At least two independent services must agree on a public IP. IPv4 is tried
first and IPv6 is checked independently even when IPv4 succeeds. Discovery runs
in the background at startup and every 15 minutes; failures retry after one minute
and retain the last confirmed address. Query timeouts never block traffic serving.

The authenticated Gateway announces its confirmed address and sources to Controller.
The Panel shows automatic/manual mode, the confirmed IP, source names and the
source IP observed by Controller. The observed IP is supplementary, since policy
routing or NAT can use different source addresses for different destinations.
Existing manually configured addresses remain manual overrides. In the Gateway
edit form, enable **public IP automatic discovery** to ignore the manual address;
the manual value is retained and restored when automatic discovery is disabled.
New Gateways default to automatic discovery. Without a confirmed IP, an existing
Gateway retains its current working address until the first successful consensus.

Enable **also generate IPv6 user ingress nodes** to keep the primary IPv4/domain
entries and add IPv6 VMess and HY2 entries after IPv6 reaches consensus. Both
Controller and Gateway must support this feature. Automatic discovery requires
Gateway v0.4.6 or newer; dual-stack IPv6 requires v0.4.7 or newer. Controller
checks the reported stable binary version before enabling features on installed
nodes, accepting address reports, and publishing IPv6 subscriptions/bundles.
Unknown, development and prerelease versions are not assumed compatible.
Uninstalled Gateways may be preconfigured, but cannot publish IPv6 entries until
a supported version reports successful application. The Gateway listens on both
families, and HY2 uses one certificate covering both addresses. IPv6 strict HY2
client bundles are available separately. Subscription entries remain unpublished
until the Gateway acknowledges the new configuration. A temporary IPv4 probe
failure retains the last confirmed IPv4. Initial enablement may reload listeners
and rotate the HY2 certificate; established connections can be interrupted then.
Announcements never replace an override or restart a node when its effective
address is unchanged. Detection does not establish inbound port reachability;
NAT forwarding or domain-based access may still require an explicit override.

For observed-IP diagnostics only, Controller trusts forwarding headers from
loopback proxies by default. Set `trusted_proxy_cidrs` to the exact addresses/CIDRs
of Nginx/Caddy if they connect over a container/private network. An explicit empty
list disables this trust. `X-Forwarded-For` is walked from the nearest proxy;
`X-Real-IP` is a fallback. Untrusted peers' forwarding headers are ignored, and
login/rate-limit identity remains the actual TCP peer. Nginx should overwrite
`X-Real-IP` and append the actual client to `X-Forwarded-For`; Caddy's normal
reverse-proxy forwarding behavior is supported.

Before the first confirmed address, HY2 TLS provisioning, automatic Agent Link
dials and subscription publication wait. An address change updates managed REALITY
Link URLs and HY2 certificates, and requires the new Gateway revision to be
acknowledged before subscriptions are republished. Custom Link URLs remain custom.

Node, route, access-policy and switch changes in the Panel are applied live; the
Controller does not restart for these saves. Nodes normally fetch configuration
every 15 seconds. State writes are serialized and durable before acknowledgement,
while concurrent readers continue using the last committed snapshot. The Panel
keeps unrelated forms and status refresh usable while a save is pending. Startup
settings in the Controller configuration file still require a Controller restart.

Controller configuration saves are applied according to their effect on live traffic:

- Saving identical values or changing node/Link names and Gateway region does not
  advance runtime versions or rebuild connections.
- Link priority, weight and maximum streams update the Gateway's existing sessions.
  The new policy applies to new streams; lowering the limit preserves active streams.
- Increasing Link connections adds only the missing Agent slots. Decreasing the count
  closes only surplus slots; streams using those slots may be interrupted.
- Changing a Link's tunnel address, transport settings or credentials rebuilds that
  Agent Link, while other Links remain connected. REALITY cores are retired per Link.
- Agent access-policy changes keep tunnels and TCP streams whose connected target
  remains allowed. Disallowed TCP streams close promptly. Pending TCP dials and UDP
  associations close when the access policy changes. Equivalent CIDR/port ordering
  does not count as a policy change. Removing a grant closes its Agent streams.
- Gateway adds VMess/HY2 grants on existing routes through Xray's loopback-only
  control API, preserving existing client TCP/UDP connections and Agent tunnels.
  This requires the default `stats_listen` control address; do not expose it publicly.
  Existing users, outbounds, listener settings and rule order must remain unchanged.
  Revoking/rotating credentials, changing listeners or REALITY settings, replacing
  upstreams, and an API failure still use a reconciled Xray restart and can interrupt
  clients on that Gateway. A revision is acknowledged only after every API request
  succeeds or the replacement process starts; metadata-only updates must match the
  running payload. Binary upgrades also restart services and are not connection-free.

- Upgrade Controller before Gateway and Agent for per-Link grant authorization.
  Updated Agents require the grant list attached to each Link and reject requests
  from Controllers that do not provide it. Existing administrator sessions must
  log in again after this update.
- A node immediately closes traffic when Controller rejects its credentials or
  returns a disabled configuration. If Controller cannot be reached, its last
  successful configuration expires after five minutes and existing traffic closes.
  Successful configuration polling renews this lease.
- SOCKS UDP associations validate the announced source IP and port. On Linux and
  Windows, UDP packets must also belong to the authenticated TCP client's process.
  Gateway and its Xray child must share the network and process namespaces so
  socket ownership can be checked. Unspecified UDP ports fail closed when ownership
  cannot be determined.
- The client manages its own DNS, routing, IPv6 choice, and Gateway selection.
- The Agent uses the host's system resolver and routing table. It does not modify either.
- Agent target access is checked against its allow/deny CIDRs after DNS resolution. Deny rules win.
- Client proxy traffic always blocks destination TCP ports 25, 465, 587 and 2525.
  Gateway client ingress rules cover both Agent and external exits; the local SOCKS
  listener and Agent TCP dialer also reject these ports before opening a stream or
  resolving a target. Agent port allow lists cannot override this default block.
  IPv4, IPv6 and domain targets are covered; UDP and node management/tunnel
  connections are unaffected. This is a port restriction, not a complete email
  sending ban: HTTPS webmail, email APIs and custom ports remain available.
  Upgrade existing Gateways and Agents to apply it; no Controller state migration
  or configuration change is needed.
- A selected Agent with no healthy Link fails closed; traffic never falls back to another Agent or
  to the Gateway's own network.
- Existing TCP streams are not replayed after tunnel loss. New streams use the remaining healthy
  Link in the best priority group.

## Batch node upgrades

The Controller upgrades selected nodes one at a time. After the updater confirms a
successful process switch, the batch enters `recovering` for up to two minutes.
The next task stays pending until every previously healthy Link has fresh, ready,
online reports from both Gateway and Agent, received since upgrade completion.
Node reports and updater heartbeats recheck recovery; its deadline persists across
Controller restarts. A successful process switch remains `succeeded` while the
batch waits for its Links.

If recovery times out, the batch pauses and identifies the Link, reporting node,
and failed condition. After fixing the Link, use **重新检查并继续** in the panel.
This also supports Link recovery pauses saved by older Controllers. It rechecks
all completed tasks' baseline Links before releasing the remaining tasks, without
repeating successful upgrades. Failed, rolled-back, cancelled or expired tasks
cannot be bypassed with this action.

## Logs and diagnosis

Agent access-policy changes retire active UDP associations because each can target multiple
destinations and must not retain an old policy. Loopback UDP ownership is rechecked for every
datagram to prevent another process from reusing a previously authorized port. On Windows this
queries the UDP endpoint table, not the process list; caching a successful ownership result
would weaken that port-reuse protection.

Use `systemctl status xmesh` and `journalctl -u xmesh -f`. The Panel separately shows desired and
applied configuration versions, Xray readiness, node status, per-Link reports, traffic direction,
TCP connections, UDP associations, RTT, and the most recent concrete error.
