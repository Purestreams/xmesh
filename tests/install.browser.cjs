// Uses only TestPanelBrowserFixture state; may run after workspace and recovery.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');

(async()=>{
  const browser=await chromium.launch({headless:true,
    executablePath:process.env.XMESH_BROWSER_EXECUTABLE||undefined,
    channel:process.env.XMESH_BROWSER_CHANNEL||undefined});
  try {
    const context=await browser.newContext({locale:'zh-CN',viewport:{width:1440,height:1000}});
    await context.grantPermissions(['clipboard-read','clipboard-write']);
    const page=await context.newPage(),errors=[];
    page.on('pageerror',error=>errors.push(error.message));
    const base=process.env.XMESH_PANEL_TEST_URL||'http://127.0.0.1:18096';
    const ready=()=>page.waitForFunction(()=>!document.querySelector('#refresh-status').disabled &&
      !document.querySelector('form[aria-busy]') && /^(已更新|Updated)/.test(document.querySelector('#refresh-note').textContent));
    const refresh=async()=>{await page.locator('#refresh-status').click();await ready();};
    const go=id=>page.evaluate(id=>location.hash=id,id);
    const dashboard=async()=>await(await page.request.get(base+'/admin/dashboard')).json();
    await page.goto(base+'/login');await page.locator('[name=username]').fill('admin');
    await page.locator('[name=password]').fill('a-strong-test-password');await page.locator('form button').click();await ready();
    const csrf=await page.locator('input[name=csrf]').first().inputValue();
    const post=async(path,values)=>{
      const response=await page.request.post(base+path,{form:{csrf,...values},maxRedirects:0});
      assert.equal(response.status(),303,await response.text());
    };
    await go('quick-install');
    const name='New <Agent> {1}';await post('/admin/agents',{name});
    const created=(await dashboard()).nodes.find(node=>node.name===name);
    const install=page.locator('#quick-install form').filter({has:page.locator(`[name=node_id][value="${created.id}"]`)});
    await refresh();assert.equal(await install.count(),1,'new node has no installation button after refresh');
    assert.equal(await install.locator('button').textContent(),'安装 Agent：'+name);
    const retained=await install.elementHandle();await refresh();
    assert.equal(await install.evaluate((form,original)=>form===original,retained),true,'refresh replaced an existing install form');
    await page.route('**/admin/dashboard',async route=>{
      const response=await route.fetch(),state=await response.json();
      state.nodes.find(node=>node.id===created.id).gateway_id='g1';await route.fulfill({response,json:state});
    });
    await refresh();assert.equal(await install.count(),0,'embedded Agent has an independent installation command');
    await page.unroute('**/admin/dashboard');await refresh();assert.equal(await install.count(),1);
    await post('/admin/agents/'+created.id+'/delete',{confirm_name:name});await refresh();
    assert.equal(await install.count(),0,'deleted node retained an active installation button');

    await go('node-upgrades');
    const target=(await dashboard()).nodes.find(node=>node.id==='g2');
    const pairing=page.waitForResponse(response=>response.url().endsWith('/admin/updaters/gateway/g2/pair') && response.request().method()==='POST');
    await page.locator('form[action="/admin/updaters/gateway/g2/pair"] button').click();
    const payload=await(await pairing).text(),token=payload.match(/^Pair token[^\n]*: (\S+)/m)[1];
    await page.locator('.install-token').waitFor();await ready();
    assert.equal(await page.locator('#detail-title').textContent(),'配对升级助手：'+target.name);
    assert.equal(await page.locator('.install-target').textContent(),'目标节点：Gateway '+target.name);
    assert.equal(await page.locator('.install-token').textContent(),token);
    assert.equal(await page.locator('#install-command-choice option').count(),2);
    const body=await page.locator('#detail-body').textContent();
    assert.ok(body.includes('无需再手动输入'));assert.ok(!body.includes('安装令牌区撤销'));
    const command=page.locator('.copy-install-command');
    assert.ok((await command.getAttribute('data-copy')).includes(token));
    await page.getByRole('button',{name:'复制配对令牌',exact:true}).click();
    assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),token,'copy token included instructions or a command');
    // Clipboard permissions can be denied on a self-hosted panel. Select the
    // exact displayed token/command so keyboard copying is still usable.
    await page.evaluate(()=>{
      window.fixtureClipboardWriteText=navigator.clipboard.writeText;
      navigator.clipboard.writeText=async()=>{throw new DOMException('Clipboard denied','NotAllowedError');};
    });
    await page.getByRole('button',{name:'复制配对令牌',exact:true}).click();
    await page.waitForFunction(token=>getSelection().toString()===token,token);
    await command.click();
    const selectedCommand=await command.getAttribute('data-copy');
    await page.waitForFunction(value=>getSelection().toString()===value,selectedCommand);
    await page.locator('#install-command-choice').selectOption('1');await command.click();
    const alternateCommand=await command.getAttribute('data-copy');assert.notEqual(alternateCommand,selectedCommand);
    await page.waitForFunction(value=>getSelection().toString()===value,alternateCommand);
    await page.evaluate(()=>{
      navigator.clipboard.writeText=window.fixtureClipboardWriteText;delete window.fixtureClipboardWriteText;
    });
    assert.equal((await dashboard()).updaters.g2.State,'配对中','pending pairing is displayed as unpaired or online');
    assert.match(await page.locator('#updater-table tr[data-node-id=g2]').textContent(),/配对中/);
    assert.ok(!JSON.stringify(await dashboard()).includes(token),'dashboard leaked a pairing token');

    page.once('dialog',async dialog=>{assert.match(dialog.message(),/令牌只显示一次/);await dialog.dismiss();});
    await page.locator('[data-language-select]').selectOption('en');
    assert.equal(await page.locator('[data-language-select]').inputValue(),'zh');
    assert.equal(await page.locator('.install-token').textContent(),token,'dismissed language change lost the token');
    const guarded=new Promise(resolve=>page.once('dialog',async dialog=>{
      assert.equal(dialog.type(),'beforeunload');await dialog.dismiss();resolve();
    }));
    await page.evaluate(()=>setTimeout(()=>location.reload(),0));await guarded;
    assert.equal(await page.locator('.install-token').textContent(),token,'cancelled reload lost the token');
    await page.locator('#close-detail').click();

    // Completing the pair removes the pending state without exposing credentials in dashboard JSON.
    const paired=await page.request.post(base+'/api/v1/updater/pair',{data:{role:'gateway',node_id:'g2',token}});
    assert.equal(paired.status(),200);const {credential}=await paired.json();
    const heartbeat=await page.request.post(base+'/api/v1/updater/heartbeat',{
      headers:{Authorization:'Bearer '+credential},data:{mode:'systemd',arch:'amd64',version:'v0.4.5',protocol:1}});
    assert.equal(heartbeat.status(),204);await refresh();
    assert.equal((await dashboard()).updaters.g2.State,'在线');
    assert.ok(!JSON.stringify(await dashboard()).includes(credential));

    await Promise.all([page.waitForNavigation(),page.locator('[data-language-select]').selectOption('en')]);await ready();
    assert.equal(await page.locator('html').getAttribute('lang'),'en-US','language change on a section URL did not reload the interface');
    await go('quick-install');
    const enrollment=page.waitForResponse(response=>response.url().endsWith('/admin/enrollments') && response.request().method()==='POST');
    await page.locator('#quick-install form').filter({has:page.locator('[name=node_id][value=g2]')}).locator('button').click();
    const enrollmentText=await(await enrollment).text(),enrollmentToken=enrollmentText.match(/^One-time enrollment token[^\n]*:\r?\n(\S+)/m)[1];
    await page.locator('.install-token').waitFor();await ready();
    assert.equal(await page.locator('#detail-title').textContent(),'Install node: '+target.name);
    assert.equal(await page.locator('.install-token').textContent(),enrollmentToken);
    assert.ok(!(await command.getAttribute('data-copy')).includes(enrollmentToken),'interactive installation embeds the enrollment token');
    await page.getByRole('button',{name:'Copy enrollment token',exact:true}).click();
    assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),enrollmentToken);
    await page.locator('#close-detail').click();
    await page.route('**/admin/dashboard',async route=>{
      const response=await route.fetch(),state=await response.json();state.nodes=[];await route.fulfill({response,json:state});
    });
    await refresh();assert.equal(await page.locator('#quick-install form').count(),0);
    assert.match(await page.locator('#quick-install .empty').textContent(),/No installable nodes/);
    await page.unroute('**/admin/dashboard');await refresh();
    assert.equal(await page.locator('#quick-install .empty').count(),0);
    assert.deepEqual(errors,[]);
    console.log('Installation browser checks passed: live nodes, token copying, named dialogs, pairing guidance and recovery, one-time-token navigation guards and bilingual empty state.');
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
