// Run after upgrade-recovery.browser.cjs against its isolated local fixture.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');

(async()=>{
  const browser=await chromium.launch({headless:true,
    executablePath:process.env.XMESH_BROWSER_EXECUTABLE||undefined,
    channel:process.env.XMESH_BROWSER_CHANNEL||undefined});
  try {
    const page=await browser.newPage({locale:'zh-CN',viewport:{width:1440,height:1100}}),errors=[];
    page.on('pageerror',error=>errors.push(error.message));
    const base=process.env.XMESH_UPGRADE_BROWSER_URL||'http://127.0.0.1:18089';
    const ready=()=>page.waitForFunction(()=>!document.querySelector('#refresh-status').disabled &&
      !document.querySelector('form[aria-busy]') && document.querySelector('#refresh-note').textContent.startsWith('已更新'));
    const refresh=async()=>{await page.locator('#refresh-status').click();await ready();};
    await page.goto(base+'/login');await page.locator('[name=username]').fill('admin');
    await page.locator('[name=password]').fill('a-strong-test-password');await page.locator('form button').click();await ready();
    await page.evaluate(()=>location.hash='node-upgrades');
    const csrf=await page.locator('input[name=csrf]').first().inputValue();
    const post=async(path,values)=>{
      const response=await page.request.post(base+path,{form:{csrf,...values},maxRedirects:0});
      assert.equal(response.status(),303,await response.text());
    };
    const dashboard=async()=>await(await page.request.get(base+'/admin/dashboard')).json();
    const first=page.locator('#upgrade-task-table tr[data-upgrade-task-id="first"]');
    assert.equal(await first.locator('td').first().locator('strong').textContent(),'Gateway Gateway');
    assert.equal(await first.locator('td').first().locator('code').textContent(),'g');
    await first.locator('summary').click();
    await refresh();assert.equal(await first.locator('details').getAttribute('open'),'','technical details collapsed during refresh');
    const tasksSearch=page.getByRole('searchbox',{name:'升级记录 搜索',exact:true});
    const nodesSearch=page.getByRole('searchbox',{name:'助手与迁移 搜索',exact:true});
    await tasksSearch.fill('  no matching maintenance task  ');
    assert.equal(await page.locator('#upgrade-task-table tbody tr:visible').count(),0);
    await refresh();assert.equal(await page.locator('#upgrade-task-table tbody tr:visible').count(),0,'refresh discarded task filter');
    await tasksSearch.fill('Gateway');
    assert.equal(await page.locator('#upgrade-task-table tbody tr:visible').count(),1);
    await refresh();assert.equal(await page.locator('#upgrade-task-table tbody tr:visible').count(),1);

    if (!await page.locator('#node-upgrades details.create-details').evaluate(details=>details.open))
      await page.locator('#node-upgrades details.create-details > summary').click();
    const batch=page.locator('#node-upgrades form[action="/admin/upgrades"]');
    const version=batch.locator('[name=version]'),gateway=batch.locator('[name=node_id][value=g]');
    await version.fill('v99.88.77');await gateway.check();
    const original=await gateway.elementHandle();
    const name='Maintenance <agent> {1}';
    await nodesSearch.fill(name);
    assert.equal(await page.locator('#updater-table tbody tr:visible').count(),0);
    await post('/admin/agents',{name});
    const created=(await dashboard()).nodes.find(node=>node.name===name);
    assert.ok(created);
    await refresh();
    const choice=batch.locator(`[name=node_id][value="${created.id}"]`);
    assert.equal(await choice.count(),1,'new Agent missing from upgrade choices');
    const row=page.locator(`#updater-table tr[data-node-id="${created.id}"]`);
    assert.equal(await row.isVisible(),true,'matching new node omitted by the active filter');
    assert.equal(await row.locator('strong').textContent(),'Agent '+name,'node name was interpreted as markup');
    assert.ok(await row.locator('form').getAttribute('action').then(action=>action.endsWith('/'+created.id+'/pair')));
    assert.equal(await gateway.isChecked(),true);assert.equal(await version.inputValue(),'v99.88.77');
    assert.equal(await gateway.evaluate((input,original)=>input===original,original),true,'refresh replaced the selected input');
    await choice.check();await version.focus();
    await page.locator('#refresh-status').evaluate(button=>button.click());await ready();
    assert.equal(await version.evaluate(input=>input===document.activeElement),true,'refresh stole version input focus');
    assert.equal(await choice.isChecked(),true);

    // Embedded Agents share a Gateway process and must never appear as independent upgrade targets.
    await page.route('**/admin/dashboard',async route=>{
      const response=await route.fetch(),state=await response.json();
      state.nodes.find(node=>node.id===created.id).gateway_id='g';
      await route.fulfill({response,json:state});
    });
    await refresh();assert.equal(await choice.count(),0,'embedded Agent offered an independent upgrade');
    assert.equal(await row.count(),0,'embedded Agent offered independent updater pairing');
    await page.unroute('**/admin/dashboard');await refresh();
    await choice.check();
    await post('/admin/agents/'+created.id+'/delete',{confirm_name:name});await refresh();
    assert.equal(await choice.count(),0,'deleted node still selected for upgrade');assert.equal(await row.count(),0);
    assert.deepEqual(await batch.evaluate(form=>new FormData(form).getAll('node_id')),['g'],'deleted node leaked into the next batch');
    assert.equal(await version.inputValue(),'v99.88.77');

    // Impact previews must follow route grants without a page reload.
    const before=(await dashboard()).nodes.find(node=>node.id==='g').impact.users;
    await post('/admin/users',{name:'Maintenance affected user'});
    const user=(await dashboard()).users.find(user=>user.name==='Maintenance affected user');
    await post('/admin/grants',{user_id:user.id,attachment_id:'r'});await refresh();
    assert.ok((await gateway.locator('..').textContent()).includes(`${before+1} 位用户`),'affected user count stayed stale');
    assert.equal(await gateway.isChecked(),true);assert.equal(await version.inputValue(),'v99.88.77');
    const grant=(await dashboard()).grants.find(grant=>grant.user_id===user.id);
    await post('/admin/grants/'+grant.id+'/delete',{confirm_id:grant.id});
    await refresh();assert.ok((await gateway.locator('..').textContent()).includes(`${before} 位用户`));
    assert.deepEqual(errors,[]);
    console.log('Maintenance browser checks passed');
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
