// Run against a fresh TestPanelBrowserFixture; all changes stay in temporary state.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');

(async()=>{
  const browser=await chromium.launch({headless:true,
    executablePath:process.env.XMESH_BROWSER_EXECUTABLE||undefined,
    channel:process.env.XMESH_BROWSER_CHANNEL||undefined});
  try {
    const page=await browser.newPage({locale:'zh-CN',viewport:{width:1440,height:1000}}),errors=[];
    page.on('pageerror',error=>errors.push(error.message));
    const base=process.env.XMESH_PANEL_TEST_URL||'http://127.0.0.1:18097';
    const ready=()=>page.waitForFunction(()=>!document.querySelector('#refresh-status').disabled &&
      !document.querySelector('form[aria-busy]') && document.querySelector('#refresh-note').textContent.startsWith('已更新'));
    const refresh=async()=>{await page.locator('#refresh-status').evaluate(button=>button.click());await ready();};
    const go=async id=>{await page.evaluate(id=>location.hash=id,id);await page.locator('#'+id).waitFor({state:'visible'});};
    const dashboard=async()=>await(await page.request.get(base+'/admin/dashboard')).json();
    await page.goto(base+'/login');await page.locator('[name=username]').fill('admin');
    await page.locator('[name=password]').fill('a-strong-test-password');await page.locator('form button').click();await ready();
    const csrf=await page.locator('input[name=csrf]').first().inputValue();
    const post=async(path,values)=>{
      const response=await page.request.post(base+path,{form:{csrf,...values},maxRedirects:0});
      assert.equal(response.status(),303,await response.text());
    };
    await go('wizard');
    const picker=page.locator('#deployment-progress > select');
    await picker.selectOption('a1');await picker.focus();const original=await picker.elementHandle();
    await refresh();
    assert.equal(await picker.inputValue(),'a1');
    assert.equal(await picker.evaluate((input,original)=>input===original,original),true,'refresh replaced the deployment selector');
    assert.equal(await picker.evaluate(input=>input===document.activeElement),true,'refresh stole deployment selector focus');
    assert.equal(await page.locator('#deployment-progress .progress-card:visible').getAttribute('data-node-id'),'a1');
    const nodeName='Runtime <Agent> {1}';await post('/admin/agents',{name:nodeName});await refresh();
    const created=(await dashboard()).nodes.find(node=>node.name===nodeName);
    await picker.selectOption(created.id);
    assert.equal(await page.locator('#deployment-progress .progress-card:visible').getAttribute('data-node-id'),created.id);

    await go('network-map');
    await page.getByRole('button',{name:'Agent '+nodeName,exact:true}).click();
    assert.match(await page.locator('.node-runtime').textContent(),/离线/);
    await post('/admin/agents/'+created.id+'/toggle',{enabled:'false'});await refresh();
    assert.match(await page.locator('.node-runtime').textContent(),/停用/,'open node details did not follow remote state changes');
    await post('/admin/agents/'+created.id+'/delete',{confirm_name:nodeName});await refresh();
    assert.match(await page.locator('.node-runtime').textContent(),/节点已删除/);
    await page.locator('#close-detail').click();await go('wizard');
    assert.notEqual(await picker.inputValue(),created.id);
    assert.notEqual(await picker.inputValue(),'','deleted tracked node left every progress card hidden');
    assert.equal(await page.locator('#deployment-progress .progress-card:visible').count(),1);

    await go('routes');
    await page.locator('#route-overview .route-card').filter({has:page.locator('[data-grant-route=r1]')}).getByRole('button',{name:'查看状态'}).click();
    assert.equal(await page.locator('.route-runtime .badge').textContent(),'可用');
    const rename=page.locator('#detail-body form[action="/admin/attachments/r1/edit"]'),name=rename.locator('[name=name]');
    const savedName=await name.inputValue();await name.fill('仍在编辑的线路名称');await name.focus();
    const nameHandle=await name.elementHandle();
    await post('/admin/gateways/g1/ingress',{ingress_controls:'1'});await refresh();
    const current=(await dashboard()).routes.find(route=>route.id==='r1');
    assert.equal(current.state,'disabled');assert.equal(current.issue.state,'disabled');
    assert.equal(await page.locator('.route-runtime .badge').textContent(),'停用');
    assert.match(await page.locator('.route-runtime').textContent(),/接入协议均已关闭/);
    assert.equal(await name.inputValue(),'仍在编辑的线路名称');
    assert.equal(await name.evaluate((input,original)=>input===original,nameHandle),true);
    assert.equal(await name.evaluate(input=>input===document.activeElement),true,'status refresh interrupted a name draft');
    assert.equal(await page.locator('.route-runtime a').getAttribute('href'),'#gateways');
    await post('/admin/attachments/r1/toggle',{enabled:'false'});await refresh();
    assert.match(await page.locator('.route-runtime').textContent(),/线路已停用/);
    assert.equal(await page.locator('.route-runtime a').getAttribute('href'),'#assignments');
    // A clean name follows another window, while a local edit remains intact.
    await name.fill(savedName);await post('/admin/attachments/r1/edit',{name:'另一窗口的新线路名称'});await refresh();
    assert.equal(await name.inputValue(),'另一窗口的新线路名称');
    assert.match(await page.locator('#detail-title').textContent(),/另一窗口的新线路名称/);
    await name.fill('删除后仍需保留的草稿');
    await post('/admin/attachments/r1/delete',{confirm_id:'r1'});await refresh();
    assert.match(await page.locator('.route-runtime').textContent(),/线路已删除/);
    assert.equal(await name.inputValue(),'删除后仍需保留的草稿');
    assert.equal(await rename.locator('button').isDisabled(),true,'deleted route retained a working save action');
    page.once('dialog',async dialog=>{assert.equal(dialog.type(),'confirm');await dialog.accept();});
    await page.locator('#close-detail').click();
    assert.deepEqual(errors,[]);
    console.log('Runtime browser checks passed: consistent route availability, live node and route details, deleted objects, preserved drafts and focused deployment selection.');
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
