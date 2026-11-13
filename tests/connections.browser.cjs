// Run against a fresh TestPanelBrowserFixture; state is temporary and local.
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");

(async () => {
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.XMESH_BROWSER_CHANNEL || undefined,
    executablePath: process.env.XMESH_BROWSER_EXECUTABLE || undefined,
  });
  try {
    const page = await browser.newPage({ locale: "zh-CN", viewport: { width: 1440, height: 1100 } });
    const errors = [];
    const accessRecords = grants => grants.map(({access, ...record}) => record);
    page.on("pageerror", (error) => errors.push(error.message));
    const base = process.env.XMESH_PANEL_TEST_URL || "http://127.0.0.1:18088";
    await page.context().addInitScript(() => sessionStorage.setItem('xmesh_expand_sections', 'true'));
    page.on('dialog', dialog => dialog.accept());
    await page.goto(base + "/login");
    await page.getByLabel("用户名").fill("admin");
    await page.getByLabel("密码").fill("a-strong-test-password");
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.waitForFunction(() => document.querySelector("#refresh-note").textContent.startsWith("已更新"));
    await page.goto(base + "/#assign");
    const pendingTab = page.getByRole("tab", { name: "全部未开启连接", exact: true });
    await pendingTab.click();
    const form = page.locator("#assign-pending-panel form");
    const selectAll = form.getByRole("button", { name: "全选筛选结果", exact: true });
    const clear = form.getByRole("button", { name: "清除选择", exact: true });
    const submit = form.getByRole("button", { name: "开启所选连接", exact: true });
    const checked = form.locator('input[name="connection"]:checked');
    await page.waitForFunction(() => document.querySelectorAll(".pending-choice").length === 4);
    assert.equal(await form.locator('.pending-choice input:not(:disabled)').count(), 2);
    assert.equal(await form.locator('[data-agent-id="a1"][data-gateway-id="g2"]').count(), 0, "active offline route must be excluded");
    assert.ok(await submit.isDisabled());
    // A missing region must explain why blank is invalid, including selections
    // hidden by filters. Region defaults and existing targets remain optional.
    const target = form.locator('input[name="reality_target"]');
    const hint = form.locator('.pending-target-hint');
    let targetMode = "missing";
    const dashboardURL = "**/admin/dashboard";
    await page.route(dashboardURL, async (route) => {
      const response = await route.fetch();
      const body = await response.json();
      const gateway = body.nodes.find((node) => node.id === "g1");
      gateway.target = targetMode === "existing" ? "custom.example:443" : "";
      gateway.region = targetMode === "default" ? "cn" : "";
      await route.fulfill({ response, json: body });
    });
    await page.locator("#refresh-status").click();
    // Both connection tabs must explain a missing target before submitting.
    await page.getByRole('tab', {name:'按 Agent 连接',exact:true}).click();
    const agentForm=page.locator('#assign-agent-panel form'),agentTarget=agentForm.locator('[name=reality_target]');
    await agentForm.locator('[name=agent_id]').selectOption('a2');
    await agentForm.locator('[name=gateway_id][value=g1]').check();
    assert.equal(await agentTarget.evaluate(input=>input.required),true,'per-Agent assignment falsely presents a missing target as optional');
    assert.equal(await agentTarget.evaluate(input=>input.checkValidity()),false);
    assert.match(await agentTarget.evaluate(input=>input.validationMessage),/Hong Kong Edge/);
    const agentHint=agentForm.locator('.connection-target-hint');
    assert.match(await agentHint.textContent(),/Hong Kong Edge/);
    await agentForm.locator('.selection-search').fill('  Singapore  ');
    assert.equal(await agentForm.locator('fieldset > label:not([hidden])').count(),1,'connection search did not trim surrounding spaces');
    assert.ok(await agentHint.isVisible(),'a hidden selection lost its target requirement');
    await agentTarget.fill('manual.example:443');assert.equal(await agentTarget.evaluate(input=>input.checkValidity()),true);
    await agentTarget.fill('   ');assert.equal(await agentTarget.evaluate(input=>input.checkValidity()),false);
    await agentForm.getByRole('button',{name:'清除选择',exact:true}).click();
    assert.equal(await agentTarget.evaluate(input=>input.required),false);assert.ok(await agentHint.isHidden());
    await agentTarget.fill('');await agentForm.locator('.selection-search').fill('');
    await agentForm.locator('[name=agent_id]').selectOption('a1');
    await agentForm.locator('[name=gateway_id][value=g1]').check();
    assert.equal(await agentTarget.evaluate(input=>input.required),false,'existing Links must not require a new target');
    assert.match(await agentForm.locator('[name=gateway_id][value=g1]').evaluate(input=>input.parentElement.textContent),/保留已有 Link 配置/);
    await agentForm.getByRole('button',{name:'清除选择',exact:true}).click();
    await pendingTab.click();
    await form.locator('[data-agent-id="a2"][data-gateway-id="g1"] input').check();
    await page.waitForFunction(() => document.querySelector('#assign-pending-panel input[name="reality_target"]').required);
    assert.match(await target.getAttribute("placeholder"), /必填/);
    assert.match(await hint.textContent(), /Hong Kong Edge/);
    assert.equal(await target.evaluate((input) => input.checkValidity()), false);
    assert.match(await target.evaluate((input) => input.validationMessage), /Hong Kong Edge/);
    await form.locator(".pending-search").fill("Singapore");
    assert.ok(await hint.isVisible(), "hidden selections still require a target");
    await target.fill("manual.example:443");
    assert.equal(await target.evaluate((input) => input.checkValidity()), true);
    await target.fill("   ");
    assert.equal(await target.evaluate((input) => input.checkValidity()), false);
    await clear.click();
    assert.equal(await target.evaluate((input) => input.required), false);
    assert.ok(await hint.isHidden());
    await target.fill("");
    await form.locator(".pending-search").fill("");
    for (targetMode of ["default", "existing"]) {
      await page.locator("#refresh-status").click();
      await page.waitForFunction((mode) => {
        const text = document.querySelector('.pending-choice[data-agent-id="a2"][data-gateway-id="g1"] .choice-state').textContent;
        return text.includes(mode === "default" ? "api.bilibili.com:443" : "custom.example:443");
      }, targetMode);
      await form.locator('[data-agent-id="a2"][data-gateway-id="g1"] input').check();
      assert.equal(await target.evaluate((input) => input.required), false);
      assert.equal(await target.evaluate((input) => input.checkValidity()), true);
      assert.ok(await hint.isHidden());
      assert.match(await target.getAttribute("placeholder"), /留空/);
      await clear.click();
      await page.getByRole('tab',{name:'按 Agent 连接',exact:true}).click();
      await agentForm.locator('[name=agent_id]').selectOption('a2');
      await agentForm.locator('[name=gateway_id][value=g1]').check();
      assert.equal(await agentTarget.evaluate(input=>input.required),false);
      assert.equal(await agentTarget.evaluate(input=>input.checkValidity()),true);
      assert.ok(await agentHint.isHidden());
      await agentForm.getByRole('button',{name:'清除选择',exact:true}).click();await pendingTab.click();
    }
    await page.unroute(dashboardURL);
    await page.locator("#refresh-status").click();
    await pendingTab.press("ArrowLeft");
    assert.ok(await page.locator("#assign-agent-panel").isVisible());
    await page.getByRole("tab", { name: "按 Agent 连接", exact: true }).press("ArrowRight");
    assert.ok(await page.locator("#assign-pending-panel").isVisible());
    await form.locator(".pending-agent").selectOption("a2");
    await form.locator(".pending-gateway").selectOption("g1");
    await selectAll.click();
    assert.equal(await checked.count(), 1);
    await form.locator(".pending-gateway").selectOption("");
    await selectAll.click();
    assert.equal(await checked.count(), 2);
    await form.locator(".pending-search").fill("Singapore");
    assert.equal(await form.locator(".pending-choice:visible").count(), 1);
    assert.equal(await checked.count(), 2, "hidden selections are retained");
    await clear.click();
    assert.equal(await checked.count(), 0, "clear must also clear hidden selections");
    assert.ok(await submit.isDisabled());
    await form.locator(".pending-search").fill("");
    await form.locator(".pending-agent").selectOption("");
    // Turn off an existing Link, then verify the pending list updates without reload.
    const csrf = await form.locator('input[name="csrf"]').inputValue();
    const response = await page.request.post(base + "/admin/links/l2/toggle", { form: { csrf } });
    assert.ok(response.ok());
    await page.locator("#refresh-status").click();
    await page.waitForFunction(() => document.querySelectorAll(".pending-choice").length === 5);
    const before = await (await page.request.get(base + "/admin/dashboard")).json();
    await selectAll.click();
    assert.equal(await checked.count(), 3);
    await page.locator("#refresh-status").click();
    await page.waitForTimeout(400);
    assert.equal(await checked.count(), 3, "refresh must preserve selection");
    const nextPending = form.locator('[data-agent-id="a2"][data-gateway-id="g2"] input');
    await nextPending.uncheck();
    await form.locator('.pending-agent').selectOption('a2');
    await form.locator('.pending-gateway').selectOption('g1');
    await form.locator('.pending-search').fill('Hong Kong');
    assert.equal(await checked.count(), 2, 'filtering must preserve hidden selections');
    const out = process.env.XMESH_PANEL_SCREENSHOTS || "tmp/panel-screenshots";
    fs.mkdirSync(out, { recursive: true });
    await page.locator("#assign").screenshot({ path: out + "/pending-connections-desktop.png" });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator("#assign").screenshot({ path: out + "/pending-connections-mobile.png" });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "mobile layout overflows");
    await page.setViewportSize({ width: 1440, height: 1100 });
    // Keep the completed response pending while preparing a new selection.
    // The request's target belongs to its own batch and should return to blank.
    await target.fill('completed-batch.example:443');
    let releasePending, capturedPending;
    const pendingGate = new Promise(resolve => { releasePending = resolve; });
    const pendingCaptured = new Promise(resolve => { capturedPending = resolve; });
    await page.route('**/admin/assign-connections', async route => {
      const response = await route.fetch();
      capturedPending();
      await pendingGate;
      await route.fulfill({ response });
    });
    await submit.click();
    try {
      await pendingCaptured;
      await form.locator('.pending-gateway').selectOption('');
      await form.locator('.pending-search').fill('');
      await nextPending.check();
      await form.locator('.pending-gateway').selectOption('g1');
      await form.locator('.pending-search').fill('Hong Kong');
    } finally { releasePending(); }
    await page.waitForFunction(() => !document.querySelector('#assign-pending-panel form').hasAttribute('aria-busy') &&
      !document.querySelector('#refresh-status').disabled);
    await page.unroute('**/admin/assign-connections');
    assert.equal(await target.inputValue(), '', 'the last connection batch carried its target into the new draft');
    assert.equal(await target.evaluate(input => input.defaultValue), '', 'the saved target became the next draft baseline');
    assert.equal(await nextPending.isChecked(), true, 'successful connection dropped a pending selection');
    assert.equal(await checked.count(), 1, 'submitted choices remained selected');
    assert.equal(await form.getAttribute('data-dirty'), 'true');
    const partial = await (await page.request.get(base + '/admin/dashboard')).json();
    assert.equal(partial.routes.some(route => route.gateway_id === 'g2' && route.agent_id === 'a2'), false,
      'the next connection was created before the second request');
    await submit.click();
    await page.waitForFunction(() => document.querySelector("#notice").textContent.includes("新增 1，重新启用 0"));
    await page.waitForFunction(() => document.querySelectorAll(".pending-choice").length === 2);
    assert.equal(await pendingTab.getAttribute("aria-selected"), "true", "tab must survive successful submit");
    assert.equal(await checked.count(), 0);
    assert.ok(await submit.isDisabled());
    assert.equal(await form.locator('.pending-agent').inputValue(), 'a2', 'successful connection reset the Agent filter');
    assert.equal(await form.locator('.pending-gateway').inputValue(), 'g1', 'successful connection reset the Gateway filter');
    assert.equal(await form.locator('.pending-search').inputValue(), 'Hong Kong', 'successful connection cleared the search');
    const after = await (await page.request.get(base + "/admin/dashboard")).json();
    assert.equal(after.routes.length, 4);
    assert.equal(after.links.length, 4);
    assert.ok(after.links.find((link) => link.id === "l2").enabled);
    assert.deepEqual(accessRecords(after.grants), accessRecords(before.grants), "bulk connection must not grant access");
    assert.deepEqual(after.nodes.filter((n) => n.role === "Gateway").map((n) => n.target), before.nodes.filter((n) => n.role === "Gateway").map((n) => n.target));
    await form.locator(".pending-agent").selectOption("a2");
    await form.locator(".pending-gateway").selectOption("g1");
    assert.ok(await form.locator(".pending-empty").isVisible());
    assert.ok(await selectAll.isDisabled());
    // A successful per-Agent submission must retain its owner and filters,
    // including filters on the other tab in the same section.
    await page.getByRole('tab', { name: '按 Agent 连接', exact: true }).click();
    const assignment = page.locator('#assign-agent-panel form');
    await assignment.locator('[name="agent_id"]').selectOption('a2');
    await assignment.locator('.selection-search').fill('Hong Kong');
    await assignment.locator('.selection-filter').selectOption('active');
    await assignment.locator('input[name="gateway_id"][value="g1"]').check();
    await assignment.locator('button[type="submit"]').click();
    await page.waitForFunction(() => document.querySelector('#notice').textContent.includes('Gateway 连接：新增 0，重新启用 0，已开启 1'));
    await page.waitForFunction(() => !document.querySelector('#refresh-status').disabled);
    assert.equal(await assignment.locator('[name="agent_id"]').inputValue(), 'a2', 'successful assignment switched to another Agent');
    assert.equal(await assignment.locator('.selection-search').inputValue(), 'Hong Kong');
    assert.equal(await assignment.locator('.selection-filter').inputValue(), 'active');
    assert.equal(await form.locator('.pending-agent').inputValue(), 'a2');
    assert.equal(await form.locator('.pending-gateway').inputValue(), 'g1');
    assert.equal(await form.locator('.pending-search').inputValue(), 'Hong Kong');
    assert.deepEqual(accessRecords((await (await page.request.get(base + '/admin/dashboard')).json()).grants), accessRecords(before.grants));
    await assignment.locator('.selection-search').fill('');
    const firstGateway=assignment.locator('input[name="gateway_id"][value="g1"]');
    const nextGateway=assignment.locator('input[name="gateway_id"][value="g2"]');
    const delayAssignment=async editPending=>{
      let release,captured;
      const gate=new Promise(resolve=>{release=resolve;}),started=new Promise(resolve=>{captured=resolve;});
      await page.route('**/admin/assign-gateways',async route=>{
        const response=await route.fetch();captured();await gate;await route.fulfill({response});
      });
      await assignment.locator('button[type="submit"]').click();
      try {await started;await editPending();} finally {release();}
      await page.waitForFunction(()=>!document.querySelector('#assign-agent-panel form').hasAttribute('aria-busy') &&
        !document.querySelector('#refresh-status').disabled);
      await page.unroute('**/admin/assign-gateways');
    };
    await firstGateway.check();
    await delayAssignment(async()=>{await nextGateway.check();});
    assert.equal(await firstGateway.isChecked(),false,'completed Gateway choices remained in the next batch');
    assert.equal(await nextGateway.isChecked(),true,'next Gateway selection was lost during save');
    await nextGateway.uncheck();
    assert.equal(await assignment.getAttribute('data-dirty'),null);
    await firstGateway.check();
    await delayAssignment(async()=>{
      await assignment.locator('[name="agent_id"]').selectOption('a1');
      await firstGateway.check();
    });
    assert.equal(await assignment.locator('[name="agent_id"]').inputValue(),'a1');
    assert.equal(await firstGateway.isChecked(),true,'completion for one Agent cleared another Agent\'s connection draft');
    await firstGateway.uncheck();
    assert.equal(await assignment.getAttribute('data-dirty'),null);
    assert.deepEqual(accessRecords((await (await page.request.get(base + '/admin/dashboard')).json()).grants),accessRecords(before.grants));
    assert.deepEqual(errors, []);
    console.log("Connection tab browser checks passed: all agents, missing/disabled pairs, filters, select/clear, keyboard, refresh, bulk enable, targets, grants and mobile layout.");
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
