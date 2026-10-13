# Control center (v0.4.1)

The Controller embeds its HTML, CSS and JavaScript; no frontend service, CDN or
production Node.js runtime is required. Existing management POST endpoints and
CSRF checks remain in use. JavaScript enables five navigation sections, drawers
for details, collapsed creation and advanced forms, search, topology and matrix
views. Basic forms remain usable with JavaScript disabled.

## Interface language

The sign-in page, control center, public monitor and monitor settings support
Chinese and English. On the first visit, the browser's primary language selects
Chinese for `zh-*` locales and English otherwise. Use the language selector in
the header to switch. An explicit choice is stored in `localStorage` under
`xmesh.language` and takes precedence on later visits to the same origin. When
local storage is unavailable, a language preference cookie provides a fallback.
Switching reloads the current page; save pending edits first. Node names, public
aliases, addresses, credentials and installation commands keep their original
values. Dates use the selected language and the browser's local time zone.

Run `node --test tests/i18n.test.cjs` for locale and translation checks. For browser
checks, start `TestPanelBrowserFixture` on port 18088 and `TestMonitorBrowserFixture`
on port 18090, then run `node tests/i18n.browser.cjs` with Playwright on `NODE_PATH`.

## Anonymous network monitor

Under **系统维护 → 公开监控**, open `/admin/monitor` to enable the public,
read-only page at `/monitor`. It is disabled by default, including for existing
state files. Select each node and Link to publish and enter a separate public
alias. Link names are optional: a selected Link with a blank name uses its
public endpoint aliases (for example, `香港入口 → 东京出口`). Multiple Links
between the same endpoints receive a sequence number. A blank name does not
clear the Link's publication checkbox. Aliases are never inferred from
operational names. Publishing a Link
requires both endpoint nodes to be selected; new objects remain hidden.
Public aliases accept up to 48 letters, numbers, spaces, hyphens, underscores,
or parentheses, and reject address/URL punctuation. Choose aliases without
sensitive identifiers: the aliases and selected connection relationships are
visible to anyone who opens the page.

The dedicated `GET /api/public/monitor` response includes only public aliases,
roles, response-local node references, coarse states, RTT measurements and
sanitized RTT history. It does not use the admin dashboard DTO or serialize
model configuration/status objects. Addresses, internal identities, raw errors,
credentials, users, usage and upgrade details are excluded. The separate static
monitor assets contain no operational data. Management routes still require an
admin session, and saving publication settings requires CSRF validation.

The matrix expands into individual Link measurements and a 24-hour chart.
RTT is the smoothed Gateway-to-Agent tunnel round trip, probed every 10 seconds,
not latency from the visitor's device or a complete proxy end-to-end test.
Both nodes must be online, ready and have applied their configuration, Gateway
Xray must be ready, and both endpoints must report the Link ready. Expired node,
Link or successful-probe timestamps suppress current RTT. Missing/invalid RTT is
shown as a dash, not zero. External routes remain **未探测** until their Gateway
reports a completed HTTPS probe. Their latency is the request time to IP.SB
through the selected upstream. History is sampled about every five
minutes; unavailable samples and long gaps are not joined in the chart.

The browser refreshes every 15 seconds. Failures mark displayed live data stale;
a closed monitor clears it on the next refresh. Server responses use `no-store`.
The server caches only the public projection for up to 10 seconds and rebuilds
on any persisted state revision, so alias changes, hiding, deletion and closing
take effect immediately on the next request. A bounded global token bucket
allows 100 API reads per second with a burst of 200. Settings persist in the
Controller state file and do not change node configuration versions or grants.

Validation: `go test ./internal/controller ./internal/model ./internal/store`.
For browser checks, run `TestMonitorBrowserFixture` with
`XMESH_MONITOR_TEST_ADDR=127.0.0.1:18089`, then run
`node tests/monitor.browser.cjs` with Playwright available on `NODE_PATH`.
The fixture uses temporary state only; screenshots go to
`tmp/monitor-screenshots/`. It checks anonymous responses for seeded private
values, matrix/history interactions, mobile layout, refresh failures, admin
settings and access revocation.

## Deploy and grant access

Choose the Gateway location when creating or editing a Gateway: China mainland
defaults to `api.bilibili.com:443`, and overseas defaults to `www.swift.com:443`.
In new-route setup the location selection fills the target and preserves a
manually entered custom value. Batch assignment resolves blank targets per
Gateway, so mixed-region selections get different defaults. Existing REALITY
targets remain unchanged, even when a Gateway's location is edited. A manually
entered batch target overrides defaults only for Gateways without an existing
target. Older Gateways retain an unset location until it is explicitly selected;
their location is never guessed from names or IP addresses.

