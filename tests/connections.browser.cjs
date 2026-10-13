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
    page.on("pageerror", (error) => errors.push(error.message));
    const base = process.env.XMESH_PANEL_TEST_URL || "http://127.0.0.1:18088";
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
    const out = process.env.XMESH_PANEL_SCREENSHOTS || "tmp/panel-screenshots";
    fs.mkdirSync(out, { recursive: true });
    await page.locator("#assign").screenshot({ path: out + "/pending-connections-desktop.png" });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator("#assign").screenshot({ path: out + "/pending-connections-mobile.png" });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "mobile layout overflows");
    await page.setViewportSize({ width: 1440, height: 1100 });
    await submit.click();
    await page.waitForFunction(() => document.querySelector("#notice").textContent.includes("新增 2，重新启用 1"));
    await page.waitForFunction(() => document.querySelectorAll(".pending-choice").length === 2);
    assert.equal(await pendingTab.getAttribute("aria-selected"), "true", "tab must survive successful submit");
    assert.equal(await checked.count(), 0);
    assert.ok(await submit.isDisabled());
    const after = await (await page.request.get(base + "/admin/dashboard")).json();
    assert.equal(after.routes.length, 4);
    assert.equal(after.links.length, 4);
    assert.ok(after.links.find((link) => link.id === "l2").enabled);
    assert.deepEqual(after.grants, before.grants, "bulk connection must not grant access");
    assert.deepEqual(after.nodes.filter((n) => n.role === "Gateway").map((n) => n.target), before.nodes.filter((n) => n.role === "Gateway").map((n) => n.target));
    await form.locator(".pending-agent").selectOption("a2");
    await form.locator(".pending-gateway").selectOption("g1");
    assert.ok(await form.locator(".pending-empty").isVisible());
    assert.ok(await selectAll.isDisabled());
    assert.deepEqual(errors, []);
    console.log("Connection tab browser checks passed: all agents, missing/disabled pairs, filters, select/clear, keyboard, refresh, bulk enable, targets, grants and mobile layout.");
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
