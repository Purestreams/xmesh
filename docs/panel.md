# Control center (v0.3.3)

The Controller embeds its HTML, CSS and JavaScript; no frontend service, CDN or
production Node.js runtime is required. Existing management POST endpoints and
CSRF checks remain in use. JavaScript enables five navigation sections, drawers
for details, collapsed creation and advanced forms, search, topology and matrix
views. Basic forms remain usable with JavaScript disabled.

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

Use **部署向导** to create a new route or reuse existing nodes. New-route setup
creates the Gateway, Agent, attachment and Link atomically. Entering a public
host fills the default REALITY URL unless the URL was manually edited. Advanced
ports, CIDRs and transport settings remain editable. For existing nodes, choose
an Agent and Gateways; missing associations and Links are created by the existing
transactional assignment handler. An unassigned matrix cell preselects this pair.

Installation commands are generated per node and shown in a dialog. Run them on
the corresponding host. The panel does not remotely execute host commands. The
progress view distinguishes enrollment, heartbeat, applied configuration, runtime
readiness and a usable route. Route readiness requires both nodes to be enabled,
online and ready, their desired configuration to be applied, Gateway Xray to be
ready, and both endpoints to report the same enabled Link ready.

In **用户与订阅**, choose a user and routes. Search, state filters and Gateway
grouping affect the candidates shown. **全选筛选结果** selects only visible,
enabled candidates, keeping any previously selected hidden candidates. The
counter includes all selected candidates. **清除选择** clears all selections in
that form; it never revokes existing assignments or grants. The preview shows
new, re-enabled and already-active items. Submission is additive, and publication
continues to wait for Gateway configuration acknowledgement. Use the separate
grant controls to disable or delete access.

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