Use **部署向导** to create a complete route, add a Gateway, add an Agent, or reuse
existing nodes. The separate Gateway and Agent entries create only that node;
the wizard then guides installation, binding, readiness checks, and user access.
An unbound node is shown as created but without a usable route. Complete-route
setup creates the Gateway, Agent, attachment and Link atomically. Entering a
public host fills the default REALITY URL unless the URL was manually edited.
Advanced ports, CIDRs and transport settings remain editable. For existing nodes,
choose an Agent and Gateways; missing associations and Links are created by the
existing transactional assignment handler. An unassigned matrix cell preselects
this pair. Installation commands are generated from each created node.

In **连接已有节点**, the **全部未开启连接** tab lists missing, disabled, or
Link-less pairs across all Agents and Gateways. Active pairs are excluded even
when offline. Search and Agent/Gateway filters narrow the list; **全选筛选结果**
selects visible available pairs and **清除选择** clears all selections, including
hidden ones. Disabled nodes and Gateways without public addresses cannot be
selected. **开启所选连接** creates or re-enables the selected pairs in one atomic
transaction, preserving existing targets and Link settings. Missing targets
require manual entry. Refresh preserves pending selections; submission leaves
the tab open and removes enabled pairs from the list. User grants are separate.

For an external exit, use **外部出口** on the network page.
Paste a single `vmess://` or `vless://` link, or a subscription URL. A subscription with several
supported nodes requires an explicit selection. Bind the exit to a Gateway, then
grant the resulting route in the same **用户与订阅** chooser used for Agent routes.
The Gateway applies a VMess or VLESS outbound directly;
there is no Agent installation or Link for this route. Subscription refresh runs
every 30 minutes and can be requested manually. A failed fetch retains the last
valid endpoint; a successful refresh that loses the selected node disables it.
The dashboard reports applied configuration separately from upstream reachability.

Installation commands are generated per node and shown in a dialog. Run them on
the corresponding host. The panel does not remotely execute host commands. The
progress view distinguishes enrollment, heartbeat, applied configuration, runtime
readiness and a usable route. Route readiness requires both nodes to be enabled,
online and ready, their desired configuration to be applied, Gateway Xray to be
ready, and both endpoints to report the same enabled Link ready.

In **用户与订阅**, choose a user and routes, including external routes.
Search, state filters and Gateway grouping affect the candidates shown.
**全选筛选结果** selects only visible,
enabled candidates, keeping any previously selected hidden candidates. The
counter includes all selected candidates. **清除选择** clears all selections in
that form; it never revokes existing assignments or grants. The preview shows
new, re-enabled and already-active items. Submission is additive, and publication
continues to wait for Gateway configuration acknowledgement. Use the separate
grant controls to disable or delete access.

Resetting a user's subscription also rotates that user's VMess and SOCKS access
credentials for every assigned route, including disabled grants. The old URL is
invalid immediately; gateways revoke the old proxy credentials when they apply
the new configuration. Publication waits for Gateway acknowledgement, then clients
must import the new subscription. Other users' credentials are unchanged.

## Refresh and history

The authenticated, non-cacheable `/admin/dashboard` endpoint returns an explicit
allowlist of display fields, never complete configuration objects, subscription
tokens, node credentials, private keys or enrollment tokens. The visible page
polls every 15 seconds without reloading or replacing form inputs. Failed refreshes
keep the last display and show an error. Successful mutations refresh management
sections except other dirty forms or open edit panels. This preserves unfinished
work; a full reload can be used to discard edits and reconcile structural changes
made by another administrator.

Only accepted Gateway Link reports feed history, preventing double counting from
Agent reports. Samples are taken at most every five minutes per Link, with up to
289 boundary-inclusive samples in the last 24 hours. Retention is enforced on
sampling and on the dashboard response. Deleted Link histories are removed on
the next sample. There is no invented history before installation of this release.
Rates require two ready samples of the same generation, increasing counters and
no more than ten minutes between samples. Resets and long gaps produce missing
points, not negative rates or continuous fabricated traffic. RTT is also hidden
when a sample is not ready or has no measured value.

The state file also stores the latest 100 authenticated, CSRF-validated management
request results. These records contain timestamp, administrator, registered action
and HTTP outcome; request bodies and returned installation secrets are excluded.
An accepted upgrade request is not an upgrade completion: its current queued,
running or terminal status is displayed separately. Logging failures are emitted
to the Controller log. Existing Controller backups include these additive fields.

The **Node upgrades** section shows each Gateway / Agent host helper, its version,
installation mode and latest task stage. Generate a pairing token for an older
host and run the verified migration command there once. Select one or more
nodes and a fixed release version to create a serial batch. The Controller
prepares and checks each release archive before a helper can claim its task.
Pending tasks may be cancelled; a claimed task completes or rolls back.

## Per-user audit

