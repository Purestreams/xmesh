# Control center (v0.3.7)

The Controller embeds its HTML, CSS and JavaScript; no frontend service, CDN or
production Node.js runtime is required. Existing management POST endpoints and
CSRF checks remain in use. JavaScript enables five navigation sections, drawers
for details, collapsed creation and advanced forms, search, topology and matrix
views. Basic forms remain usable with JavaScript disabled.

## Anonymous network monitor

Under **系统维护 → 公开监控**, open `/admin/monitor` to enable the public,
read-only page at `/monitor`. It is disabled by default, including for existing
state files. Select each node and Link to publish and enter a separate public
alias. Aliases are never inferred from operational names. Publishing a Link
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
shown as a dash, not zero. External exits are labeled **未探测** and have no
fabricated RTT or reachability assertion. History is sampled about every five
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

## Verification

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
