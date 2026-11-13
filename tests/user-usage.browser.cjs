// Uses temporary TestPanelBrowserFixture state, also after workspace/recovery/install.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');

(async()=>{
  const browser=await chromium.launch({headless:true,
    executablePath:process.env.XMESH_BROWSER_EXECUTABLE||undefined,
    channel:process.env.XMESH_BROWSER_CHANNEL||undefined});
  try {
    const context=await browser.newContext({locale:'zh-CN',viewport:{width:1440,height:1000}});
    const page=await context.newPage(),errors=[];
    page.on('pageerror',error=>errors.push(error.message));
    const base=process.env.XMESH_PANEL_TEST_URL||'http://127.0.0.1:18096';
    const ready=()=>page.waitForFunction(()=>!document.querySelector('#refresh-status').disabled &&
      !document.querySelector('form[aria-busy]') && document.querySelector('#refresh-note').textContent.startsWith('已更新'));
    // A background refresh should not itself move keyboard focus to the refresh button.
    const refresh=async()=>{await page.locator('#refresh-status').evaluate(button=>button.click());await ready();};
    const go=async id=>{
      await page.evaluate(id=>location.hash=id,id);
      await page.locator('#'+id).waitFor({state:'visible'});
    };
    await page.goto(base+'/login');await page.locator('[name=username]').fill('admin');
    await page.locator('[name=password]').fill('a-strong-test-password');await page.locator('form button').click();await ready();
    const post=async(path,values)=>{
      const csrf=await page.locator('input[name=csrf]').first().inputValue();
      const response=await page.request.post(base+path,{form:{csrf,...values},maxRedirects:0});
      assert.equal(response.status(),303,await response.text());
    };

    await go('users');
    const user=page.locator('#user-table tr[data-user-id=u1]'),technical=user.locator('td').first().locator('details');
    const profile=user.locator('.user-profile'),name=profile.locator('[name=name]'),notes=profile.locator('[name=notes]');
    await technical.locator('summary').click();await profile.locator('summary').click();
    await name.focus();await name.evaluate(input=>input.setSelectionRange(2,2));
    const originalName=await name.elementHandle();
    await post('/admin/users/u2/edit',{name:'Team <external> {2}',notes:'Updated in another window'});await refresh();
    assert.equal(await technical.evaluate(details=>details.open),true,'refresh collapsed technical details');
    assert.equal(await name.evaluate((input,original)=>input===original,originalName),true,'refresh replaced the profile input');
    assert.equal(await name.evaluate(input=>input===document.activeElement),true,'refresh lost profile focus');
    assert.equal(await name.evaluate(input=>input.selectionStart),2,'unrelated refresh moved the text cursor');

    await post('/admin/users/u1/edit',{name:'Personal <remote> {1}',notes:'Remote notes <safe>'});await refresh();
    assert.equal(await name.inputValue(),'Personal <remote> {1}');
    assert.equal(await name.evaluate(input=>input.defaultValue),'Personal <remote> {1}');
    assert.equal(await notes.inputValue(),'Remote notes <safe>');
    assert.equal(await user.locator('td').first().locator(':scope > small').textContent(),'Remote notes <safe>');
    assert.equal(await technical.evaluate(details=>details.open),true);
    assert.equal(await name.evaluate(input=>input===document.activeElement),true);

    await name.fill('Local draft');await notes.fill('Local notes');
    await post('/admin/users/u1/edit',{name:'Another remote name',notes:''});await refresh();
    assert.equal(await name.inputValue(),'Local draft','remote refresh discarded a local draft');
    assert.equal(await notes.inputValue(),'Local notes');
    assert.equal(await user.locator('td').first().locator('strong').textContent(),'Another remote name');
    assert.equal(await user.locator('td').first().locator(':scope > small').count(),0,'removed notes remained visible');
    await post('/admin/users/u1/edit',{name:'Newest remote name',notes:'Newest remote notes'});await refresh();
    assert.equal(await name.inputValue(),'Local draft');assert.equal(await notes.inputValue(),'Local notes');
    await name.fill('Personal <remote> {1}');await notes.fill('Remote notes <safe>');await refresh();
    assert.equal(await name.inputValue(),'Newest remote name','reverting a draft did not resume the latest profile update');
    assert.equal(await notes.inputValue(),'Newest remote notes');
    await name.fill('Local draft');await notes.fill('Local notes');
    await profile.getByRole('button',{name:'保存用户资料',exact:true}).click();await ready();
    assert.equal(await name.inputValue(),'Local draft');

    await page.locator('#workspace-user').selectOption('u1');await go('usage');
    const usage=page.locator('#usage-table > table > tbody > tr[data-user-id=u1]');
    const detail=usage.locator(':scope > td').last().locator(':scope > details'),summary=detail.locator(':scope > summary');
    await summary.click();await summary.focus();const originalSummary=await summary.elementHandle();
    await refresh();
    const checkDetail=async()=>{
      assert.equal(await detail.evaluate(details=>details.open),true,'refresh collapsed usage details');
      assert.equal(await summary.evaluate((element,original)=>element===original,originalSummary),true,'refresh replaced usage details');
      assert.equal(await summary.evaluate(element=>element===document.activeElement),true,'refresh lost usage keyboard focus');
    };
    await checkDetail();

    // HTTP-level counter ingestion is tested in usage_test.go. Here exercise live DOM reconciliation.
    let empty=false;
    await page.route('**/admin/dashboard',async route=>{
      const response=await route.fetch(),state=await response.json(),entry=state.usage.find(item=>item.user_id==='u1');
      entry.windows['5h']={upload:2048,download:4096};
      entry.links=empty?[]:[{id:'test-usage-link',name:'Updated <Link> {1}',windows:{'5h':{upload:2048,download:4096}}}];
      await route.fulfill({response,json:state});
    });
    await refresh();await checkDetail();
    assert.equal(await usage.locator(':scope > td').first().textContent(),'↑ 2.0 KiB · ↓ 4.0 KiB');
    const link=detail.locator('tr[data-usage-link-id=test-usage-link]');
    assert.equal(await link.locator('th').textContent(),'Updated <Link> {1}');
    assert.equal(await link.locator('td').first().textContent(),'↑ 2.0 KiB · ↓ 4.0 KiB');
    const originalLink=await link.elementHandle();await refresh();await checkDetail();
    assert.equal(await link.evaluate((row,original)=>row===original,originalLink),true,'unchanged usage link was replaced');
    empty=true;await refresh();await checkDetail();
    assert.equal(await detail.locator('table').count(),0,'removed usage links remained visible');
    assert.equal(await detail.locator('.empty').textContent(),'尚未分配 Link。');
    empty=false;await refresh();await checkDetail();assert.equal(await link.count(),1);
    await page.setViewportSize({width:390,height:844});await refresh();await checkDetail();
    assert.equal(await usage.locator(':scope > td').first().getAttribute('data-label'),'近 5 小时');
    assert.equal(await link.locator('td').first().getAttribute('data-label'),'近 5 小时');
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'mobile usage details overflow horizontally');
    await page.unroute('**/admin/dashboard');await refresh();await checkDetail();
    assert.deepEqual(errors,[]);
    await context.close();
    console.log('User profile and usage refresh browser checks passed');
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exit(1);});
