const {chromium} = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const zlib = require("node:zlib");

(async()=>{
  const base=process.env.XMESH_AUDIT_BROWSER_URL||"http://127.0.0.1:18093";
  const out="tmp/audit-screenshots";fs.mkdirSync(out,{recursive:true});
  const browser=await chromium.launch({headless:true,channel:process.env.XMESH_BROWSER_CHANNEL||"msedge"});
  try{
    const context=await browser.newContext({viewport:{width:1440,height:1050},locale:"zh-CN"});
    const page=await context.newPage();const errors=[];page.on("pageerror",e=>errors.push(e.message));
    await page.goto(base+"/login");
    await page.locator('[name="username"]').fill("admin");await page.locator('[name="password"]').fill("a-strong-test-password");
    await Promise.all([page.waitForURL(base+"/"),page.locator('button[type="submit"], button').first().click()]);
    await page.goto(base+"/#users");await page.locator("#user-audit").waitFor({state:"visible"});
    assert.match(await page.locator("#user-audit").innerText(),/默认关闭用户/);
    await page.goto(base+"/admin/users/other/audit");assert.equal(await page.locator('[name="enabled"]').isChecked(),false);
    let data=await (await context.request.get(base+"/admin/users/other/audit/data")).json();assert.equal(data.view.events.length,0);
    await page.goto(base+"/admin/users/u/audit");
    assert.equal(await page.locator('[name="enabled"]').isChecked(),true);
    assert.match(await page.locator("body").innerText(),/短时间大量连接/);
    assert.match(await page.locator("body").innerText(),/audited\.example/);
    await page.locator('[name="host"]').fill("audited.example");await page.getByRole("button",{name:"筛选与刷新"}).click();
    assert.equal(await page.locator("tbody tr").count(),1);
    assert.match(await page.locator("tbody").innerText(),/123 B/);
    await page.screenshot({path:out+"/desktop.png",fullPage:true});
    const response=await context.request.get(base+"/admin/users/u/audit/download");assert.equal(response.status(),200);
    const exported=zlib.gunzipSync(await response.body()).toString();assert.match(exported,/audited\.example/);assert.ok(!exported.includes("默认关闭用户"));
    await Promise.all([page.waitForEvent("domcontentloaded"),page.locator('[data-language-select]').selectOption("en")]);assert.match(await page.locator("body").innerText(),/User audit/);
    await Promise.all([page.waitForEvent("domcontentloaded"),page.locator('[data-language-select]').selectOption("zh")]);
    await page.setViewportSize({width:390,height:844});await page.screenshot({path:out+"/mobile.png",fullPage:true});
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth+1),"mobile page overflows");
    await page.locator('[name="enabled"]').uncheck();await page.getByRole("button",{name:"保存设置"}).click();
    data=await (await context.request.get(base+"/admin/users/u/audit/data")).json();assert.equal(data.enabled,false);assert.ok(data.view.events.length>0,"disabling erased history");
    await page.getByText("清空该用户日志",{exact:true}).click();await page.getByRole("button",{name:"确认清空"}).click();
    data=await (await context.request.get(base+"/admin/users/u/audit/data")).json();assert.equal(data.view.events.length,0);assert.equal(data.view.storage_bytes,0);
    assert.deepEqual(errors,[]);console.log("User audit browser checks passed");
  }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
