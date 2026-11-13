const { chromium } = require("playwright");
const assert = require("node:assert/strict");

(async () => {
  const browser = await chromium.launch({ headless: true,
    channel: process.env.XMESH_BROWSER_CHANNEL || undefined,
    executablePath: process.env.XMESH_BROWSER_EXECUTABLE || undefined });
  try {
    const context = await browser.newContext({ locale: "zh-CN", viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    const base = process.env.XMESH_PANEL_TEST_URL || "http://127.0.0.1:18088";
    await page.context().addInitScript(() => sessionStorage.setItem('xmesh_expand_sections', 'true'));
    page.on('dialog', dialog => dialog.accept());
    await page.goto(base + "/login");
    await page.locator('[name="username"]').fill("admin");
    await page.locator('[name="password"]').fill("a-strong-test-password");
    await page.locator('form[action="/login"] button').click();
    await page.waitForFunction(() => document.querySelector("#refresh-note").textContent.startsWith("已更新"));
    const csrf = await page.locator('#grant-bulk [name="csrf"]').inputValue();
    const post = async (path, form) => {
      const response = await page.request.post(base + path, { form: { csrf, ...form } });
      assert.equal(response.status(), 200, await response.text());
    };
    const dashboard = () => page.request.get(base + '/admin/dashboard').then((r) => r.json());
    const refresh = async () => {
      await page.locator('#refresh-status').click();
      await page.waitForFunction(() => !document.querySelector('#refresh-status').disabled);
    };
    const suffix = Date.now().toString();
    for (const i of [1, 2]) await post('/admin/quick-setup', {
      gateway_name: `Grant Review Gateway ${i} ${suffix}`, agent_name: `Grant Review Agent ${i} ${suffix}`,
      region: 'cn', public_host: `review${i}.example`, vmess_port: '8080', vmess_path: '/proxy',
      allowed_cidrs: '0.0.0.0/0', link_url: `reality://review${i}.example:8443/tunnel`,
    });
    const setup = await dashboard();
    const routes = [1, 2].map((i) => setup.routes.find((r) => r.gateway_id === setup.nodes.find((n) => n.name === `Grant Review Gateway ${i} ${suffix}`).id));
    const [route1, route2] = routes.map((r) => r.id);
    const linkID = setup.links.find((l) => l.route_id === route1).id;
    const userName = `Grant review user ${suffix}`;
    await post('/admin/users', { name: userName });
    const user = (await dashboard()).users.find((u) => u.name === userName);
    await post('/admin/subscribe', { user_id: user.id, attachment_id: route1 });
    // Ownership must be safe even before the first dashboard response arrives.
    const cold = await page.context().newPage();
    let releaseInitial;
    const initial = new Promise((resolve) => { releaseInitial = resolve; });
    await cold.route('**/admin/dashboard', async (route) => { await initial; await route.continue(); });
    await cold.goto(base + '/#subscribe');
    await cold.locator('#subscribe .selection-summary').waitFor();
    await cold.locator('#subscribe select[name="user_id"]').selectOption('u1');
    await cold.locator(`#subscribe input[value="${route1}"]`).check();
    await cold.locator('#subscribe select[name="user_id"]').selectOption(user.id);
    assert.equal(await cold.locator('#subscribe input:checked').count(), 0, 'slow initial load kept another user\'s selections');
    releaseInitial();
    await cold.waitForFunction(() => document.querySelector('#refresh-note').textContent.startsWith('已更新'));
    await cold.close();
    await page.reload();
    await page.goto(base + '/#subscribe');
    await page.locator('#subscribe select[name="user_id"]').selectOption('u1');
    await page.locator(`#subscribe input[value="${route1}"]`).check();
    await page.locator('#subscribe select[name="user_id"]').selectOption(user.id);
    assert.equal(await page.locator('#subscribe input:checked').count(), 0, 'owner change kept another user\'s selections');
    await page.locator('#users [data-manage-user]').filter({ hasText: '管理线路' }).first().click();
    await page.locator('#grant-user').selectOption(user.id);
    await page.locator('#grants a[href="#subscribe"]').click();
    assert.equal(await page.locator('#subscribe select[name="user_id"]').inputValue(), user.id, 'add routes lost the current user');
    await page.goto(base + '/#grants');
    await page.locator('#grant-user').selectOption(user.id);
    const first = page.locator('#grant-table tr[data-grant-id]:visible');
    const firstID = await first.getAttribute('data-grant-id');
    await first.locator('input[name="grant_id"]').check();
    await post('/admin/subscribe', { user_id: user.id, attachment_id: route2 });
    await refresh();
    await page.waitForFunction((id) => document.querySelectorAll(`#grant-table tr[data-user-id="${id}"]`).length === 2, user.id);
    assert.equal(await page.locator('#grant-table input:checked').count(), 1, 'refresh lost selected access');
    const firstRow = page.locator(`#grant-table tr[data-grant-id="${firstID}"]`);
    // A protected subscription chooser must still receive a newly rotated URL.
    await page.goto(base + '/#subscribe');
    await page.locator('#subscribe select[name="user_id"]').selectOption(user.id);
    await page.locator(`#subscribe input[value="${route2}"]`).check();
    const subscription = page.locator(`#subscribe .subscription-row[data-user-id="${user.id}"]`);
    const oldURL = await subscription.locator('button[data-copy]').getAttribute('data-copy');
    await subscription.locator('summary').click();
    await page.goto(base + '/#users');
    const reset = page.locator(`form[action="/admin/users/${user.id}/reset-subscription"]`);
    await reset.locator('..').locator('summary').click();
    await reset.locator('button').click();
    await page.locator('#users .form-success').waitFor();
    assert.notEqual(await subscription.locator('button[data-copy]').getAttribute('data-copy'), oldURL, 'subscription reset kept an invalid copy URL');
    assert.equal(await subscription.locator('input[readonly]').inputValue(), await subscription.locator('button[data-copy]').getAttribute('data-copy'));
    assert.equal(await page.locator(`#subscribe input[value="${route2}"]`).isChecked(), true, 'subscription reset discarded pending route choices');
    assert.equal(await subscription.locator('details').getAttribute('open'), '');
    assert.equal((await page.request.get(oldURL)).status(), 404, 'reset did not invalidate the old subscription');
    // Another admin can rotate again while every grant is already unpublished.
    // Publication flags alone cannot invalidate this page's cached links.
    const rotatedURL = await subscription.locator('button[data-copy]').getAttribute('data-copy');
    const oldCredentials = await firstRow.locator('td:first-child details small').textContent();
    await post(`/admin/users/${user.id}/reset-subscription`, {});
    await refresh();
    await page.waitForFunction(({ id, url }) => document.querySelector(`#subscription-links [data-user-id="${id}"] button[data-copy]`).dataset.copy !== url,
      { id: user.id, url: rotatedURL });
    assert.notEqual(await firstRow.locator('td:first-child details small').textContent(), oldCredentials, 'external reset left old grant credentials visible');
    assert.equal(await subscription.locator('input[readonly]').inputValue(), await subscription.locator('button[data-copy]').getAttribute('data-copy'));
    assert.equal(await page.locator(`#subscribe input[value="${route2}"]`).isChecked(), true);
    assert.equal(await subscription.locator('details').getAttribute('open'), '');
    assert.equal((await page.request.get(rotatedURL)).status(), 404);
    // User state still refreshes while an unrelated create-user draft is kept.
    await page.goto(base + '/#users');
    await page.locator('#users details.create-details > summary').click();
    const userDraft = page.locator('#users form[action="/admin/users"] [name="name"]');
    await userDraft.fill('Unsubmitted user draft');
    const userSearch = page.locator('#users .table-search');
    await userSearch.fill(userName);
    const resetDetails = page.locator(`form[action="/admin/users/${user.id}/reset-subscription"]`).locator('..');
    if (await resetDetails.getAttribute('open') === null) await resetDetails.locator('summary').click();
    const remoteName = `Remote <user> ${suffix}`;
    await post('/admin/users', { name: remoteName });
    const remoteUser = (await dashboard()).users.find((u) => u.name === remoteName);
    await refresh();
    assert.equal(await page.locator(`#users tr[data-user-id="${remoteUser.id}"]`).count(), 1, 'live user table omitted a user created in another session');
    assert.equal(await page.locator(`#users tr[data-user-id="${remoteUser.id}"]`).isHidden(), true, 'new user escaped the current table filter');
    assert.equal(await page.locator(`#users tr[data-user-id="${remoteUser.id}"] user`).count(), 0);
    assert.equal(await userSearch.inputValue(), userName);
    assert.equal(await resetDetails.getAttribute('open'), '');
    assert.equal(await userDraft.inputValue(), 'Unsubmitted user draft');
    await userSearch.fill('');
    const userRow = page.locator(`#users tr[data-user-id="${user.id}"]`);
    assert.equal(await userRow.locator('.user-state-form [name="enabled"]').inputValue(), 'false');
    await post(`/admin/users/${user.id}/state`, { enabled: 'false' });
    await refresh();
    await page.waitForFunction((id) => document.querySelector(`#users tr[data-user-id="${id}"]`).dataset.enabled === 'false', user.id);
    assert.equal(await userRow.locator('.user-state-form [name="enabled"]').inputValue(), 'true');
    assert.equal(await userRow.locator('.user-state-form button').textContent(), '启用');
    assert.ok((await firstRow.locator('td').nth(3).textContent()).includes('用户已停用'), 'disabled user was misleadingly shown as awaiting grant publication');
    await post(`/admin/users/${user.id}/state`, { enabled: 'false' });
    assert.equal((await dashboard()).users.find((u) => u.id === user.id).enabled, false, 'repeated disable re-enabled a user');
    await userRow.locator('.user-state-form button').click();
    await page.waitForFunction((id) => document.querySelector(`#users tr[data-user-id="${id}"]`).dataset.enabled === 'true', user.id);
    assert.equal(await userDraft.inputValue(), 'Unsubmitted user draft');
    await page.goto(base + '/#grants');
    await firstRow.locator('details').last().locator('summary').click();
    const confirmation = firstRow.locator('[name="confirm_name"]');
    const firstConfirmation = await firstRow.getAttribute('data-confirmation');
    await confirmation.fill(firstConfirmation);
    const current = await dashboard();
    const link = current.links.find((l) => l.id === linkID);
    await post(`/admin/links/${linkID}/edit`, { name: 'Reviewed <line>', url: link.url });
    await refresh();
    await page.waitForFunction((id) => [...document.querySelectorAll('#grant-table tr')].find((r) => r.dataset.grantId === id)?.querySelector('.grant-route-name').textContent.includes('Reviewed <line>'), firstID);
    assert.equal(await confirmation.inputValue(), firstConfirmation, 'refresh discarded an unfinished deletion form');
    assert.equal(await page.locator('#grant-table .grant-route-name line').count(), 0, 'custom name was interpreted as markup');
    const secondID = (await dashboard()).grants.find((g) => g.user_id === user.id && g.route_id === route2).id;
    const secondRow = page.locator(`#grant-table tr[data-grant-id="${secondID}"]`);
    await secondRow.locator('details').last().locator('summary').click();
    await secondRow.locator('[name="confirm_name"]').fill(await secondRow.getAttribute('data-confirmation'));
    await secondRow.locator('form[action$="/delete"] button').click();
    await page.waitForFunction((id) => ![...document.querySelectorAll('#grant-table tr')].some((r) => r.dataset.grantId === id), secondID);
    assert.equal(await confirmation.inputValue(), firstConfirmation);
    assert.equal(await page.locator('#grant-table input:checked').count(), 1);
    await page.goto(base + '/#subscribe');
    await page.locator('#subscribe select[name="user_id"]').selectOption(user.id);
    await page.locator(`#subscribe input[value="${route1}"]`).check();
    await page.goto(base + '/#grants');
    // Batch and individual writes must not race against the same access record.
    let releaseBatch, batchCaptured;
    const batchRelease = new Promise((resolve) => { releaseBatch = resolve; });
    const batchWaiting = new Promise((resolve) => { batchCaptured = resolve; });
    const pendingBatch = async (route) => { batchCaptured(); await batchRelease; await route.continue(); };
    await page.route('**/admin/grants/state', pendingBatch);
    await page.locator('#grant-bulk select').selectOption('true');
    await page.locator('#grant-bulk button').click();
    await batchWaiting;
    assert.ok(await firstRow.locator('.grant-state-form button').isDisabled(), 'batch request left conflicting individual access controls active');
    assert.ok(await firstRow.locator('form[action$="/delete"] button').isDisabled(), 'batch request allowed deleting a selected grant');
    assert.ok(await page.locator('#subscribe button[type="submit"]').isDisabled(), 'batch access change allowed a conflicting subscription submission');
    assert.ok(await userRow.locator('.user-state-form button').isDisabled(), 'batch access change allowed a conflicting user state change');
    assert.ok(await resetDetails.locator('button').isDisabled(), 'batch access change allowed rotating credentials concurrently');
    let conflicting = 0;
    const countConflict = (request) => { if (request.method() === 'POST' &&
      [ `/admin/grants/${firstID}/state`, '/admin/subscribe', `/admin/users/${user.id}/state`, `/admin/users/${user.id}/reset-subscription` ].some((path) => request.url().endsWith(path))) conflicting++; };
    page.on('request', countConflict);
    await firstRow.locator('.grant-state-form').evaluate((form) => form.requestSubmit());
    await page.locator('#subscribe form.chooser').evaluate((form) => form.requestSubmit());
    await userRow.locator('.user-state-form').evaluate((form) => form.requestSubmit());
    await resetDetails.locator('form').evaluate((form) => form.requestSubmit());
    assert.equal(conflicting, 0, 'programmatic submission bypassed the pending grant mutation guard');
    releaseBatch();
    await page.waitForFunction(() => !document.querySelector('#grant-bulk').hasAttribute('aria-busy'));
    await page.unroute('**/admin/grants/state', pendingBatch);
    page.off('request', countConflict);
    assert.equal(await firstRow.locator('.grant-state-form button').isDisabled(), false);
    await firstRow.locator('input[name="grant_id"]').check();
    await page.locator('#grant-bulk select').selectOption('false');
    // The same guard applies in the other direction and releases after failure.
    let releaseSubscribe, subscribeCaptured;
    const subscribeRelease = new Promise((resolve) => { releaseSubscribe = resolve; });
    const subscribeWaiting = new Promise((resolve) => { subscribeCaptured = resolve; });
    const failedSubscribe = async (route) => {
      subscribeCaptured();
      await subscribeRelease;
      await route.fulfill({ status: 503, contentType: 'text/plain', body: 'Temporary subscription test failure' });
    };
    await page.route('**/admin/subscribe', failedSubscribe);
    await page.goto(base + '/#subscribe');
    await page.locator('#subscribe button[type="submit"]').click();
    await subscribeWaiting;
    assert.ok(await firstRow.locator('.grant-state-form button').isDisabled(), 'pending subscription allowed a conflicting access change');
    assert.ok(await page.locator('#grant-bulk button').isDisabled());
    assert.ok(await userRow.locator('.user-state-form button').isDisabled());
    conflicting = 0;
    page.on('request', countConflict);
    await firstRow.locator('.grant-state-form').evaluate((form) => form.requestSubmit());
    assert.equal(conflicting, 0);
    page.off('request', countConflict);
    releaseSubscribe();
    await page.locator('#subscribe .form-error').waitFor();
    await page.waitForFunction(() => !document.querySelector('#subscribe form.chooser').hasAttribute('aria-busy'));
    await page.unroute('**/admin/subscribe', failedSubscribe);
    await refresh();
    assert.equal(await page.locator(`#subscribe input[value="${route1}"]`).isChecked(), true, 'failed submission discarded pending choices');
    assert.equal(await page.locator('#subscribe button[type="submit"]').isDisabled(), false, 'failed request left subscription controls locked');
    assert.equal(await firstRow.locator('.grant-state-form button').isDisabled(), false);
    assert.equal(await userRow.locator('.user-state-form button').isDisabled(), false);
    assert.equal(await resetDetails.locator('button').isDisabled(), false);
    assert.equal((await dashboard()).grants.find((g) => g.id === firstID).enabled, true);
    assert.equal(await firstRow.locator('input[name="grant_id"]').isChecked(), true);
    await page.goto(base + '/#grants');
    // An older dashboard response arriving after a write must not undo its UI.
    let releaseOld, captured;
    const released = new Promise((resolve) => { releaseOld = resolve; });
    const waiting = new Promise((resolve) => { captured = resolve; });
    let hold = true, requests = 0;
    const staleDashboard = async (route) => {
      requests++;
      if (!hold) return route.continue();
      hold = false;
      const response = await route.fetch();
      const body = await response.text();
      captured();
      await released;
      await route.fulfill({ response, body });
    };
    await page.route('**/admin/dashboard', staleDashboard);
    await page.locator('#refresh-status').click();
    await waiting;
    await firstRow.locator('.grant-state-form button').click();
    await page.waitForFunction(() => document.querySelector('#notice').textContent.includes('用户线路授权已停用：变更 1'));
    assert.equal(await firstRow.getAttribute('data-enabled'), 'false');
    await page.evaluate((id) => {
      window.grantTransitions = [];
      window.grantObserver = new MutationObserver((records) => {
        records.forEach((record) => window.grantTransitions.push(record.target.dataset.enabled));
      });
      window.grantObserver.observe([...document.querySelectorAll('#grant-table tr')].find((r) => r.dataset.grantId === id), { attributes: true, attributeFilter: ['data-enabled'] });
    }, firstID);
    const fresh = page.waitForResponse((r) => r.url().endsWith('/admin/dashboard') && requests > 1);
    releaseOld();
    await fresh;
    await page.waitForFunction(() => document.querySelector('#stale-banner').hidden);
    assert.ok(!(await page.evaluate(() => window.grantTransitions)).includes('true'), 'stale refresh restored revoked access in the UI');
    await page.evaluate(() => window.grantObserver.disconnect());
    await page.unroute('**/admin/dashboard', staleDashboard);
    await firstRow.locator('.grant-state-form button').click();
    await page.waitForFunction(() => document.querySelector('#notice').textContent.includes('用户线路授权已启用：变更 1'));
    await page.locator('#grant-bulk button').click();
    await page.waitForFunction(() => document.querySelector('#notice').textContent.includes('用户线路授权已停用：变更 1'));
    assert.equal(await page.locator('#grant-table input:checked').count(), 0, 'successful bulk access change kept submitted selections');
    assert.equal(await confirmation.inputValue(), firstConfirmation);
    assert.equal((await dashboard()).grants.find((g) => g.id === firstID).enabled, false);
    await firstRow.locator('input[name="grant_id"]').check();
    await page.locator('#grant-bulk button').click();
    await page.waitForFunction(() => document.querySelector('#notice').textContent.includes('所选授权均已设为停用，无需重复操作'));
    assert.equal(await page.locator('#grant-table input:checked').count(), 0, 'a no-op retry kept submitted selections');
    await confirmation.fill(await firstRow.getAttribute('data-confirmation'));
    await firstRow.locator('form[action$="/delete"] button').click();
    await page.waitForFunction((id) => ![...document.querySelectorAll('#grant-table tr')].some((r) => r.dataset.grantId === id), firstID);
    assert.ok(await page.locator('#grant-empty').isVisible());
    assert.ok(await page.locator('#grant-bulk button').isDisabled());
    // Route candidates must reconcile external additions, names and deletions
    // without replacing pending choices or the current owner and filters.
    await page.goto(base + '/#subscribe');
    await page.locator('#subscribe select[name="user_id"]').selectOption(user.id);
    await page.locator(`#subscribe input[value="${route1}"]`).check();
    await page.goto(base + '/#assign');
    const assignment = page.locator('#assign-agent-panel form');
    await assignment.locator('select[name="agent_id"]').selectOption(routes[0].agent_id);
    await assignment.locator(`input[value="${routes[1].gateway_id}"]`).check();
    await assignment.locator('.selection-search').fill(suffix);
    await assignment.locator('.selection-filter').selectOption('new');
    await assignment.locator('[name="reality_target"]').fill('assignment-draft.example:443');
    await page.getByRole('tab', { name: '全部未开启连接', exact: true }).click();
    const pending = page.locator('#assign-pending-panel form');
    await pending.locator('.pending-agent').selectOption(routes[0].agent_id);
    await pending.locator('.pending-gateway').selectOption(routes[1].gateway_id);
    await pending.locator('.pending-search').fill(suffix);
    const pendingChoice = pending.locator(`[data-agent-id="${routes[0].agent_id}"][data-gateway-id="${routes[1].gateway_id}"] input`);
    await pendingChoice.check();
    await pending.locator('[name="reality_target"]').fill('pending-draft.example:443');
    await page.evaluate((gatewayID) => {
      window.assignmentChoice = document.querySelector(`#assign-agent-panel input[value="${gatewayID}"]`);
      window.pendingChoice = document.querySelector('#assign-pending-panel input:checked');
    }, routes[1].gateway_id);
    await page.goto(base + '/#subscribe');
    await post('/admin/quick-setup', {
      gateway_name: `Grant Review Gateway 3 ${suffix}`, agent_name: `Grant Review Agent 3 ${suffix}`,
      region: 'cn', public_host: 'review3.example', vmess_port: '8080', vmess_path: '/proxy',
      allowed_cidrs: '0.0.0.0/0', link_url: 'reality://review3.example:8443/tunnel',
    });
    const added = await dashboard();
    const route3 = added.routes.find((r) => r.gateway_id === added.nodes.find((n) => n.name === `Grant Review Gateway 3 ${suffix}`).id).id;
    await refresh();
    await page.waitForFunction((id) => document.querySelector(`#grant-user option[value="${id}"]`), user.id);
    assert.equal(await page.locator(`#subscribe input[value="${route3}"]`).count(), 1, 'live refresh omitted a newly created route candidate');
    const gateway3 = added.nodes.find((n) => n.name === `Grant Review Gateway 3 ${suffix}`);
    const agent3 = added.nodes.find((n) => n.name === `Grant Review Agent 3 ${suffix}`);
    assert.equal(await page.locator(`#assign-agent-panel select[name="agent_id"] option[value="${agent3.id}"]`).count(), 1, 'live refresh omitted a newly created Agent from assignment');
    assert.equal(await page.locator(`#assign-agent-panel input[value="${gateway3.id}"]`).count(), 1, 'live refresh omitted a newly created Gateway from assignment');
    assert.equal(await page.locator(`#assign-pending-panel .pending-agent option[value="${agent3.id}"]`).count(), 1, 'pending connection Agent filters stayed stale');
    assert.equal(await page.locator(`#assign-pending-panel .pending-gateway option[value="${gateway3.id}"]`).count(), 1, 'pending connection Gateway filters stayed stale');
    assert.equal(await assignment.locator('select[name="agent_id"]').inputValue(), routes[0].agent_id);
    assert.equal(await assignment.locator(`input[value="${routes[1].gateway_id}"]`).isChecked(), true);
    assert.equal(await assignment.locator('.selection-search').inputValue(), suffix);
    assert.equal(await assignment.locator('.selection-filter').inputValue(), 'new');
    assert.equal(await assignment.locator('[name="reality_target"]').inputValue(), 'assignment-draft.example:443');
    assert.equal(await pending.locator('.pending-agent').inputValue(), routes[0].agent_id);
    assert.equal(await pending.locator('.pending-gateway').inputValue(), routes[1].gateway_id);
    assert.equal(await pending.locator('.pending-search').inputValue(), suffix);
    assert.equal(await pendingChoice.isChecked(), true);
    assert.equal(await pending.locator('[name="reality_target"]').inputValue(), 'pending-draft.example:443');
    assert.equal(await page.evaluate(() => window.assignmentChoice.isConnected && window.assignmentChoice.checked &&
      window.pendingChoice.isConnected && window.pendingChoice.checked), true, 'refresh replaced selected connection inputs');
    assert.equal(await page.locator(`#subscribe input[value="${route1}"]`).isChecked(), true);
    const gateway = added.nodes.find((n) => n.id === routes[0].gateway_id);
    const newName = `Renamed <route> ${suffix}`;
    await page.locator('#subscribe .selection-search').fill(suffix);
    await page.locator('#subscribe .selection-group').selectOption(gateway.id);
    await post(`/admin/gateways/${gateway.id}/edit`, { name: newName, region: gateway.region,
      public_host: gateway.host, vmess_port: String(gateway.vmess_port), vmess_path: gateway.vmess_path,
      hy2_port: String(gateway.hy2_port) });
    await refresh();
    const routeLabel = page.locator(`#subscribe label:has(input[value="${route1}"])`);
    assert.ok((await routeLabel.textContent()).includes(newName), 'route chooser retained a stale Gateway name');
    assert.equal(await routeLabel.locator('route').count(), 0);
    assert.equal(await page.locator('#subscribe .selection-group option:checked').textContent(), newName);
    assert.equal(await page.locator('#subscribe .selection-group').inputValue(), gateway.id);
    assert.equal(await page.locator('#subscribe .selection-search').inputValue(), suffix);
    assert.equal(await page.locator(`#subscribe input[value="${route1}"]`).isChecked(), true);
    assert.ok((await assignment.locator(`label:has(input[value="${gateway.id}"])`).textContent()).includes(newName));
    assert.equal(await pending.locator(`.pending-gateway option[value="${gateway.id}"]`).textContent(), newName);
    const agentName = `Renamed <Agent> ${suffix}`;
    await post(`/admin/agents/${routes[0].agent_id}/edit`, { name: agentName, allowed_cidrs: '0.0.0.0/0' });
    await refresh();
    assert.equal(await assignment.locator('select[name="agent_id"] option:checked').textContent(), agentName);
    assert.equal(await pending.locator('.pending-agent option:checked').textContent(), agentName);
    assert.equal(await pendingChoice.isChecked(), true);
    await page.locator('#subscribe .selection-group').selectOption('');
    await page.locator(`#subscribe input[value="${route3}"]`).check();
    await post(`/admin/attachments/${route3}/delete`, { confirm_id: route3 });
    await refresh();
    assert.equal(await page.locator(`#subscribe input[value="${route3}"]`).count(), 0, 'deleted route remained available for selection');
    assert.equal(await page.locator(`#subscribe input[value="${route1}"]`).isChecked(), true);
    assert.equal(await page.locator('#subscribe select[name="user_id"]').inputValue(), user.id);
    // Deleting a selected owner must not carry its choices to another Agent.
    await page.goto(base + '/#assign');
    await page.getByRole('tab', { name: '按 Agent 连接', exact: true }).click();
    await assignment.locator('select[name="agent_id"]').selectOption(agent3.id);
    assert.equal(await assignment.locator('input:checked').count(), 0);
    await assignment.locator(`input[value="${routes[1].gateway_id}"]`).check();
    await page.getByRole('tab', { name: '全部未开启连接', exact: true }).click();
    await pending.locator('.pending-agent').selectOption(agent3.id);
    await pending.locator('.pending-gateway').selectOption(gateway3.id);
    await pending.locator(`[data-agent-id="${agent3.id}"][data-gateway-id="${gateway3.id}"] input`).check();
    await post(`/admin/agents/${agent3.id}/delete`, { confirm_name: agent3.name });
    await post(`/admin/gateways/${gateway3.id}/delete`, { confirm_name: gateway3.name });
    await refresh();
    assert.equal(await assignment.locator(`option[value="${agent3.id}"]`).count(), 0);
    assert.equal(await assignment.locator(`input[value="${gateway3.id}"]`).count(), 0);
    assert.equal(await assignment.locator('select[name="agent_id"]').inputValue(), '');
    assert.equal(await assignment.locator('input:checked').count(), 0, 'deleted Agent choices were transferred to another owner');
    assert.equal(await assignment.locator('button[type="submit"]').isDisabled(), true);
    assert.equal(await pending.locator('.pending-agent').inputValue(), '');
    assert.equal(await pending.locator('.pending-gateway').inputValue(), '');
    assert.equal(await pending.locator(`[data-agent-id="${agent3.id}"], [data-gateway-id="${gateway3.id}"]`).count(), 0);
    assert.equal(await pendingChoice.isChecked(), true, 'deleting unrelated nodes cleared an existing connection selection');
    assert.equal(await pending.locator('[name="reality_target"]').inputValue(), 'pending-draft.example:443');
    await page.goto(base + '/#subscribe');
    // Clipboard access can be denied or absent on an HTTP admin origin.
    for (const denied of [true, false]) {
      await page.evaluate((denied) => Object.defineProperty(navigator, 'clipboard', { configurable: true,
        value: denied ? { writeText: async () => { throw new Error('Clipboard access denied'); } } : undefined }), denied);
      if (await subscription.locator('details').getAttribute('open') !== null)
        await subscription.locator('summary').click();
      await subscription.locator('button[data-copy]').click();
      assert.equal(await subscription.locator('input[readonly]').isVisible(), true, 'copy fallback did not reveal the subscription URL');
      assert.equal(await subscription.locator('input[readonly]').evaluate((input) => document.activeElement === input && input.selectionStart === 0 && input.selectionEnd === input.value.length), true,
        'copy fallback did not select the full URL for manual copying');
    }
    await page.evaluate(() => {
      window.copiedURLs = [];
      Object.defineProperty(navigator, 'clipboard', { configurable: true,
        value: { writeText: async (text) => window.copiedURLs.push(text) } });
    });
    await subscription.locator('button[data-copy]').click();
    await subscription.locator('button[data-copy]').click();
    await page.waitForFunction((id) => document.querySelector(`#subscription-links [data-user-id="${id}"] button[data-copy]`).textContent === '复制订阅链接', user.id, { timeout: 5000 });
    assert.deepEqual(await page.evaluate(() => window.copiedURLs), Array(2).fill(await subscription.locator('input[readonly]').inputValue()));
    // A delayed operation must not replace a freshly rotated subscription URL.
    const race = await context.newPage();
    race.on('pageerror', (error) => errors.push(error.message));
    await race.goto(base + '/#subscribe');
    await race.waitForFunction(() => !document.querySelector('#refresh-status').disabled && document.querySelector('#refresh-note').textContent.startsWith('已更新'));
    const raceUser = await race.locator('#subscribe select[name="user_id"]').inputValue();
    const raceSubscription = race.locator(`#subscription-links [data-user-id="${raceUser}"]`);
    const beforeRace = await raceSubscription.locator('button[data-copy]').getAttribute('data-copy');
    await raceSubscription.locator('summary').click();
    let releaseAction, actionCaptured;
    const actionRelease = new Promise((resolve) => { releaseAction = resolve; });
    const actionWaiting = new Promise((resolve) => { actionCaptured = resolve; });
    await race.route('**/admin/users', async (route) => {
      const response = await route.fetch();
      const body = await response.text();
      actionCaptured();
      await actionRelease;
      await route.fulfill({ response, body });
    });
    await race.goto(base + '/#users');
    await race.locator('#users details.create-details > summary').click();
    await race.locator('#users form[action="/admin/users"] [name="name"]').fill(`Delayed user ${suffix}`);
    await race.locator('#users form[action="/admin/users"] button').click();
    await actionWaiting;
    await post(`/admin/users/${raceUser}/reset-subscription`, {});
    await race.locator('#refresh-status').click();
    await race.waitForFunction(() => !document.querySelector('#refresh-status').disabled);
    const currentRace = await raceSubscription.locator('button[data-copy]').getAttribute('data-copy');
    assert.notEqual(currentRace, beforeRace);
    await race.goto(base + '/#subscribe');
    await raceSubscription.locator('summary').click();
    releaseAction();
    await race.locator('#users .form-success').waitFor();
    await race.waitForFunction(() => !document.querySelector('#refresh-status').disabled);
    assert.equal(await raceSubscription.locator('button[data-copy]').getAttribute('data-copy'), currentRace, 'delayed action restored an invalid subscription URL');
    await race.close();
    assert.deepEqual(errors, []);
    console.log('Grant browser checks passed: owner changes, add-route context, external users and grants, live names and candidates, subscription rotation and copying, user state retries and disabled reasons, draft preservation, serialized access writes and failure recovery, deletion with selection, stale refresh and actions, bulk completion and empty filters.');
  } finally {
    await browser.close();
  }
})().catch((error) => { console.error(error); process.exitCode = 1; });
