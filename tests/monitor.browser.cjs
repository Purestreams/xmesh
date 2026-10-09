// Run TestMonitorBrowserFixture with XMESH_MONITOR_TEST_ADDR first.
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");

(async () => {
  const browser = await chromium.launch({headless: true, channel: process.env.XMESH_BROWSER_CHANNEL || "msedge"});
  const base = process.env.XMESH_MONITOR_TEST_URL || "http://127.0.0.1:18089";
  const out = process.env.XMESH_MONITOR_SCREENSHOTS || "tmp/monitor-screenshots";
  fs.mkdirSync(out, {recursive: true});
  const secrets = ["private-", "192.0.2.42", "203.0.113.5", "192.168.0.0", "123456789"];
  const errors = [];
  try {
    const context = await browser.newContext({viewport: {width: 1440, height: 1050}});
    const page = await context.newPage();
    page.on("pageerror", (error) => errors.push(error.message));
    const responses = [];
    page.on("response", (response) => {
      if (response.url().startsWith(base) && response.ok()) {
        responses.push(response.text().then((body) => {
          for (const secret of secrets) assert.ok(!body.includes(secret), `${response.url()} leaked ${secret}`);
        }));
      }
    });
    await page.goto(base + "/monitor");
    await page.getByText("已更新", {exact: false}).waitFor();
    assert.equal(await page.locator("header").evaluate((node) => getComputedStyle(node).backgroundColor), "rgb(18, 47, 58)", "monitor stylesheet blocked or missing");
    assert.equal(await page.locator(".node").count(), 3);
    assert.equal(await page.locator("#matrix button").count(), 2);
    assert.match(await page.locator("#matrix").textContent(), /52\.3 ms/);
    assert.match(await page.locator("#matrix").textContent(), /未探测/);
    assert.equal((await context.cookies()).length, 0);
    await page.locator("#matrix button.ready").click();
    assert.match(await page.locator("#detail-title").textContent(), /香港入口(?: A)? → 东京出口/);
    assert.equal(await page.locator("#detail canvas").count(), 1);
    await page.locator("#detail summary").click();
    assert.equal(await page.locator("#detail tbody tr").count(), 3);
    await page.locator("#detail summary").click();
    await page.screenshot({path: out + "/desktop.png", fullPage: true});
    await page.locator("#search").fill("东京");
    assert.equal(await page.locator("#matrix button").count(), 1);
    await page.locator("#search").fill("无匹配");
    assert.match(await page.locator("#matrix").textContent(), /没有匹配/);
    await page.locator("#search").fill("");
    await page.locator("#refresh").click();
    await page.waitForFunction(() => !document.getElementById("refresh").disabled);
    assert.equal(await page.locator("#detail:visible").count(), 1);
    await page.setViewportSize({width: 390, height: 844});
    await page.screenshot({path: out + "/mobile.png", fullPage: true});
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "mobile page overflows");
    // A failed refresh must immediately remove live RTT and healthy counts.
    await page.route("**/api/public/monitor", (route) => route.fulfill({status: 503, body: "unavailable"}));
    await page.locator("#refresh").click();
    await page.locator("#notice").waitFor({state: "visible"});
    assert.equal(await page.locator("#matrix button.ready").count(), 0);
    assert.ok(!(await page.locator("#matrix").textContent()).includes("52.3 ms"));
    await page.unroute("**/api/public/monitor");
    await page.locator("#refresh").click();
    await page.waitForFunction(() => document.getElementById("notice").hidden);
    await page.route("**/api/public/monitor", (route) => route.fulfill({status: 404, body: "not found"}));
    await page.locator("#refresh").click();
    await page.getByText("公开监控已关闭。", {exact: true}).waitFor();
    assert.equal(await page.locator(".node").count(), 0);
    assert.equal(await page.locator("#matrix button").count(), 0);
    await Promise.all(responses);
    assert.deepEqual(errors, []);
    await context.close();

    // A separate admin context is the only one that receives operational names.
    const admin = await browser.newPage({viewport: {width: 1440, height: 1050}});
    await admin.goto(base + "/admin/monitor");
    assert.match(admin.url(), /\/login$/);
    await admin.getByLabel("Username").fill("admin");
    await admin.getByLabel("Password").fill("a-strong-test-password");
    await admin.getByRole("button", {name: "Sign in", exact: true}).click();
    await admin.goto(base + "/admin/monitor");
    await admin.getByRole("heading", {name: "公开监控设置", exact: true}).waitFor();
    assert.match(await admin.locator("body").textContent(), /192\.0\.2\.42/);
    await admin.locator('input[name="node_private-gw"]').fill("香港入口 A");
    const saved = admin.waitForResponse((response) => response.request().method() === "POST" && response.url() === base + "/admin/monitor");
    await admin.getByRole("button", {name: "保存公开设置"}).click();
    const saveResponse = await saved;
    if (saveResponse.status() !== 303) throw new Error(`Save failed: ${saveResponse.status()} ${await saveResponse.text()}`);
    await admin.waitForURL("**/admin/monitor?saved=1");
    await admin.getByText("公开监控设置已保存。", {exact: true}).waitFor();
    await admin.screenshot({path: out + "/settings.png", fullPage: true});
    const anonymous = await browser.newPage();
    await anonymous.goto(base + "/monitor");
    await anonymous.getByText("香港入口 A", {exact: true}).first().waitFor();
    await admin.locator('input[name="enabled"]').uncheck();
    await admin.getByRole("button", {name: "保存公开设置"}).click();
    await admin.getByText("公开监控设置已保存。", {exact: true}).waitFor();
    await anonymous.locator("#refresh").click();
    await anonymous.getByText("公开监控已关闭。", {exact: true}).waitFor();
    assert.equal(await anonymous.locator(".node").count(), 0);
    console.log("Monitor browser checks passed: anonymous isolation, matrix, history, mobile, failure recovery, settings and revocation.");
  } finally {
    await browser.close();
  }
})().catch((error) => { console.error(error); process.exit(1); });
