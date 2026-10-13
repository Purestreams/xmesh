// Run against the isolated TestUpgradeRecoveryBrowserFixture.
const { chromium } = require("playwright");
const assert = require("node:assert/strict");

(async () => {
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.XMESH_BROWSER_CHANNEL || undefined,
    executablePath: process.env.XMESH_BROWSER_EXECUTABLE || undefined,
  });
  try {
    const page = await browser.newPage({locale: "zh-CN"});
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    const base = process.env.XMESH_UPGRADE_BROWSER_URL || "http://127.0.0.1:18089";
    await page.goto(base + "/login");
    await page.getByLabel("用户名").fill("admin");
    await page.getByLabel("密码").fill("a-strong-test-password");
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.goto(base + "/#node-upgrades");
    await page.waitForFunction(() =>
      document.querySelector("#refresh-note").textContent.startsWith("已更新"),
    );
    const resume = page.getByRole("button", { name: "重新检查并继续", exact: true });
    assert.equal(await resume.count(), 1);
    assert.match(await page.locator("#node-upgrades").textContent(), /批次 已暂停/);
    const response = page.waitForResponse((response) =>
      response.url().endsWith("/admin/upgrades/first/resume") && response.request().method() === "POST",
    );
    await resume.click();
    assert.equal((await response).status(), 303);
    await page.goto(base + "/#node-upgrades");
    await page.waitForFunction(() =>
      document.querySelector("#node-upgrades").textContent.includes("批次 执行中"),
    );
    assert.equal(await resume.count(), 0);
    const dashboard = await page.request.get(base + "/admin/dashboard");
    assert.equal(dashboard.status(), 200);
    const state = await dashboard.json();
    assert.equal(state.upgrade_batches.batch.stage, "running");
    assert.equal(state.upgrade_tasks.find((task) => task.id === "first").stage, "succeeded");
    assert.equal(state.upgrade_tasks.find((task) => task.id === "second").stage, "pending");
    assert.deepEqual(errors, []);
    console.log("Upgrade recovery browser checks passed");
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
