// Run fresh TestPanelBrowserFixture and TestMonitorBrowserFixture instances first.
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");

(async () => {
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.XMESH_BROWSER_CHANNEL || undefined,
    executablePath: process.env.XMESH_BROWSER_EXECUTABLE || undefined,
  });
  const base = process.env.XMESH_PANEL_TEST_URL || "http://127.0.0.1:18088";
  const monitor = process.env.XMESH_MONITOR_TEST_URL || "http://127.0.0.1:18090";
  const errors = [];
  const track = (page) => page.on("pageerror", (error) => errors.push(error.message));
  const selectLanguage = async (page, language) => {
    await Promise.all([
      page.waitForEvent("load"),
      page.locator("[data-language-select]").selectOption(language),
    ]);
    assert.equal(await page.locator("html").getAttribute("lang"), language === "zh" ? "zh-CN" : "en-US");
  };
  const signIn = async (page, origin) => {
    await page.goto(origin + "/login");
    await page.locator('[name="username"]').fill("admin");
    await page.locator('[name="password"]').fill("a-strong-test-password");
    await page.locator('form[action="/login"] button').click();
    await page.locator("#metrics .metric").first().waitFor();
  };
  try {
    for (const [locale, expected] of [["en-GB", "en-US"], ["zh-HK", "zh-CN"], ["fr-FR", "en-US"]]) {
      const context = await browser.newContext({locale});
      const page = await context.newPage(); track(page);
      await page.goto(base + "/login");
      assert.equal(await page.locator("html").getAttribute("lang"), expected);
      assert.equal(await page.locator('form[action="/login"] button').textContent(), expected === "zh-CN" ? "登录" : "Sign in");
      await context.close();
    }
    const context = await browser.newContext({locale: "zh-CN", viewport: {width: 1440, height: 1000}});
    const page = await context.newPage(); track(page);
    await page.goto(base + "/login");
    await selectLanguage(page, "en");
    assert.equal(await page.evaluate(() => localStorage.getItem("xmesh.language")), "en");
    await signIn(page, base);
    await page.waitForFunction(() => document.querySelector("#refresh-note").textContent.startsWith("Updated"));
    assert.equal(await page.locator("#page-title").textContent(), "Overview");
    assert.equal(await page.locator("#map-search").getAttribute("placeholder"), "Name or address");
    await page.goto(base + "/#assign");
    await page.locator("#assign-agent-panel .selection-summary").waitFor();
    assert.match(await page.locator("#assign-agent-panel .selection-summary").textContent(), /Selected/);
    await page.goto(base + "/#users");
    await page.locator("#users .create-details").evaluate((node) => { node.open = true; });
    await page.locator('#users input[name="name"]').fill("总览");
    await page.locator('#users form[action="/admin/users"] button').click();
    await page.locator("#users .form-success").waitFor();
    await page.locator("#users tbody").getByRole("cell", {name: /总览/}).first().waitFor();
    // A server-rendered replacement retains English labels, and the Chinese user name is unchanged.
    assert.equal(await page.locator('#users form[action="/admin/users"] button').textContent(), "Create user");
    await page.locator("#refresh-status").click();
    await page.waitForFunction(() => !document.querySelector("#refresh-status").disabled);
    await page.goto(base + "/#overview");
    fs.mkdirSync("tmp/i18n-screenshots", {recursive: true});
    await page.screenshot({path: "tmp/i18n-screenshots/panel-en.png", fullPage: true});
    await page.setViewportSize({width: 390, height: 844});
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "English panel overflows on mobile");
    await page.screenshot({path: "tmp/i18n-screenshots/panel-mobile-en.png", fullPage: true});
    const state = await context.storageState();
    await context.close();
    // A new browser session with a Chinese locale still uses the saved English preference.
    const restored = await browser.newContext({locale: "zh-CN", storageState: state});
    const returning = await restored.newPage(); track(returning);
    await returning.goto(base + "/");
    await returning.locator("#metrics .metric").first().waitFor();
    assert.equal(await returning.locator("#page-title").textContent(), "Overview");
    await selectLanguage(returning, "zh");
    assert.equal(await returning.locator("#page-title").textContent(), "总览");
    await restored.close();

    const publicContext = await browser.newContext({locale: "en-US", viewport: {width: 1440, height: 1000}});
    const publicPage = await publicContext.newPage(); track(publicPage);
    await publicPage.goto(monitor + "/monitor");
    await publicPage.waitForFunction(() => document.querySelector("#updated").textContent.startsWith("Updated"));
    assert.equal(await publicPage.locator("h1").textContent(), "Network connection status");
    assert.match(await publicPage.locator("#matrix").textContent(), /Unprobed/);
    assert.match(await publicPage.locator("#nodes").textContent(), /香港入口/); // Public aliases are opaque.
    await publicPage.locator("#matrix button.ready").click();
    assert.match(await publicPage.locator("#detail summary").textContent(), /Sample data/);
    await publicPage.screenshot({path: "tmp/i18n-screenshots/monitor-en.png", fullPage: true});
    await publicPage.setViewportSize({width: 390, height: 844});
    assert.ok(await publicPage.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "English monitor overflows on mobile");
    await selectLanguage(publicPage, "zh");
    await publicPage.waitForFunction(() => document.querySelector("#updated").textContent.startsWith("已更新"));
    assert.equal(await publicPage.locator("h1").textContent(), "网络连接状态");
    await signIn(publicPage, monitor);
    await publicPage.goto(monitor + "/admin/monitor");
    assert.equal(await publicPage.locator("h1").textContent(), "公开监控设置");
    await selectLanguage(publicPage, "en");
    assert.equal(await publicPage.locator("h1").textContent(), "Public monitor settings");
    await publicPage.getByRole("button", {name: "Save public settings", exact: true}).click();
    await publicPage.getByText("Public monitor settings saved.", {exact: true}).waitFor();
    await publicPage.goto(monitor + "/monitor");
    assert.equal(await publicPage.locator("h1").textContent(), "Network connection status");
    await publicContext.close();

    const blocked = await browser.newContext({locale: "en-US"});
    await blocked.addInitScript(() => {
      Object.defineProperty(window, "localStorage", {get() { throw new DOMException("blocked", "SecurityError"); }});
    });
    const blockedPage = await blocked.newPage(); track(blockedPage);
    await blockedPage.goto(base + "/login");
    await selectLanguage(blockedPage, "zh");
    assert.equal(await blockedPage.locator('form[action="/login"] button').textContent(), "登录");
    await blocked.close();
    assert.deepEqual(errors, []);
    console.log("Bilingual browser checks passed");
  } finally {
    await browser.close();
  }
})().catch((error) => { console.error(error); process.exitCode = 1; });
