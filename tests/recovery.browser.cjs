// Uses only TestPanelBrowserFixture state. May also run after workspace.browser.cjs.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');

(async()=>{
  const browser=await chromium.launch({headless:true,
    executablePath:process.env.XMESH_BROWSER_EXECUTABLE||undefined,
    channel:process.env.XMESH_BROWSER_CHANNEL||undefined});
  try {
    const context=await browser.newContext({locale:'zh-CN',viewport:{width:1440,height:1000}});
    const page=await context.newPage(),errors=[];
    context.on('page',target=>target.on('pageerror',error=>errors.push(error.message)));
    page.on('pageerror',error=>errors.push(error.message));
    const base=process.env.XMESH_PANEL_TEST_URL||'http://127.0.0.1:18096';
    const ready=target=>target.waitForFunction(()=>!document.querySelector('form[aria-busy]') &&
      !document.querySelector('#refresh-status')?.disabled && document.querySelector('#refresh-note')?.textContent.startsWith('已更新'));
    const signIn=async target=>{
      await target.locator('[name="username"]').fill('admin');
      await target.locator('[name="password"]').fill('a-strong-test-password');
      await target.locator('form[action="/login"] button').click();await ready(target);
    };
    const refresh=async()=>{await page.locator('#refresh-status').click();await ready(page);};
    await page.goto(base+'/login');await signIn(page);
    await page.evaluate(()=>location.hash='quick-install');
    let releaseInstall,captureInstall;
    const gate=new Promise(resolve=>releaseInstall=resolve),started=new Promise(resolve=>captureInstall=resolve);
    let installPosts=0;
    await page.route('**/admin/enrollments',async route=>{
      installPosts++;const response=await route.fetch();captureInstall();await gate;await route.fulfill({response});
    });
    await page.locator('form[action="/admin/enrollments"]').filter({has:page.locator('[name="node_id"][value="g2"]')}).locator('button').click();
    await started;
    await page.locator('[data-language-select]').selectOption('en');
    assert.equal(await page.locator('[data-language-select]').inputValue(),'zh','pending install was lost during language reload');
    assert.match(await page.locator('#notice').textContent(),/操作正在进行/);
    const unloadSeen=new Promise(resolve=>page.once('dialog',async dialog=>{
      assert.equal(dialog.type(),'beforeunload');await dialog.dismiss();resolve();
    }));
    await page.evaluate(()=>setTimeout(()=>location.reload(),0));await unloadSeen;
    assert.equal(await page.locator('form[aria-busy]').count(),1,'cancelled reload lost the pending install');
    releaseInstall();await page.locator('#install-command-choice').waitFor();await ready(page);
    assert.equal(installPosts,1,'guard retried the one-time installation request');
    await page.unroute('**/admin/enrollments');await page.locator('#close-detail').click();
    // An active revoke form used to crash initialization on any later page load.
    await page.reload();await ready(page);
    assert.equal(await page.locator('#install form[action$="/revoke"]').count(),1,'active token was not restored after reload');

    await page.evaluate(()=>location.hash='users');
    const details=page.locator('#user-table tr[data-user-id="u1"] .user-profile');await details.locator('summary').click();
    const profile=details.locator('form[action$="/edit"]'),name=profile.locator('[name="name"]'),notes=profile.locator('[name="notes"]');
    await name.fill('Recovered profile draft');await notes.fill('Keep this draft through sign-in');
    await page.route('**/admin/dashboard',route=>route.fulfill({status:503,contentType:'text/plain',body:'Temporary service outage'}));
    await page.locator('#refresh-status').click();
    await page.waitForFunction(()=>!document.querySelector('#stale-banner').hidden && !document.querySelector('#refresh-status').disabled);
    assert.match(await page.locator('#stale-banner').textContent(),/HTTP 503/);
    assert.equal(await page.locator('#stale-banner a[href="/login"]').count(),0,'service outage was reported as an expired login');
    assert.equal(await name.inputValue(),'Recovered profile draft');
    await page.unroute('**/admin/dashboard');await refresh();
    const oldToken=await profile.locator('[name="csrf"]').inputValue();
    await context.clearCookies();await profile.locator('button').click();
    await profile.locator('.form-error a[href="/login"]').waitFor();
    await page.waitForFunction(()=>!document.querySelector('form[aria-busy]') && !document.querySelector('#refresh-status').disabled);
    assert.equal(await name.inputValue(),'Recovered profile draft');
    assert.equal(await notes.inputValue(),'Keep this draft through sign-in');
    assert.equal(await profile.locator('.form-error a').getAttribute('target'),'_blank');
    assert.equal(await page.locator('#stale-banner a[href="/login"]').count(),1,'expired refresh has no recovery action');
    const popupPromise=page.waitForEvent('popup');await profile.locator('.form-error a').click();
    const popup=await popupPromise;await signIn(popup);await popup.close();
    await refresh();
    assert.ok((await profile.locator('[name="csrf"]').inputValue())!==oldToken,'refresh retained the previous session token');
    assert.equal(await profile.locator('.form-error').count(),0,'recovered form still reports an expired session');
    assert.match(await page.locator('#notice').textContent(),/登录已恢复/);
    assert.equal(await name.inputValue(),'Recovered profile draft');
    assert.equal(await notes.inputValue(),'Keep this draft through sign-in');
    await profile.locator('button').click();await ready(page);
    assert.equal(await page.locator('#user-table tr[data-user-id="u1"] strong').first().textContent(),'Recovered profile draft');

    // A new login may occur before the original tab has refreshed its token.
    const staleFormToken=await profile.locator('[name="csrf"]').inputValue();
    await context.clearCookies();
    const loginAgain=await context.newPage();await loginAgain.goto(base+'/login');await signIn(loginAgain);await loginAgain.close();
    // Keep this form stale even if the periodic refresh runs during a slow CI login.
    await profile.locator('[name="csrf"]').evaluate((input,token)=>input.value=token,staleFormToken);
    await name.fill('Retry after session rotation');
    let savePosts=0;const countSave=request=>{if(request.method()==='POST'&&request.url().endsWith('/admin/users/u1/edit'))savePosts++;};
    page.on('request',countSave);
    const rejected=page.waitForResponse(response=>response.url().endsWith('/admin/users/u1/edit'));
    await profile.locator('button').click();assert.equal((await rejected).status(),403);await ready(page);
    assert.match(await profile.locator('.form-error').textContent(),/登录状态已更新/);
    assert.equal(await name.inputValue(),'Retry after session rotation');
    assert.equal(savePosts,1,'CSRF recovery automatically replayed a mutation');
    await profile.locator('button').click();await ready(page);
    assert.equal(savePosts,2);
    assert.equal(await page.locator('#user-table tr[data-user-id="u1"] strong').first().textContent(),'Retry after session rotation');
    page.off('request',countSave);

    // Old action HTML and headers must not replace a token already renewed by refresh.
    let releaseSave,captureSave;
    const saveGate=new Promise(resolve=>releaseSave=resolve),saveStarted=new Promise(resolve=>captureSave=resolve);
    await page.route('**/admin/users/u1/edit',async route=>{
      const response=await route.fetch();captureSave();await saveGate;await route.fulfill({response});
    });
    await name.fill('Delayed pre-login save');await profile.locator('button').click();await saveStarted;
    const preLoginToken=await profile.locator('[name="csrf"]').inputValue();
    await context.clearCookies();
    const loginDuringSave=await context.newPage();await loginDuringSave.goto(base+'/login');await signIn(loginDuringSave);await loginDuringSave.close();
    await page.locator('#refresh-status').click();
    await page.waitForFunction(old=>document.querySelector('#user-table tr[data-user-id="u1"] form[action$="/edit"] [name="csrf"]').value!==old,preLoginToken);
    await page.waitForFunction(()=>!document.querySelector('#refresh-status').disabled);
    const renewedToken=await profile.locator('[name="csrf"]').inputValue();
    releaseSave();await ready(page);await page.unroute('**/admin/users/u1/edit');
    assert.ok((await profile.locator('[name="csrf"]').inputValue())===renewedToken,'delayed response restored the old session token');
    assert.ok(await page.locator('input[name="csrf"]').evaluateAll((inputs,token)=>inputs.every(input=>input.value===token),renewedToken),'imported forms retained an old session token');
    await name.fill('Save after delayed response');await profile.locator('button').click();await ready(page);
    assert.equal(await page.locator('#user-table tr[data-user-id="u1"] strong').first().textContent(),'Save after delayed response');
    assert.deepEqual(errors,[]);
    console.log('Recovery browser checks passed: active-token reload, pending-request navigation, expired login, preserved drafts, CSRF renewal and stale responses.');
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
