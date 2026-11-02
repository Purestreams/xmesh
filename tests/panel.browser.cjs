// Start TestPanelBrowserFixture first; keep Playwright in a disposable test directory.
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
    const page = await browser.newPage({
        locale: "zh-CN", viewport: { width: 1440, height: 1100 },
      }),
      errors = [];
    let expectedInvalid = false;
    page.on("pageerror", (e) => errors.push(e.message));
    page.on("console", (m) => {
      if (
        m.type() === "error" &&
        !expectedInvalid &&
        !m.text().includes("favicon.ico")
      )
        errors.push(m.text());
    });
    const base = process.env.XMESH_PANEL_TEST_URL || "http://127.0.0.1:18088";
    await page.goto(base + "/login");
    await page.getByLabel("用户名").fill("admin");
    await page.getByLabel("密码").fill("a-strong-test-password");
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.waitForFunction(() =>
      document.querySelector("#refresh-note").textContent.startsWith("已更新"),
    );
    await page.locator("#metrics .metric").first().waitFor();
    assert.equal(await page.locator("section[data-page]:visible").count(), 4);
    assert.equal(await page.locator("#topology svg .node").count(), 4);
    assert.equal(await page.locator("#topology svg .route-label").count(), 0);
    assert.equal(await page.locator("#topology .topology-routes tbody tr").count(), 2);
    assert.match(await page.locator("#topology .topology-routes tbody tr").first().textContent(), /35\.0 ms/);
    const out = process.env.XMESH_PANEL_SCREENSHOTS || "tmp/panel-screenshots";
    fs.mkdirSync(out, { recursive: true });
    await page.screenshot({ path: out + "/overview.png", fullPage: true });
    await page.getByRole("button", { name: "矩阵", exact: true }).click();
    await page.locator("#matrix").waitFor({ state: "visible" });
    await page.locator("#matrix button.ready").first().click();
    assert.match(await page.locator("#detail-title").textContent(), /节点组合：Hong Kong Edge \/ Home Network/);
    assert.match(await page.locator("#detail-body").textContent(), /Link：Route 1/);
    await page.locator("#close-detail").click();
    await page.locator("#matrix button.unassigned").first().click();
    await page.locator("#assign").waitFor({ state: "visible" });
    assert.equal(
      await page.locator("#assign input[type=checkbox]:checked").count(),
      1,
    );
    await page.locator("#assign .selection-search").fill("Hong Kong");
    await page
      .locator("#assign")
      .getByRole("button", { name: "清除选择" })
      .click();
    await page
      .locator("#assign")
      .getByRole("button", { name: "全选筛选结果" })
      .click();
    assert.equal(
      await page.locator("#assign input[type=checkbox]:checked").count(),
      1,
    );
    await page.locator("#refresh-status").click();
    await page.waitForTimeout(400);
    assert.equal(
      await page.locator("#assign input[type=checkbox]:checked").count(),
      1,
    );
    await page
      .locator("#assign")
      .getByRole("button", { name: "清除选择" })
      .click();
    assert.equal(
      await page.locator("#assign input[type=checkbox]:checked").count(),
      0,
    );
    assert.ok(await page.locator("#assign-agent-panel button[type=submit]").isDisabled());
    await page.goto(base + "/#subscribe");
    await page.locator("#subscribe .selection-summary").waitFor();
    await page.locator("#subscribe select[name=user_id]").selectOption("u2");
    await page
      .locator("#subscribe")
      .getByRole("button", { name: "全选筛选结果" })
      .click();
    assert.equal(await page.locator("#subscribe input:checked").count(), 2);
    await page.locator("#subscribe button[type=submit]").click();
    await page.waitForFunction(() =>
      document.querySelector("#notice").textContent.includes("新增 2"),
    );
    assert.equal(await page.locator("#grants tbody tr").count(), 3);
    await page
      .locator("#subscribe")
      .getByRole("button", { name: "全选筛选结果" })
      .click();
    await page.locator("#subscribe button[type=submit]").click();
    await page.waitForFunction(() =>
      document.querySelector("#notice").textContent.includes("已开启"),
    );
    assert.equal(await page.locator("#grants tbody tr").count(), 3);
    await page.goto(base + "/#users");
    await page.locator("#usage-table tbody tr").first().waitFor();
    assert.match(await page.locator("#usage-table tbody tr").first().textContent(), /1.0 KiB/);
    await page.locator("#users details.create-details summary").click();
    await page.locator("#users input[name=name]").fill("Browser QA user");
    await page.locator("#refresh-status").click();
    await page.waitForTimeout(400);
    assert.equal(
      await page.locator("#users input[name=name]").inputValue(),
      "Browser QA user",
    );
    await page.locator('#users form[action="/admin/users"] button').click();
    await page.getByRole("cell", { name: /Browser QA user/ }).waitFor();
    // Native validation reveals advanced required fields instead of trapping focus.
    await page.goto(base + "/#create-route");
    assert.ok(await page.locator('#create-route input[name="vmess_enabled"]').isChecked());
    assert.ok(await page.locator('#create-route input[name="hy2_enabled"]').isChecked());
    const region = page.locator("#create-route select[name=region]");
    const realityTarget = page.locator(
      "#create-route input[name=reality_target]",
    );
    await region.selectOption("cn");
    assert.equal(await realityTarget.inputValue(), "api.bilibili.com:443");
    await region.selectOption("overseas");
    assert.equal(await realityTarget.inputValue(), "www.swift.com:443");
    await realityTarget.fill("custom.example:443");
    await region.selectOption("cn");
    assert.equal(await realityTarget.inputValue(), "custom.example:443");
    await page
      .locator("#create-route input[name=gateway_name]")
      .fill("New edge");
    await page
      .locator("#create-route input[name=agent_name]")
      .fill("New agent");
    await page
      .locator("#create-route input[name=public_host]")
      .fill("203.0.113.7");
    assert.equal(
      await page.locator("#create-route input[name=link_url]").inputValue(),
      "reality://203.0.113.7:8443/tunnel",
    );
    expectedInvalid = true;
    await page
      .locator("#create-route input[name=reality_target]")
      .fill("invalid target");
    await page.locator("#create-route button").click();
    await page.locator("#notice.error").waitFor();
    assert.equal(
      await page.locator("#create-route input[name=gateway_name]").inputValue(),
      "New edge",
    );
    expectedInvalid = false;
    await page.goto(base + "/#maintenance");
    assert.match(await page.locator("#node-upgrades table").first().textContent(), /节点 v0\.3\.4 · 升级助手 v0\.3\.3/);
    assert.match(await page.locator('#node-upgrades form[action="/admin/upgrades"] fieldset').textContent(), /Agent Home Network · 在线 · v0\.3\.4/);
    await page.locator("#operation-list tbody tr").first().waitFor();
    assert.ok((await page.locator("#operation-list tbody tr").count()) >= 4);
    // Create each node type through the deployment wizard, then connect an external exit.
    await page.goto(base + "/#wizard-gateway");
    assert.ok(await page.locator('#wizard-gateway input[name="vmess_enabled"]').isChecked());
    assert.ok(await page.locator('#wizard-gateway input[name="hy2_enabled"]').isChecked());
    await page.locator('#wizard-gateway input[name="name"]').fill("Browser QA Gateway");
    await page.locator('#wizard-gateway input[name="public_host"]').fill("qa.example.com");
    await page.locator('#wizard-gateway select[name="region"]').selectOption("overseas");
    await page.locator('#wizard-gateway form[action="/admin/gateways"] button').click();
    await page.locator("#wizard-gateway .form-success").waitFor();
    await page.goto(base + "/#gateways");
    const gatewayRow = page.locator('#gateways tbody tr').filter({ hasText: "Browser QA Gateway" });
    const ingressForm = gatewayRow.locator('form.ingress-controls');
    for (const [vmess, hy2] of [[false, true], [true, false], [false, false], [true, true]]) {
      await ingressForm.locator('input[name="vmess_enabled"]').setChecked(vmess);
      await ingressForm.locator('input[name="hy2_enabled"]').setChecked(hy2);
      await ingressForm.getByRole('button', { name: "保存接入开关" }).click();
      await page.locator('#gateways .form-success').waitFor();
      await page.waitForFunction(() => !document.querySelector('#gateways form.ingress-controls[data-dirty]'));
      assert.equal(await ingressForm.locator('input[name="vmess_enabled"]').isChecked(), vmess);
      assert.equal(await ingressForm.locator('input[name="hy2_enabled"]').isChecked(), hy2);
      assert.match(await gatewayRow.locator('td').nth(1).textContent(), vmess ? /VMess\/WS qa\.example\.com/ : /VMess\/WS 已关闭/);
      assert.match(await gatewayRow.locator('td').nth(1).textContent(), hy2 ? /HY2 UDP qa\.example\.com/ : /HY2 已关闭/);
    }
    await page.screenshot({ path: out + "/ingress-switches.png", fullPage: true });
    await page.goto(base + "/#wizard-agent");
    await page.locator('#wizard-agent input[name="name"]').fill("Browser QA Agent");
    await page.locator('#wizard-agent form[action="/admin/agents"] button').click();
    await page.locator("#wizard-agent .form-success").waitFor();
    await page.goto(base + "/#quick-install");
    await page.getByRole("button", { name: "安装 Gateway：Browser QA Gateway" }).waitFor();
    await page.getByRole("button", { name: "安装 Agent：Browser QA Agent" }).waitFor();
    await page.goto(base + "/#upstreams");
    const sampleVMess = "vmess://" + Buffer.from(JSON.stringify({
      v: "2", ps: "Browser QA Exit", add: "exit.example.com", port: "443",
      id: "00000000-0000-4000-8000-000000000022", aid: "0", scy: "auto",
      net: "ws", type: "none", host: "exit.example.com", path: "/proxy", tls: "tls",
    })).toString("base64");
    await page.locator('#upstreams form[action="/admin/upstreams"] input[name="name"]').fill("Browser QA Exit");
    await page.locator('#upstreams form[action="/admin/upstreams"] input[name="source"]').fill(sampleVMess);
    await page.locator('#upstreams form[action="/admin/upstreams"] button').click();
    await page.locator("#upstreams .form-success").waitFor();
    await page.locator("#upstreams details.create-details").last().locator("summary").click();
    await page.locator('#upstreams form[action="/admin/upstream-attachments"] select[name="gateway_id"]').selectOption({ label: "Browser QA Gateway" });
    await page.locator('#upstreams form[action="/admin/upstream-attachments"] select[name="upstream_id"]').selectOption({ label: "Browser QA Exit" });
    await page.locator('#upstreams form[action="/admin/upstream-attachments"] button').click();
    await page.locator("#upstreams .form-success").waitFor();
    await page.goto(base + "/#network");
    await page.locator("#route-overview .route-card").filter({ hasText: "Browser QA Gateway / Browser QA Exit" }).waitFor();
    await page.screenshot({ path: out + "/network-with-external-exit.png", fullPage: true });
    await page.goto(base + "/#subscribe");
    assert.equal(await page.locator('#subscribe input[name="attachment_id"]').count(), 3);
    // A delayed save must not stop refreshing or submitting another form.
    // Deliver the older response last to exercise concurrent section updates.
    await page.goto(base + "/#agents");
    let releaseSave, markHeld, holdTimer;
    const saveGate = new Promise((resolve) => { releaseSave = resolve; });
    const held = new Promise((resolve) => { markHeld = resolve; });
    let agentSubmissions = 0;
    await page.route("**/admin/agents/a1/toggle", async (route) => {
      agentSubmissions++;
      const response = await route.fetch();
      markHeld();
      await saveGate;
      await route.fulfill({ response });
    });
    try {
      await page.locator('#agents form[action="/admin/agents/a1/toggle"] button').click();
      await Promise.race([held, new Promise((_, reject) => {
        holdTimer = setTimeout(() => reject(new Error("save did not reach fixture")), 5000);
      })]);
      clearTimeout(holdTimer);
      await page.evaluate(() => document.querySelector('#agents form[action="/admin/agents/a1/toggle"]').requestSubmit());
      await Promise.all([
        page.waitForResponse("**/admin/dashboard", { timeout: 5000 }),
        page.locator("#refresh-status").click(),
      ]);
      await page.locator('.sidebar a[href="#network"]').click();
      const otherSave = page.locator('#gateways form[action="/admin/gateways/g1/toggle"]');
      await otherSave.locator("button").click();
      await page.locator("#gateways .form-success").waitFor({ timeout: 5000 });
      assert.equal(agentSubmissions, 1, "duplicate submission escaped the form lock");
      assert.equal(await page.locator('#agents form[action="/admin/agents/a1/toggle"]').getAttribute("aria-busy"), "true");
      await page.locator('#gateways form[action="/admin/gateways/g1/toggle"]').evaluate((form) => {
        form.closest("tr").dataset.keepLatest = "true";
      });
      const completed = page.waitForResponse((response) => response.url().endsWith("/admin/agents/a1/toggle"));
      releaseSave();
      await completed;
      await page.waitForFunction(() => !document.querySelector('#agents form[action="/admin/agents/a1/toggle"]')?.hasAttribute("aria-busy"));
      assert.equal(await page.locator('#gateways form[action="/admin/gateways/g1/toggle"]').evaluate((form) => form.closest("tr").dataset.keepLatest), "true", "an older save response replaced newer controls");
    } finally {
      clearTimeout(holdTimer);
      releaseSave();
      await page.unroute("**/admin/agents/a1/toggle");
    }
    // Create without a host, then exercise authenticated node announcement and
    // manual override without reloading the panel.
    await page.goto(base + "/#wizard-gateway");
    const autoWizard = page.locator('#wizard-gateway form');
    await autoWizard.locator('[name="name"]').fill("Browser Auto Gateway");
    assert.equal(await autoWizard.locator('[name="public_host"]').getAttribute("required"), null);
    await autoWizard.locator('button').click();
    await page.locator('#wizard-gateway .form-success').waitFor();
    await page.locator('.sidebar a[href="#network"]').click();
    const autoRow = page.locator('#gateways tbody tr').filter({ hasText: 'Browser Auto Gateway' });
    const autoID = await autoRow.locator('td code').first().textContent();
    const csrf = await page.locator('input[name="csrf"]').first().inputValue();
    const tokenResponse = await page.request.post(base + '/admin/enrollments', { form: { csrf, role: 'gateway', node_id: autoID } });
    assert.equal(tokenResponse.status(), 200);
    const token = (await tokenResponse.text()).split('\n')[1].trim();
    const enrollment = await page.request.post(base + '/api/v1/enroll', { data: { token, role: 'gateway', node_id: autoID } });
    assert.equal(enrollment.status(), 201);
    const credential = (await enrollment.json()).credential;
    const announce = async (ip, id = autoID, key = credential) => {
      const response = await page.request.post(base + '/api/v1/gateway/address', { headers: { Authorization: 'Bearer ' + key }, data: { ip, sources: ['ip.sb', 'ipify'] } });
      assert.equal(response.status(), 204);
      await Promise.all([page.waitForResponse((r) => r.url().endsWith('/admin/dashboard')), page.locator('#refresh-status').click()]);
      await page.waitForFunction(({ id, ip }) => Array.from(document.querySelectorAll('#gateways tr')).find((r) => r.textContent.includes(id))?.querySelector('[data-public-ip]')?.textContent.includes(ip), { id, ip });
    };
    await announce('8.8.8.8');
    assert.match(await autoRow.locator('td').nth(1).textContent(), /8\.8\.8\.8/);
    assert.match(await autoRow.locator('[data-public-ip]').textContent(), /ip\.sb, ipify/);
    await autoRow.locator('details summary').click();
    const addressForm = autoRow.locator('form[action$="/edit"]');
    await addressForm.locator('[name="public_host"]').fill('manual.example');
    await addressForm.getByRole('button', { name: '保存', exact: true }).click();
    await page.locator('#gateways .form-success').waitFor();
    await announce('9.9.9.9');
    assert.match(await autoRow.locator('td').nth(1).textContent(), /manual\.example/);
    const editDetails = autoRow.locator('details');
    if (!(await editDetails.getAttribute('open') !== null)) await editDetails.locator('summary').click();
    await addressForm.locator('[name="public_host"]').fill('');
    await addressForm.getByRole('button', { name: '保存', exact: true }).click();
    await page.locator('#gateways .form-success').waitFor();
    await page.waitForFunction((id) => Array.from(document.querySelectorAll('#gateways tr')).find((r) => r.textContent.includes(id))?.cells[1].textContent.includes('9.9.9.9'), autoID);
    const pendingRoute = await page.request.post(base + '/admin/quick-setup', { form: {
      csrf, gateway_name: 'Pending IP Gateway', agent_name: 'Pending IP Agent', region: 'cn',
      vmess_port: '8080', vmess_path: '/proxy', allowed_cidrs: '0.0.0.0/0',
    } });
    assert.equal(pendingRoute.status(), 200);
    await page.reload();
    const pendingID = await page.locator('#gateways tbody tr').filter({ hasText: 'Pending IP Gateway' }).locator('td code').first().textContent();
    const pendingToken = await page.request.post(base + '/admin/enrollments', { form: { csrf, role: 'gateway', node_id: pendingID } });
    assert.equal(pendingToken.status(), 200);
    const pendingEnrollment = await page.request.post(base + '/api/v1/enroll', { data: { role: 'gateway', node_id: pendingID, token: (await pendingToken.text()).split('\n')[1].trim() } });
    assert.equal(pendingEnrollment.status(), 201);
    const pendingCredential = (await pendingEnrollment.json()).credential;
    const pendingLink = page.locator('#links>table:first-of-type tbody tr').filter({ hasText: 'Pending IP Gateway / Pending IP Agent' });
    const linkInput = pendingLink.locator('form[action$="/edit"] [name="url"]');
    assert.equal(await linkInput.inputValue(), '');
    assert.equal(await linkInput.getAttribute('required'), null);
    await announce('8.8.8.8', pendingID, pendingCredential);
    assert.equal(await linkInput.inputValue(), 'reality://8.8.8.8:8443/tunnel');
    assert.equal(await pendingLink.locator('td').nth(1).locator('code').textContent(), 'reality://8.8.8.8:8443/tunnel');
    await pendingLink.locator('details summary').click();
    await linkInput.fill('reality://manual-link.example:8443/tunnel');
    await announce('9.9.9.9', pendingID, pendingCredential);
    assert.equal(await linkInput.inputValue(), 'reality://manual-link.example:8443/tunnel', 'refresh discarded an edited URL');
    assert.equal(await pendingLink.locator('td').nth(1).locator('code').textContent(), 'reality://9.9.9.9:8443/tunnel');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(base + "/");
    await page.waitForFunction(() =>
      document.querySelector("#refresh-note").textContent.startsWith("已更新"),
    );
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.locator("#metrics .metric").first().waitFor();
    await page.screenshot({ path: out + "/mobile.png", fullPage: true });
    assert.ok(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth + 1,
      ),
      "page overflows mobile viewport",
    );
    assert.deepEqual(errors, []);
    console.log(
      "Panel browser checks passed: navigation, topology, matrix, selection, refresh, grants, validation, audit, node wizard, ingress defaults and all switch combinations, external exit, mobile.",
    );
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
