// Run against a fresh TestPanelBrowserFixture. All changes stay in temporary fixture state.
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');

(async () => {
  const browser = await chromium.launch({headless:true,
    executablePath:process.env.XMESH_BROWSER_EXECUTABLE || undefined,
    channel:process.env.XMESH_BROWSER_CHANNEL || undefined});
  try {
    const context = await browser.newContext({locale:'zh-CN',viewport:{width:1440,height:1000}});
    const page = await context.newPage();
    const errors=[]; page.on('pageerror',error=>errors.push(error.message));
    const base=process.env.XMESH_PANEL_TEST_URL || 'http://127.0.0.1:18096';
    await page.goto(base+'/login');
    await page.locator('[name="username"]').fill('admin');
    await page.locator('[name="password"]').fill('a-strong-test-password');
    await page.locator('form button').click();
    const ready=()=>page.waitForFunction(()=>!document.querySelector('form[aria-busy]') && !document.querySelector('#refresh-status').disabled && document.querySelector('#refresh-note').textContent.startsWith('已更新'));
    await ready();
    const refresh=async()=>{await page.locator('#refresh-status').click();await ready();};
    const csrf=await page.locator('#grant-bulk [name="csrf"]').inputValue();
    const post=async(path,form={})=>{const response=await page.request.post(base+path,{form:{csrf,...form}});assert.equal(response.status(),200,await response.text());};
    const dashboard=()=>page.request.get(base+'/admin/dashboard').then(r=>r.json());
    const go=async(id)=>{await page.evaluate(id=>location.hash=id,id);await page.locator('#'+({network:'routes'}[id]||id)).waitFor({state:'visible'});};

    await go('network');
    assert.equal(await page.locator('section[data-page="network"]:visible').count(),1);
    await page.locator('#routes a[data-grant-route="r1"]').click();
    await page.locator('#subscribe').waitFor({state:'visible'});
    assert.equal(await page.locator('#subscribe input[name="attachment_id"]:checked').inputValue(),'r1');
    assert.equal(await page.locator('.subscription-row:visible').count(),1);
    assert.equal(await page.locator('.subscription-row:visible').getAttribute('data-user-id'),'u1');
    await page.locator('#workspace-user').selectOption('u2');
    assert.equal(await page.locator('#subscribe input:checked').count(),0);
    assert.equal(await page.locator('.subscription-row:visible').getAttribute('data-user-id'),'u2');
    assert.equal(await page.locator('#grant-user').inputValue(),'u2');
    assert.match(await page.locator('#subscription-guide').textContent(),/暂无可导入线路/);
    assert.match(await page.locator('#user-context-audit').getAttribute('href'),/\/u2\/audit$/);
    // Searching with Enter must never submit the selected routes or connections.
    let searchPosts=0;
    const blockSearchSubmit=route=>{searchPosts++;return route.fulfill({status:400,contentType:'text/plain',body:'Unexpected search submission'});};
    for (const path of ['subscribe','assign-gateways','assign-connections']) await page.route(`**/admin/${path}`,blockSearchSubmit);
    await page.locator('#subscribe [name="attachment_id"][value="r1"]').check();
    await page.locator('#subscribe .selection-search').fill('Route');
    await page.locator('#subscribe .selection-search').press('Enter');
    await go('assign');
    await page.getByRole('tab',{name:'按 Agent 连接',exact:true}).click();
    await page.locator('#assign-agent-panel [name="gateway_id"][value="g1"]').check();
    await page.locator('#assign-agent-panel .selection-search').fill('Hong Kong');
    await page.locator('#assign-agent-panel .selection-search').press('Enter');
    await page.getByRole('tab',{name:'全部未开启连接',exact:true}).click();
    await page.locator('.pending-choice input:not(:disabled)').first().check();
    await page.locator('.pending-search').fill('Hong Kong');
    await page.locator('.pending-search').press('Enter');
    // A refresh is also a barrier for any fetch incorrectly started by a search keypress.
    await refresh();
    assert.equal(searchPosts,0,'Enter in a filter submitted a mutation');
    assert.equal(await page.locator('.pending-choice input:checked').count(),1,'search changed pending selections');
    await page.locator('[data-pending-clear]').click();
    await page.getByRole('tab',{name:'按 Agent 连接',exact:true}).click();
    await page.locator('#assign-agent-panel .selection-tools button').last().click();
    for (const path of ['subscribe','assign-gateways','assign-connections']) await page.unroute(`**/admin/${path}`,blockSearchSubmit);
    await go('subscribe');
    await page.locator('#subscribe .selection-tools button').last().click();
    await page.locator('#subscribe .selection-search').fill('');
    // Even while dashboard loading is delayed, never show another user's copy action.
    const cold=await page.context().newPage(); cold.on('pageerror',error=>errors.push(error.message));
    await cold.addInitScript(()=>sessionStorage.setItem('xmesh_selected_user','u2'));
    let releaseCold; const coldGate=new Promise(resolve=>{releaseCold=resolve;});
    await cold.route('**/admin/dashboard',async route=>{await coldGate;await route.continue();});
    await cold.goto(base+'/#subscribe');
    assert.equal(await cold.locator('.subscription-row:visible').count(),1);
    assert.equal(await cold.locator('.subscription-row:visible').getAttribute('data-user-id'),'u2');
    await cold.locator('#subscribe select[name="user_id"]').selectOption('u1');
    assert.equal(await cold.locator('.subscription-row:visible').getAttribute('data-user-id'),'u1');
    releaseCold();
    await cold.waitForFunction(()=>document.querySelector('#refresh-note').textContent.startsWith('已更新'));
    await cold.close();

    // Reverting a form with implicit SELECT defaults must not produce a false draft warning.
    const reverted=await context.newPage();
    await reverted.goto(base+'/#create-route');
    await reverted.waitForFunction(()=>document.querySelector('#refresh-note').textContent.startsWith('已更新'));
    await reverted.locator('#create-route [name="gateway_name"]').fill('Temporary draft');
    await reverted.locator('#create-route [name="gateway_name"]').fill('');
    assert.deepEqual(await reverted.evaluate(()=>{
      let prompts=0;const original=window.confirm;window.confirm=()=>{prompts++;return false;};
      const allowed=dispatchEvent(new Event('xmesh:before-language-change',{cancelable:true}));window.confirm=original;
      return {prompts,allowed};
    }),{prompts:0,allowed:true});
    // A reverted profile must resume following external updates, rather than retaining a stale draft marker.
    await reverted.evaluate(()=>location.hash='users');
    const revertedProfile=reverted.locator('#user-table tr[data-user-id="u1"] .user-profile');
    await revertedProfile.locator('summary').click();
    await revertedProfile.locator('[name="name"]').fill('Temporary profile');
    await revertedProfile.locator('[name="name"]').fill('Personal');
    await post('/admin/users/u1/edit',{name:'External profile update',notes:'Updated externally'});
    await reverted.locator('#refresh-status').click();
    await reverted.waitForFunction(()=>document.querySelector('#user-table tr[data-user-id="u1"] .user-profile [name="name"]').value==='External profile update');
    assert.equal(await revertedProfile.locator('[name="notes"]').inputValue(),'Updated externally');
    await post('/admin/users/u1/edit',{name:'Personal',notes:''});
    await reverted.close();

    await go('routes');
    await page.locator('#routes .route-card').filter({has:page.locator('[data-grant-route="r1"]')}).getByRole('button',{name:'查看状态'}).click();
    const rename=page.locator('#detail-dialog form[action="/admin/attachments/r1/edit"]');
    await rename.locator('[name="name"]').fill('Unsaved route draft');
    page.once('dialog',async dialog=>{assert.equal(dialog.type(),'confirm');await dialog.dismiss();});
    await page.locator('#close-detail').click();
    assert.equal(await rename.locator('[name="name"]').inputValue(),'Unsaved route draft');
    page.once('dialog',async dialog=>{assert.equal(dialog.type(),'confirm');await dialog.dismiss();});
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('#detail-dialog').evaluate(e=>e.open),true,'Escape silently discarded route edits');
    const beforeLink=page.url();
    for (const link of [page.locator('#detail-dialog a').first(),page.locator('#detail-dialog a').last()]) {
      page.once('dialog',async dialog=>{assert.equal(dialog.type(),'confirm');await dialog.dismiss();});
      await link.click();
      assert.equal(await page.locator('#detail-dialog').evaluate(e=>e.open),true,'detail navigation silently discarded route edits');
      assert.equal(await rename.locator('[name="name"]').inputValue(),'Unsaved route draft');
      assert.equal(page.url(),beforeLink,'cancelled detail navigation still changed sections');
    }
    await rename.locator('[name="name"]').fill('');
    await page.locator('#close-detail').click();
    await page.locator('#detail-dialog').waitFor({state:'hidden'});
    await page.locator('#routes .route-card').filter({has:page.locator('[data-grant-route="r1"]')}).getByRole('button',{name:'查看状态'}).click();
    await rename.locator('[name="name"]').fill('香港专线初稿');
    let releaseRename,capturedRename;
    const renameGate=new Promise(resolve=>{releaseRename=resolve;}),renameStarted=new Promise(resolve=>{capturedRename=resolve;});
    await page.route('**/admin/attachments/r1/edit',async route=>{capturedRename();await renameGate;await route.continue();});
    await rename.locator('button').click();
    await renameStarted;await page.locator('#close-detail').click();
    assert.equal(await page.locator('#detail-dialog').evaluate(e=>e.open),true,'pending save was dismissed');
    assert.match(await page.locator('#detail-feedback').textContent(),/正在保存/);
    await page.locator('#detail-dialog a').last().click();
    assert.equal(page.url(),beforeLink,'pending save allowed detail navigation');
    await rename.locator('[name="name"]').fill('香港专线 <A>');
    releaseRename();
    await ready();
    assert.equal(await page.locator('#detail-dialog').evaluate(e=>e.open),true,'save response discarded newer edits');
    assert.equal(await rename.locator('[name="name"]').inputValue(),'香港专线 <A>');
    assert.match(await page.locator('#detail-feedback').textContent(),/新修改已保留/);
    assert.equal((await dashboard()).routes.find(route=>route.id==='r1').name,'香港专线初稿');
    // Returning to the pre-save value remains a new edit relative to the acknowledged save.
    await rename.locator('[name="name"]').fill('');
    page.once('dialog',async dialog=>{assert.equal(dialog.type(),'confirm');await dialog.dismiss();});
    await page.locator('#close-detail').click();
    assert.equal(await page.locator('#detail-dialog').evaluate(e=>e.open),true);
    await rename.locator('[name="name"]').fill('香港专线 <A>');
    await rename.locator('button').click();
    await page.locator('#detail-dialog').waitFor({state:'hidden'}); await ready();
    await page.unroute('**/admin/attachments/r1/edit');
    assert.match(await page.locator('#routes').textContent(),/香港专线 <A>/);
    assert.match(await page.locator('#routes .route-card').filter({has:page.locator('[data-grant-route="r1"]')}).locator('strong').innerHTML(),/&lt;A&gt;/,'name was not escaped');
    const subURL=await page.locator('.subscription-row[data-user-id="u1"] [data-copy]').getAttribute('data-copy');
    const payload=Buffer.from(await (await page.request.get(subURL)).text(),'base64').toString();
    const vmess=JSON.parse(Buffer.from(payload.split('\n').find(s=>s.startsWith('vmess://')).slice(8),'base64').toString());
    assert.match(vmess.ps,/香港专线 <A>/);

    await post('/admin/subscribe',{user_id:'u1',attachment_id:'r2'}); await refresh();
    await go('grants'); await page.locator('#workspace-user').selectOption('u1');
    const first=page.locator('#grant-table tr[data-user-id="u1"]').filter({hasText:'香港专线 <A>'});
    await first.locator('input[name="grant_id"]').check();
    await page.locator('#grant-search').fill('no matching route');
    assert.equal(await page.locator('#grant-table input:checked').count(),1);
    assert.match(await page.locator('#grant-selection-summary').textContent(),/1 项被筛选隐藏/);
    assert.equal(await page.locator('#grant-bulk button').isDisabled(),false);
    await page.locator('[data-grant-clear]').click(); await page.locator('#grant-search').fill('');
    await first.locator('.grant-state-form button').click(); await ready();
    await page.waitForFunction(()=>[...document.querySelectorAll('#grant-table tr[data-user-id="u1"]')].some(r=>r.textContent.includes('停用已提交')));
    assert.match(await first.locator('td').nth(3).textContent(),/等待入口确认配置/);
    await first.locator('details').last().locator('summary').click();
    assert.equal(await first.locator('[name="confirm_name"]').getAttribute('placeholder'),'Personal / 香港专线 <A>');

    await go('users'); await page.locator('#workspace-user').selectOption('u2');
    const row=page.locator('#user-table tr[data-user-id="u2"]');
    await row.locator('.user-profile summary').click();
    const profile=row.locator('form[action$="/edit"]');
    await profile.locator('[name="name"]').fill('团队用户 <二>');
    await profile.locator('[name="notes"]').fill('客户备注 draft');
    page.once('dialog',async dialog=>{assert.equal(dialog.type(),'confirm');await dialog.dismiss();});
    await page.locator('[data-language-select]').selectOption('en');
    assert.equal(await page.locator('[data-language-select]').inputValue(),'zh');
    await refresh(); assert.equal(await profile.locator('[name="notes"]').inputValue(),'客户备注 draft');
    // Reload must present the native unsaved-changes guard and preserve on cancel.
    const dialogSeen=new Promise(resolve=>page.once('dialog',async dialog=>{assert.equal(dialog.type(),'beforeunload');await dialog.dismiss();resolve();}));
    await page.evaluate(()=>{setTimeout(()=>location.reload(),0);}); await dialogSeen;
    assert.equal(await profile.locator('[name="notes"]').inputValue(),'客户备注 draft');
    await profile.locator('button').click(); await ready();
    await page.waitForFunction(()=>document.querySelector('#user-table tr[data-user-id="u2"] strong').textContent==='团队用户 <二>');
    assert.match(await page.locator('#user-context-summary').textContent(),/团队用户 <二>/);
    assert.match(await page.locator('#user-audit tr[data-user-id="u2"]').textContent(),/团队用户 <二>/,'audit list retained the old user name');
    await row.locator('form[action$="/archive"] button').click(); await ready();
    await page.waitForFunction(()=>document.querySelector('#user-table tr[data-user-id="u2"]').dataset.archived==='true');
    assert.equal(await row.isVisible(),false);
    await page.locator('#user-archive-filter').selectOption('archived');
    assert.equal(await row.isVisible(),true); assert.equal(await row.locator('.user-state-form button').isDisabled(),true);
    await row.locator('form[action$="/archive"] button').click(); await ready();
    await page.waitForFunction(()=>document.querySelector('#user-table tr[data-user-id="u2"]').dataset.archived==='false');
    assert.equal((await dashboard()).users.find(u=>u.id==='u2').enabled,false);
    await page.locator('#user-archive-filter').selectOption('all');
    assert.equal(await row.locator('.user-state-form button').isDisabled(),false);

    await post('/admin/users',{name:'New audit user'});
    await refresh();
    const newUser=(await dashboard()).users.find(user=>user.name==='New audit user');
    await page.waitForFunction(id=>!!document.querySelector(`#user-audit tr[data-user-id="${id}"]`),newUser.id);
    await go('user-audit');await page.locator('#workspace-user').selectOption('');
    await page.locator('#user-audit .table-search').fill('New audit user');
    assert.equal(await page.locator('#user-audit tr[data-user-id]:visible').count(),1);
    await page.locator('#workspace-user').selectOption('u2');
    assert.equal(await page.locator('#user-audit tr[data-user-id]:visible').count(),0,'user switch ignored audit search');
    await page.locator('#user-audit .table-search').fill('团队用户');
    await post('/admin/users/u2/audit',{enabled:'true',limit_mb:'45'});
    await refresh();
    assert.equal(await page.locator('#user-audit tr[data-user-id]:visible').count(),1,'refresh lost the combined audit filters');
    assert.match(await page.locator('#user-audit tr[data-user-id="u2"]').textContent(),/已开启.*45 MB/s,'audit settings remained stale');

    // Remote state changes must update each action, and repeats must keep the requested state.
    for (const [section,kind,id] of [['gateways','gateways','g1'],['agents','agents','a1'],['assignments','attachments','r1'],['links','links','l1']]) {
      const path=`/admin/${kind}/${id}/toggle`;
      await post(path,{enabled:'false'});await refresh();await go(section);
      const form=page.locator(`form[action="${path}"]`);
      assert.equal(await form.locator('button').textContent(),'启用',`${kind} action retained stale enabled state`);
      assert.equal(await form.locator('[name="enabled"]').inputValue(),'true');
      const before=(await dashboard()).nodes.map(node=>[node.id,node.desired]);
      await post(path,{enabled:'false'});await refresh();
      assert.deepEqual((await dashboard()).nodes.map(node=>[node.id,node.desired]),before,'repeated disable issued another configuration update');
      assert.equal(await form.locator('button').textContent(),'启用','repeated disable re-enabled the object');
      await form.locator('button').click();await ready();
      assert.equal(await form.locator('button').textContent(),'停用');
    }
    await go('gateways');
    const ingress=page.locator('form[action="/admin/gateways/g1/ingress"]');
    await post('/admin/gateways/g1/ingress',{ingress_controls:'1'});await refresh();
    assert.equal(await ingress.locator('[name="vmess_enabled"]').isChecked(),false,'clean ingress controls ignored a remote disable');
    const nodeEdit=page.locator('form[action="/admin/gateways/g1/edit"]');
    assert.equal(await nodeEdit.locator('[name="vmess_enabled"]').isChecked(),false,'edit form retained a stale protocol switch');
    // A local protocol draft remains intact while another window changes the saved settings.
    await ingress.locator('[name="hy2_enabled"]').check();
    await post('/admin/gateways/g1/ingress',{ingress_controls:'1',vmess_enabled:'1'});await refresh();
    assert.equal(await ingress.locator('[name="vmess_enabled"]').isChecked(),false,'refresh overwrote a local ingress draft');
    assert.equal(await ingress.locator('[name="hy2_enabled"]').isChecked(),true);
    await ingress.locator('[name="hy2_enabled"]').uncheck();
    await refresh();
    assert.equal(await ingress.locator('[name="vmess_enabled"]').isChecked(),true,'reverted ingress draft did not resume live updates');
    assert.equal(await ingress.locator('[name="hy2_enabled"]').isChecked(),false);

    await post('/admin/gateways/g1/toggle'); await refresh(); await go('network-map');
    await page.locator('#map-state').selectOption('disabled');
    assert.equal(await page.locator('#topology .topology-routes tbody tr').count(),1,'disabled gateway hid its route');
    assert.match(await page.locator('#topology').textContent(),/香港专线/);
    await post('/admin/gateways/g1/toggle'); await refresh();
    await go('upstreams');
    const exitName='State action QA exit', source='vmess://'+Buffer.from(JSON.stringify({
      v:'2',ps:exitName,add:'exit.example',port:'443',id:'00000000-0000-4000-8000-000000000022',
      aid:'0',scy:'auto',net:'ws',type:'none',host:'exit.example',path:'/proxy',tls:'tls',
    })).toString('base64');
    const createExit=page.locator('form[action="/admin/upstreams"]');
    await createExit.locator('[name="name"]').fill(exitName);await createExit.locator('[name="source"]').fill(source);
    await createExit.locator('button').click();await ready();
    const exit=(await dashboard()).nodes.find(node=>node.name===exitName);
    const exitToggle=page.locator(`form[action="/admin/upstreams/${exit.id}/toggle"]`);
    await exitToggle.locator('..').evaluate(details=>details.open=true);
    await post(`/admin/upstreams/${exit.id}/toggle`,{enabled:'false'});await refresh();
    assert.equal(await exitToggle.locator('button').textContent(),'启用','external exit action did not follow remote state');
    assert.match(await exitToggle.locator('..').locator(':scope > summary').textContent(),/停用/);
    await exitToggle.locator('button').click();await ready();
    assert.equal(await exitToggle.locator('button').textContent(),'停用');
    await post(`/admin/upstreams/${exit.id}/delete`,{confirm_name:exitName});await refresh();
    assert.equal(await exitToggle.count(),0,'deleted external exit remained in the management list');
    await go('gateways');
    const embedded=page.locator('form[action="/admin/gateways/g1/agent"]');
    await post('/admin/gateways/g1/agent',{enabled:'true'});await refresh();
    assert.equal(await embedded.locator('button').textContent(),'关闭内置 Agent');
    assert.equal(await embedded.locator('[name="enabled"]').inputValue(),'false');
    await embedded.locator('button').click();await ready();
    assert.equal(await embedded.locator('button').textContent(),'启用内置 Agent');
    await go('quick-install');
    let releaseInstall,capturedInstall,installResponse;
    const installGate=new Promise(resolve=>{releaseInstall=resolve;}),installStarted=new Promise(resolve=>{capturedInstall=resolve;});
    await page.route('**/admin/enrollments',async route=>{
      const response=await route.fetch();installResponse=await response.text();capturedInstall();
      await installGate;await route.fulfill({response});
    });
    await page.locator('form[action="/admin/enrollments"]').filter({has:page.locator('[name="node_id"][value="g2"]')}).locator('button').click();
    await installStarted;
    await go('routes');
    await page.locator('#routes .route-card').filter({has:page.locator('[data-grant-route="r1"]')}).getByRole('button',{name:'查看状态'}).click();
    await rename.locator('[name="name"]').fill('安装期间的草稿');
    releaseInstall();await ready();
    assert.equal(await rename.locator('[name="name"]').inputValue(),'安装期间的草稿','delayed installation response replaced route draft');
    assert.equal(await page.locator('#install-command-choice').count(),0);
    assert.match(await page.locator('#detail-feedback').textContent(),/关闭当前窗口后显示/);
    page.once('dialog',async dialog=>{assert.match(dialog.message(),/尚未查看的安装选项/);await dialog.dismiss();});
    await page.locator('[data-language-select]').evaluate(select=>{select.value='en';select.dispatchEvent(new Event('change',{bubbles:true}));});
    assert.equal(await page.locator('[data-language-select]').inputValue(),'zh');
    page.once('dialog',async dialog=>{assert.equal(dialog.type(),'confirm');await dialog.dismiss();});
    await page.locator('#close-detail').click();
    assert.equal(await rename.locator('[name="name"]').inputValue(),'安装期间的草稿');
    page.once('dialog',async dialog=>{assert.equal(dialog.type(),'confirm');await dialog.accept();});
    await page.locator('#close-detail').click();
    await page.locator('#install-command-choice').waitFor();
    await page.unroute('**/admin/enrollments');
    assert.equal(await page.locator('#install-command-choice option').count(),8,'existing nodes retain Docker and systemd credential rotation options');
    assert.equal(await page.locator('.install-token').textContent(),installResponse.match(/^One-time enrollment token[^\n]*:\n(\S+)/m)[1],'queued installation token was not preserved');
    const command=page.locator('#detail-dialog .copy-install-command');
    assert.match(await command.getAttribute('data-copy'),/install\.sh/);
    await page.locator('#install-command-choice').selectOption('1');
    assert.match(await command.getAttribute('data-copy'),/install-docker\.sh/);
    assert.equal((await command.getAttribute('data-copy')).split('\n').length,1);
    await page.locator('#install-command-choice').selectOption('4');
    assert.match(await command.getAttribute('data-copy'),/--rotate-credential/);
    assert.equal(await page.locator('#install-rotation-hint').isVisible(),true);
    await page.locator('#install-command-choice').selectOption('6');
    assert.match(await command.getAttribute('data-copy'),/install\.sh/);
    assert.match(await command.getAttribute('data-copy'),/--rotate-credential/);
    await page.locator('#close-detail').click();
    await ready();await go('install');
    const tokenRow=page.locator('#install tr[data-enrollment-id]');
    await tokenRow.waitFor();
    assert.match(await tokenRow.textContent(),/Singapore Edge/,'token list omitted the configured node name');
    assert.equal(await tokenRow.locator('form[action$="/revoke"]').count(),1,'new enrollment cannot be revoked without reload');
    await tokenRow.locator('details summary').click();await refresh();
    assert.equal(await tokenRow.locator('details').evaluate(e=>e.open),true,'refresh collapsed token details');
    await tokenRow.locator('button').click();await ready();
    await page.waitForFunction(()=>!document.querySelector('#install form[action$="/revoke"]'));
    assert.match(await tokenRow.textContent(),/已过期/);

    await page.setViewportSize({width:390,height:844}); await go('users');
    await page.locator('#user-archive-filter').selectOption('active');
    await row.locator('.user-profile').evaluate(e=>e.open=false);
    const out=process.env.XMESH_PANEL_SCREENSHOTS || 'tmp/workspace-screenshots';fs.mkdirSync(out,{recursive:true});
    await page.screenshot({path:out+'/users-mobile.png',fullPage:true});
    const userHeight=await page.evaluate(()=>document.documentElement.scrollHeight);
    assert.ok(userHeight<3000,`user page still too long: ${userHeight}`);
    await go('network'); await page.screenshot({path:out+'/network-mobile.png',fullPage:true});
    const networkHeight=await page.evaluate(()=>document.documentElement.scrollHeight);
    assert.ok(networkHeight<3500,`network page still too long: ${networkHeight}`);
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'mobile page overflows horizontally');
    assert.deepEqual(errors,[]);
    console.log(`Workspace browser checks passed; mobile users ${userHeight}px, network ${networkHeight}px.`);
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