Open **用户与订阅 → 用户审计 → 查看与设置审计** to enable auditing for
one user. It is off by default and applies to that user's Agent grants across
all Gateways, Agents and Links. Settings apply through the existing node polling
without disconnecting traffic. Only new events are recorded; disabling retains
history. Clearing invalidates queued events before removing only that user's logs.

Records include UTC time, trusted User/Grant identity, original target host and
port, actual connected IP, exit/Link, result or failure reason, connection duration
and payload upload/download bytes. Agent DNS resolution records include returned
IPs and resolution duration. TCP records distinguish attempts, successful connects,
failures/denials and closed connections. Duration and final byte totals are written
when a connection closes. Existing connections are not backfilled. TCP targets
blocked at Xray ingress are not visible to the Agent audit collector.

Logs are stored separately from Controller state, under `audit/<UserID>/` beside
the state file; `audit_dir` can override this root. The default per-user limit is
20 MB (20,000,000 bytes), shared by current JSONL logs and all gzip archives across
that user's exits. The admin can set 1–1024 MB. Segments rotate at approximately
1 MiB, compress automatically, and oldest archives are removed to enforce the
quota; at most 64 archives are retained. View the latest 200 matching records,
filter by target/port/result, download all retained records as gzip, or clear them.
All endpoints require administrator authentication; changes require CSRF tokens.

The last 60 seconds of received events are combined by user. Alerts indicate
120 connection attempts, 30 failures/denials, 30 distinct targets or 20 distinct
ports. Counts are bounded at 128 distinct targets/ports and reset with the
Controller. Closed events and DNS records do not inflate TCP attempt counts.
These are indicators, not proof of an attack, and never automatically block users.

Agent collection uses a bounded 2,048-event memory queue, with batches of up to
256 events each second. Overflow and failed reports are counted as lost events;
there is no disk spool or event retry. Loss counts are reported when connectivity
returns and retained in Controller memory until restart/clear. Logging failures
do not block proxy traffic. Audit history and lost-event counts can be incomplete
after process crashes or outages.

External exits' final DNS and TCP connections, client-local DNS, encrypted DNS
query contents and UDP traffic are not covered. DNS lookups do not prove access.
The page warns when the user has external exit grants. Gateway Xray access logging
is explicitly disabled; regular operational warning/error logs remain separate.
Upgrade Controller and Agent to use the audit feature; upgrade Gateway to apply
the access-log setting. No existing users are enabled automatically.

Browser checks: start `TestUserAuditBrowserFixture` with
`XMESH_AUDIT_BROWSER_ADDR=127.0.0.1:18093`, then run
`node tests/audit.browser.cjs` with Playwright on `NODE_PATH`.

## Verification

External routes have a **测试连接** button on the external-exit page and route
cards. The corresponding Gateway uses an authenticated loopback SOCKS listener
in its running Xray process to select that exact upstream, independently of user
grants. It requests IP.SB's HTTPS IP API and Microsoft's HTTPS homepage, verifies
TLS, and shows each site's status, HTTP request time, error, and the exit IP.
The route is verified only when both checks succeed.

Tests run once after the Gateway applies a configuration and then every minute
by default. Set the Gateway's local `upstream_probe_interval` (for example
`"1m"`) to change this interval. An external route is omitted from user
subscriptions after five continuous minutes of failed tests and returns after
the next successful test. `upstream_probe_listen` defaults to
`127.0.0.1:18083` and must remain on loopback. Manual requests do not restart
Xray. Results from older configurations or superseded requests are ignored.
Probe traffic is excluded from user usage counters. Upgrade both Controller and
Gateway to enable the feature; older Gateways show an upgrade hint.

For external routes, the public monitor's latency and history measure the
Gateway-to-IP.SB HTTPS request through the selected upstream, rather than ICMP
RTT. Microsoft's separate result and timing remain visible in the admin panel.

```sh
go test ./...
go vet ./...
node --test tests/panel.test.cjs
```

Browser regression tests use an isolated in-memory fixture persisted in a test
temporary directory, with synthetic names, counters and credentials. Start it
in one terminal (never set this environment variable on a production service):

```sh
XMESH_PANEL_TEST_ADDR=127.0.0.1:18088 go test ./internal/controller -run '^TestPanelBrowserFixture$' -v -timeout 15m
```

With Playwright installed in a separate tools directory and its `node_modules`
on `NODE_PATH`, run `node tests/panel.browser.cjs`. Set `XMESH_BROWSER_CHANNEL=msedge`
to use a locally installed Edge, or install Playwright Chromium. Screenshots go
to ignored `tmp/panel-screenshots/`. The test covers navigation, topology, matrix
binding, selection scope, clearing, preserved inputs, idempotent grants, failed
validation, operation history and mobile overflow.
