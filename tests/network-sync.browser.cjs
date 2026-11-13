// Run on a fresh TestPanelBrowserFixture. Only temporary fixture data is changed.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
(async()=>{
  const browser=await chromium.launch({headless:true,executablePath:process.env.XMESH_BROWSER_EXECUTABLE||undefined,
    channel:process.env.XMESH_BROWSER_CHANNEL||undefined});
  try {
    const context=await browser.newContext({locale:'zh-CN',viewport:{width:1440,height:1000}}),page=await context.newPage(),errors=[];
    page.on('pageerror',error=>errors.push(error.message));
    const base=process.env.XMESH_PANEL_TEST_URL||'http://127.0.0.1:18098';
    const ready=()=>page.waitForFunction(()=>!document.querySelector('#refresh-status').disabled &&
      !document.querySelector('form[aria-busy]') && document.querySelector('#refresh-note').textContent.startsWith('已更新'));
    const refresh=async()=>{await page.locator('#refresh-status').evaluate(button=>button.click());await ready();};
    const go=async id=>{await page.evaluate(id=>location.hash=id,id);await page.locator('#'+id).waitFor({state:'visible'});};
    const dashboard=async()=>await(await page.request.get(base+'/admin/dashboard')).json();
    await page.goto(base+'/login');await page.locator('[name=username]').fill('admin');
    await page.locator('[name=password]').fill('a-strong-test-password');await page.locator('form button').click();await ready();
    const post=async(path,values)=>{
      const csrf=await page.locator('input[name=csrf]').first().inputValue();
      const response=await page.request.post(base+path,{form:{csrf,...values},maxRedirects:0});
      assert.equal(response.status(),303,await response.text());
    };
    const row=(section,path)=>page.locator(`#${section} > table`).first().locator('tbody > tr').filter({has:page.locator(`form[action="${path}"]`)});
    await go('assignments');
    const createPair=page.locator('#assignments form[action="/admin/attachments"]');
    await createPair.locator('[name=gateway_id]').selectOption('g2');
    await createPair.locator('[name=agent_id]').selectOption('a2');
    await go('agents');
    const a1=row('agents','/admin/agents/a1/edit'),edit=a1.locator('form[action$="/edit"]');
    await a1.locator('summary').click();await edit.locator('[name=allowed_cidrs]').fill('10.0.0.0/8');
    const originalInput=await edit.locator('[name=allowed_cidrs]').elementHandle();
    await post('/admin/agents',{name:'New <Agent> {3}',allowed_cidrs:'0.0.0.0/0'});
    const created=(await dashboard()).nodes.find(node=>node.name==='New <Agent> {3}');await refresh();
    const newRow=row('agents',`/admin/agents/${created.id}/edit`);
    assert.equal(await newRow.count(),1,'new Agent missing from management list after refresh');
    assert.equal(await createPair.locator(`[name=agent_id] option[value="${created.id}"]`).count(),1,'a local creation draft hid newly available Agent options');
    assert.equal(await createPair.locator('[name=agent_id]').inputValue(),'a2','refresh changed the draft owner');
    assert.equal(await createPair.locator('[name=gateway_id]').inputValue(),'g2','refresh changed the draft Gateway');
    await go('assignments');await createPair.locator('[name=gateway_id]').selectOption('g1');await go('agents');
    assert.equal(await edit.locator('[name=allowed_cidrs]').inputValue(),'10.0.0.0/8','refresh discarded an existing policy draft');
    assert.equal(await edit.locator('[name=allowed_cidrs]').evaluate((input,original)=>input===original,originalInput),true);
    assert.equal(await a1.locator('details').evaluate(details=>details.open),true);
    await edit.locator('[name=allowed_cidrs]').focus();await refresh();
    assert.equal(await edit.locator('[name=allowed_cidrs]').evaluate(input=>input===document.activeElement),true);

    // Gateway settings and external-exit management share the same refresh path.
    await post('/admin/gateways',{name:'New Gateway',public_host:'new.example',vmess_port:'8081',vmess_path:'/gateway',hy2_port:'0',region:'overseas'});await refresh();
    const gateway=(await dashboard()).nodes.find(node=>node.name==='New Gateway'),gatewayRow=row('gateways',`/admin/gateways/${gateway.id}/edit`);
    assert.equal(await gatewayRow.count(),1);
    await post(`/admin/gateways/${gateway.id}/edit`,{name:'Renamed Gateway',public_host:'renamed.example',vmess_port:'8181',vmess_path:'/renamed',vmess_host:'vh.example',hy2_port:'0',region:'cn'});await refresh();
    assert.match(await gatewayRow.locator('td').first().textContent(),/Renamed Gateway/);
    assert.equal(await gatewayRow.locator('form[action$="/edit"] [name=vmess_port]').inputValue(),'8181');
    assert.equal(await gatewayRow.locator('[name=vmess_host]').inputValue(),'vh.example');
    await post(`/admin/gateways/${gateway.id}/delete`,{confirm_name:'Renamed Gateway'});await refresh();
    assert.equal(await gatewayRow.count(),0);
    const source='vmess://'+Buffer.from(JSON.stringify({v:'2',ps:'Remote',add:'exit.example',port:'443',id:'11111111-1111-4111-8111-111111111111',aid:'0',net:'ws',host:'cdn.example',path:'/relay',tls:'tls'})).toString('base64');
    await post('/admin/upstreams',{name:'New upstream',source});await refresh();
    const upstream=(await dashboard()).nodes.find(node=>node.name==='New upstream');
    const upstreamBlock=page.locator('#upstreams > details').filter({has:page.locator(`form[action="/admin/upstreams/${upstream.id}/toggle"]`)});
    assert.equal(await upstreamBlock.count(),1);await go('upstreams');await upstreamBlock.locator('summary').click();
    await post(`/admin/upstreams/${upstream.id}/edit`,{name:'Renamed upstream',source:''});await refresh();
    assert.match(await upstreamBlock.locator('summary').textContent(),/Renamed upstream/);
    assert.equal(await upstreamBlock.evaluate(details=>details.open),true);
    assert.equal(await page.locator(`form[action="/admin/upstreams/${upstream.id}/edit"] [name=name]`).inputValue(),'Renamed upstream');
    await post(`/admin/upstreams/${upstream.id}/delete`,{confirm_name:'Renamed upstream'});await refresh();
    assert.equal(await upstreamBlock.count(),0);
    assert.equal(await page.locator(`form[action="/admin/upstreams/${upstream.id}/edit"]`).count(),0);
    await go('agents');

    await post(`/admin/agents/${created.id}/edit`,{name:'Renamed Agent',allowed_cidrs:'192.168.0.0/16',denied_cidrs:'192.168.1.0/24'});await refresh();
    assert.match(await newRow.locator('td').first().textContent(),/Renamed Agent/);
    assert.equal(await newRow.locator('[name=allowed_cidrs]').inputValue(),'192.168.0.0/16');
    await newRow.locator('summary').click();await newRow.locator('[name=name]').focus();
    const cleanInput=await newRow.locator('[name=name]').elementHandle();
    await post(`/admin/agents/${created.id}/edit`,{name:'Second Agent',allowed_cidrs:'172.16.0.0/12'});await refresh();
    assert.equal(await newRow.locator('[name=name]').inputValue(),'Second Agent');
    assert.equal(await newRow.locator('[name=name]').evaluate((input,original)=>input===original&&input===document.activeElement,cleanInput),true);
    assert.equal(await newRow.locator('details').evaluate(details=>details.open),true);
    await newRow.locator('[name=name]').fill('Saved Agent');
    await newRow.locator('form[action$="/edit"] button').click();await ready();
    assert.equal(await page.locator('#agents > table').count(),1,'saving duplicated the management table');
    assert.equal(await newRow.locator('details').evaluate(details=>details.open),true,'saving closed the current profile');
    assert.equal(await newRow.locator('[name=name]').evaluate(input=>input.defaultValue),'Saved Agent','saving did not advance the input baseline');
    await newRow.locator('[name=name]').fill('Second Agent');await refresh();
    assert.equal(await newRow.locator('[name=name]').inputValue(),'Second Agent','editing back to the original value was mistaken for a clean form');
    await newRow.locator('form[action$="/edit"] button').click();await ready();

    await post('/admin/agents/a1/edit',{name:'Home <renamed>',allowed_cidrs:'172.16.0.0/12'});await refresh();
    assert.match(await a1.locator('td').first().textContent(),/Home <renamed>/);
    assert.equal(await edit.locator('[name=allowed_cidrs]').inputValue(),'10.0.0.0/8');
    await post('/admin/agents/a1/edit',{name:'Home <renamed>',allowed_cidrs:'192.168.0.0/16'});await refresh();
    assert.equal(await edit.locator('[name=allowed_cidrs]').inputValue(),'10.0.0.0/8');
    await edit.locator('[name=allowed_cidrs]').fill('0.0.0.0/0');await refresh();
    assert.equal(await edit.locator('[name=allowed_cidrs]').inputValue(),'192.168.0.0/16','reverting a draft left an outdated Agent policy');
    await edit.locator('[name=allowed_cidrs]').fill('10.0.0.0/8');
    await go('assignments');
    assert.match(await row('assignments','/admin/attachments/r1/toggle').textContent(),/Home <renamed>/);
    await go('links');
    const l1=row('links','/admin/links/l1/edit');
    assert.ok(Array.isArray((await dashboard()).link_reports),'dashboard omits live per-node Link reports');
    const reportTable=page.locator('#links > table').nth(1);
    const report=reportTable.locator('tr[data-report-key]').filter({hasText:'Hong Kong Edge'}).filter({hasText:'Route 1'});
    assert.equal(await report.count(),1,'node reports do not display configured Link and node names');
    await report.locator('details').first().locator('summary').click();
    await report.locator('details').first().locator('summary').focus();
    const reportAt=new Date(Date.now()+123000).toISOString();
    await page.route('**/admin/dashboard',async route=>{
      const response=await route.fetch(),state=await response.json();
      Object.assign(state.link_reports.find(item=>item.reporter_node_id==='g1'&&item.link_id==='l1'),{
        connections:9,active_streams:7,last_seen:reportAt,last_error:'diagnostic update'});
      await route.fulfill({response,json:state});
    });
    await refresh();
    assert.match(await report.locator('td').nth(2).textContent(),/9.*7/);
    assert.equal(await report.locator('time').getAttribute('datetime'),reportAt);
    assert.match(await report.textContent(),/diagnostic update/);
    assert.equal(await report.locator('details').first().evaluate(details=>details.open),true);
    assert.equal(await report.locator('summary').first().evaluate(summary=>summary===document.activeElement),true);
    await page.unroute('**/admin/dashboard');await refresh();
    await post('/admin/links/l1/policy',{priority:'3',weight:'4',connections:'5',max_streams:'80'});await refresh();
    assert.equal(await l1.locator('[name=weight]').inputValue(),'4');
    await post('/admin/assign-gateways',{agent_id:created.id,gateway_id:'g1'});await refresh();
    const connection=(await dashboard()).routes.find(route=>route.agent_id===created.id);
    const link=(await dashboard()).links.find(link=>link.route_id===connection.id);
    assert.equal(await row('links',`/admin/links/${link.id}/toggle`).count(),1);
    await go('assignments');assert.equal(await row('assignments',`/admin/attachments/${connection.id}/toggle`).count(),1);
    await post(`/admin/agents/${created.id}/delete`,{confirm_name:'Second Agent'});await refresh();
    assert.equal(await row('agents',`/admin/agents/${created.id}/edit`).count(),0);
    assert.equal(await row('assignments',`/admin/attachments/${connection.id}/toggle`).count(),0);
    assert.equal(await row('links',`/admin/links/${link.id}/toggle`).count(),0);

    await go('agents');
    // A delayed response may contain an older page than the snapshot received
    // by polling while this form was submitting. Apply the latest snapshot
    // when it becomes clean again, without requiring another remote mutation.
    const idle=row('agents','/admin/agents/a2/edit'),idleEdit=idle.locator('form[action$="/edit"]');
    await idle.locator('summary').click();
    let releasePost,postStarted;
    const gate=new Promise(resolve=>{releasePost=resolve;}),started=new Promise(resolve=>{postStarted=resolve;});
    await page.route('**/admin/agents/a2/edit',async route=>{
      const response=await route.fetch();postStarted();await gate;await route.fulfill({response});
    });
    await idleEdit.locator('button').click();await started;
    try {
      await post('/admin/agents/a2/edit',{name:'Office latest',allowed_cidrs:'10.20.0.0/16'});
      await page.locator('#refresh-status').evaluate(button=>button.click());
      await page.waitForFunction(()=>!document.querySelector('#refresh-status').disabled && document.querySelector('#refresh-note').textContent.startsWith('已更新'));
      assert.equal(await idleEdit.locator('[name=allowed_cidrs]').inputValue(),'0.0.0.0/0','polling replaced a submitting form');
    } finally {releasePost();}
    await ready();await page.unroute('**/admin/agents/a2/edit');
    assert.equal(await idleEdit.locator('[name=name]').inputValue(),'Office latest','delayed save hid a newer remote name');
    assert.equal(await idleEdit.locator('[name=allowed_cidrs]').inputValue(),'10.20.0.0/16','delayed save left a clean form on an old snapshot');

    // Creating the next Agent has a blank/default baseline, even when its name
    // was typed before the previous creation response arrived.
    const createAgent=page.locator('#agents form[action="/admin/agents"]');
    await createAgent.evaluate(form=>{form.closest('details').open=true;});
    await createAgent.locator('details').evaluateAll(items=>items.forEach(details=>{details.open=true;}));
    const createName=createAgent.locator('[name=name]'),createPolicy=createAgent.locator('[name=allowed_cidrs]');
    const defaultPolicy=await createPolicy.inputValue();
    let releaseCreate,createStarted;
    const createGate=new Promise(resolve=>{releaseCreate=resolve;}),creating=new Promise(resolve=>{createStarted=resolve;});
    await page.route('**/admin/agents',async route=>{
      const response=await route.fetch();createStarted();await createGate;await route.fulfill({response});
    });
    await createName.fill('Created Agent');await createPolicy.fill('10.0.0.0/8');
    await createAgent.locator('button').click();
    try {await creating;await createName.fill('Next Agent');} finally {releaseCreate();}
    await ready();await page.unroute('**/admin/agents');
    assert.equal(await createName.inputValue(),'Next Agent','creating an Agent erased the next name draft');
    assert.equal(await createName.evaluate(input=>input.defaultValue),'','next Agent name retained the saved baseline');
    assert.equal(await createPolicy.inputValue(),defaultPolicy,'creating an Agent carried the saved policy into the next draft');
    await createName.fill('');
    assert.equal(await createAgent.getAttribute('data-dirty'),null,'clearing the next Agent draft still counted as unsaved');
    const successMessage=await page.locator('#notice').textContent();
    await post('/admin/agents/a2/edit',{name:'Office final',allowed_cidrs:'10.20.0.0/16'});await refresh();
    assert.equal(await page.locator('#agents > .form-success').count(),1,'background synchronization removed the operation result');
    assert.equal(await page.locator('#agents > .form-success').textContent(),successMessage);

    // Checkbox and select defaults must also reset without overwriting changes
    // made for the next Gateway while the first request is pending.
    await go('gateways');
    const createGateway=page.locator('#gateways form[action="/admin/gateways"]');
    await createGateway.evaluate(form=>{form.closest('details').open=true;});
    await createGateway.locator('details').evaluateAll(items=>items.forEach(details=>{details.open=true;}));
    let releaseGateway,gatewayStarted;
    const gatewayGate=new Promise(resolve=>{releaseGateway=resolve;}),creatingGateway=new Promise(resolve=>{gatewayStarted=resolve;});
    await page.route('**/admin/gateways',async route=>{
      const response=await route.fetch();gatewayStarted();await gatewayGate;await route.fulfill({response});
    });
    await createGateway.locator('[name=name]').fill('Created Gateway');
    await createGateway.locator('[name=vmess_port]').fill('8182');
    await createGateway.locator('[name=region]').selectOption('overseas');
    await createGateway.locator('[name=hy2_enabled]').uncheck();
    await createGateway.locator('button').click();
    try {
      await creatingGateway;
      await createGateway.locator('[name=vmess_enabled]').uncheck();
      await createGateway.locator('[name=region]').selectOption('cn');
    } finally {releaseGateway();}
    await ready();await page.unroute('**/admin/gateways');
    assert.equal(await createGateway.locator('[name=name]').inputValue(),'');
    assert.equal(await createGateway.locator('[name=vmess_port]').inputValue(),'8080');
    assert.equal(await createGateway.locator('[name=hy2_enabled]').isChecked(),true,'submitted checkbox did not reset to its creation default');
    assert.equal(await createGateway.locator('[name=vmess_enabled]').isChecked(),false,'next Gateway protocol draft was lost');
    assert.equal(await createGateway.locator('[name=region]').inputValue(),'cn','next Gateway region draft was lost');
    await createGateway.locator('[name=vmess_enabled]').check();
    await createGateway.locator('[name=region]').selectOption('');
    assert.equal(await createGateway.getAttribute('data-dirty'),null,'Gateway draft retained submitted checkbox/select defaults');
    await go('agents');

    await post('/admin/agents/a1/delete',{confirm_name:'Home <renamed>'});await refresh();
    assert.equal(await a1.count(),1,'deletion silently discarded an unsaved policy draft');
    assert.equal(await edit.locator('[name=allowed_cidrs]').inputValue(),'10.0.0.0/8');
    assert.equal(await a1.locator('button:not(:disabled)').count(),0,'deleted node retained active save/delete actions');
    assert.match(await a1.textContent(),/已删除/);
    await page.setViewportSize({width:390,height:844});await refresh();
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'retained draft overflows on mobile');
    assert.deepEqual(errors,[]);
    console.log('Network management synchronization browser checks passed');
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exit(1);});
