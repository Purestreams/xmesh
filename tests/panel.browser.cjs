// Start TestPanelBrowserFixture first; install Playwright outside the repository.
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");

(async () => {
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.XMESH_BROWSER_CHANNEL || undefined,
  });
  try {
    const page = await browser.newPage({
        viewport: { width: 1440, height: 1100 },
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
    await page.getByLabel("Username").fill("admin");
    await page.getByLabel("Password").fill("a-strong-test-password");
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await page.waitForFunction(() =>
      document.querySelector("#refresh-note").textContent.startsWith("已更新"),
    );
    await page.locator("#metrics .metric").first().waitFor();
    assert.equal(await page.locator("section[data-page]:visible").count(), 4);
    assert.equal(await page.locator("#topology svg .node").count(), 5);
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
    assert.ok(await page.locator("#assign button[type=submit]").isDisabled());
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
      document.querySelector("#notice").textContent.includes("2 created"),
    );
    assert.equal(await page.locator("#grants tbody tr").count(), 3);
    await page
      .locator("#subscribe")
      .getByRole("button", { name: "全选筛选结果" })
      .click();
    await page.locator("#subscribe button[type=submit]").click();
    await page.waitForFunction(() =>
      document.querySelector("#notice").textContent.includes("already active"),
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
      "Panel browser checks passed: navigation, topology, matrix, selection, refresh, grants, validation, audit, mobile.",
    );
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
