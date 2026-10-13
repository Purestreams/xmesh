const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");

(async () => {
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.XMESH_BROWSER_CHANNEL || undefined,
    executablePath: process.env.XMESH_BROWSER_EXECUTABLE || undefined,
  });
  const errors = [];
  try {
    const page = await browser.newPage({locale:"zh-CN", viewport:{width:1440,height:1000}});
    page.on("pageerror",error => errors.push(error.message));
    const base = process.env.XMESH_PROBE_BROWSER_URL || "http://127.0.0.1:18093";
    await page.goto(base + "/login");
    await page.locator('[name="username"]').fill("admin");
    await page.locator('[name="password"]').fill("a-strong-test-password");
    await page.locator('form[action="/login"] button').click();
    await page.locator("#metrics .metric").first().waitFor();
    await page.goto(base + "/#upstreams");
    const summary = page.locator('[data-upstream-probe="route"]');
    await summary.getByText("出口 IP：155.248.187.71").waitFor();
    assert.match(await summary.textContent(), /ip\.sb：成功.*HTTP 200.*123 ms/);
    assert.match(await summary.textContent(), /microsoft\.com：成功.*HTTP 200.*456 ms/);
    assert.match(await summary.textContent(), /300 秒/);
    const test = page.locator('#upstreams form[action="/admin/attachments/route/probe"] button');
    assert.equal(await test.isEnabled(), true);
    await test.click();
    await page.locator("#upstreams .form-success").waitFor();
    assert.match(await page.locator("#upstreams .form-success").textContent(), /测试请求已提交/);
    await summary.getByText("HTTP 403", {exact:true}).waitFor();
    assert.match(await summary.textContent(), /microsoft\.com：失败.*HTTP 403/);
    const routeSection = await page.locator("#route-overview").evaluate(node => node.closest("section").id);
    await page.goto(base + "/#" + routeSection);
    await page.locator("#route-overview .badge.probe_failed").waitFor();
    assert.equal(await page.locator('#route-overview form[action="/admin/attachments/route/probe"] button').count(),1);
    await page.evaluate(() => localStorage.setItem("xmesh.language", "en"));
    await page.reload();
    await page.goto(base + "/#upstreams");
    await summary.getByText("Exit IP: 155.248.187.71").waitFor();
    assert.match(await summary.textContent(), /microsoft\.com: Failed/);
    await page.setViewportSize({width:390,height:844});
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),true);
    fs.mkdirSync("tmp/upstream-probe-screenshots",{recursive:true});
    await page.screenshot({path:"tmp/upstream-probe-screenshots/upstream-tests-mobile.png",fullPage:true});
    assert.deepEqual(errors,[]);
    process.stdout.write("Upstream probe browser regression passed.\n");
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode=1; });
