// Run against TestFirstUserBrowserFixture; the state has routes but no users.
const { chromium } = require('playwright');
const assert = require('node:assert/strict');

(async () => {
  const browser = await chromium.launch({ headless: true,
    channel: process.env.XMESH_BROWSER_CHANNEL || undefined,
    executablePath: process.env.XMESH_BROWSER_EXECUTABLE || undefined });
  try {
    const context = await browser.newContext({ locale: 'zh-CN' });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    const base = process.env.XMESH_PANEL_TEST_URL || 'http://127.0.0.1:18095';
    await page.context().addInitScript(() => sessionStorage.setItem('xmesh_expand_sections', 'true'));
    page.on('dialog', dialog => dialog.accept());
    await page.goto(base + '/login');
    await page.locator('[name="username"]').fill('admin');
    await page.locator('[name="password"]').fill('a-strong-test-password');
    await page.locator('form[action="/login"] button').click();
    const ready = (tab) => tab.waitForFunction(() => !document.querySelector('#refresh-status').disabled &&
      document.querySelector('#refresh-note').textContent.startsWith('已更新'));
    await ready(page);
    const dashboard = () => page.request.get(base + '/admin/dashboard').then((r) => r.json());
    assert.equal((await dashboard()).users.length, 0, 'fixture must start with no users');
    const other = await context.newPage();
    other.on('pageerror', (error) => errors.push(error.message));
    await other.goto(base + '/#subscribe');
    await ready(other);
    const otherOwner = other.locator('#subscribe select[name="user_id"]');
    assert.equal(await otherOwner.locator('option').count(), 0);
    assert.equal(await other.locator('#subscribe input[name="attachment_id"]:not(:disabled)').count(), 0);
    // Keep a search while a live fragment populates the first user in the
    // other window, without a full section replacement.
    await other.locator('#subscribe .selection-search').fill('Hong Kong');
    await page.goto(base + '/#users');
    await page.locator('#users details.create-details > summary').click();
    const create = page.locator('#users form[action="/admin/users"]');
    await create.locator('[name="name"]').fill('First <user>');
    await create.locator('button').click();
    await page.locator('#users .form-success').waitFor();
    await ready(page);
    assert.equal(await create.locator('[name="name"]').inputValue(), '',
      'successful creation left the submitted user name ready to submit again');
    assert.equal(await create.locator('[name="name"]').evaluate(input => input.defaultValue), '');
    const first = (await dashboard()).users[0];
    const owner = page.locator('#subscribe select[name="user_id"]');
    assert.equal(await owner.inputValue(), first.id, 'creating the first user left subscription ownership blank');
    assert.equal(await page.locator('#subscribe input[name="attachment_id"]:not(:disabled)').count(), 2,
      'creating the first user left all routes unavailable');
    await other.locator('#refresh-status').click();
    await ready(other);
    assert.equal(await otherOwner.inputValue(), first.id, 'external first-user creation left subscription ownership blank');
    assert.equal(await otherOwner.locator('option:checked').textContent(), first.name);
    assert.equal(await other.locator('#subscribe .selection-search').inputValue(), 'Hong Kong');
    await other.locator('#subscribe .selection-group').selectOption('g1');
    await other.locator('#subscribe .selection-filter').selectOption('available');
    await other.locator('#subscribe input[value="r1"]').check();
    // Adding another user must retain the chosen owner and pending route.
    const csrf = await page.locator('#grant-bulk [name="csrf"]').inputValue();
    const response = await page.request.post(base + '/admin/users', { form: { csrf, name: 'Another user' } });
    assert.equal(response.status(), 200);
    const second = (await dashboard()).users.find((user) => user.id !== first.id);
    await other.locator('#refresh-status').click();
    await ready(other);
    assert.equal(await otherOwner.locator('option').count(), 2);
    assert.equal(await otherOwner.inputValue(), first.id, 'adding a user switched an existing owner');
    assert.equal(await other.locator('#subscribe input[value="r1"]').isChecked(), true);
    await other.locator('#subscribe button[type="submit"]').click();
    await other.locator('#subscribe .form-success').waitFor();
    await ready(other);
    assert.equal(await other.locator('#subscribe .selection-search').inputValue(), 'Hong Kong', 'successful subscription cleared the search');
    assert.equal(await other.locator('#subscribe .selection-group').inputValue(), 'g1', 'successful subscription reset the Gateway group');
    assert.equal(await other.locator('#subscribe .selection-filter').inputValue(), 'available', 'successful subscription reset the state filter');
    const state = await dashboard();
    assert.equal(state.grants.length, 1);
    assert.equal(state.grants[0].user_id, first.id, 'first route was granted to the wrong user');
    assert.equal(state.grants[0].route_id, 'r1');
    await other.locator('#subscribe input[value="r1"]').check();
    await otherOwner.selectOption(second.id);
    assert.equal(await other.locator('#subscribe input:checked').count(), 0, 'switching owners retained another user\'s selection');

    const subscribe = other.locator('#subscribe form.chooser');
    const delaySubscription = async editPending => {
      let release, captured;
      const gate = new Promise(resolve => { release = resolve; });
      const started = new Promise(resolve => { captured = resolve; });
      await other.route('**/admin/subscribe', async route => {
        const response = await route.fetch();
        captured();
        await gate;
        await route.fulfill({ response });
      });
      await subscribe.locator('button[type="submit"]').click();
      try { await started; await editPending(); } finally { release(); }
      await other.waitForFunction(() => !document.querySelector('#subscribe form.chooser').hasAttribute('aria-busy'));
      await ready(other);
      await other.unroute('**/admin/subscribe');
    };
    await otherOwner.selectOption(first.id);
    await other.locator('#subscribe .selection-search').fill('');
    await other.locator('#subscribe .selection-group').selectOption('');
    await other.locator('#subscribe .selection-filter').selectOption('all');
    const firstRoute = subscribe.locator('input[name="attachment_id"][value="r1"]');
    const nextRoute = subscribe.locator('input[name="attachment_id"][value="r2"]');
    await firstRoute.check();
    await delaySubscription(async () => { await nextRoute.check(); });
    assert.equal(await firstRoute.isChecked(), false, 'completed subscription remained selected for the next submission');
    assert.equal(await nextRoute.isChecked(), true, 'a route selected during submission was lost');
    assert.equal((await dashboard()).grants.some(grant => grant.user_id === first.id && grant.route_id === 'r2'), false,
      'a route selected after submission was granted before the next request');
    await nextRoute.uncheck();
    assert.equal(await subscribe.getAttribute('data-dirty'), null, 'clearing the next subscription draft retained a saved selection baseline');

    await firstRoute.check();
    await delaySubscription(async () => {
      await otherOwner.selectOption(second.id);
      await firstRoute.check();
    });
    assert.equal(await otherOwner.inputValue(), second.id);
    assert.equal(await firstRoute.isChecked(), true, 'completion for the previous user cleared the new user\'s identical route draft');
    assert.equal((await dashboard()).grants.some(grant => grant.user_id === second.id), false,
      'editing the next owner changed the owner of the request already submitted');
    await firstRoute.uncheck();
    assert.equal(await subscribe.getAttribute('data-dirty'), null);

    // A name entered while the previous creation is pending belongs to the
    // next user. Preserve it, including its unsaved state after the response.
    let releasePost, postStarted;
    const gate = new Promise(resolve => { releasePost = resolve; });
    const started = new Promise(resolve => { postStarted = resolve; });
    await page.route('**/admin/users', async route => {
      const response = await route.fetch();
      postStarted();
      await gate;
      await route.fulfill({ response });
    });
    const nameInput = create.locator('[name="name"]');
    await nameInput.fill('Saved while pending');
    await create.locator('button').click();
    try {
      await started;
      await nameInput.fill('Next user draft');
    } finally { releasePost(); }
    await page.waitForFunction(() => !document.querySelector('#users form[action="/admin/users"]').hasAttribute('aria-busy'));
    await ready(page);
    await page.unroute('**/admin/users');
    assert.equal(await nameInput.inputValue(), 'Next user draft', 'successful creation erased the next user draft');
    assert.equal(await nameInput.evaluate(input => input.defaultValue), '', 'the create form retained a submitted name as its baseline');
    await nameInput.fill('Saved while pending');
    assert.equal(await create.getAttribute('data-dirty'), 'true', 'reusing the previous name was not tracked as a new draft');
    await nameInput.fill('');
    assert.equal(await create.getAttribute('data-dirty'), null, 'clearing the creation form still counted as unsaved');
    assert.deepEqual(errors, []);
    console.log('First-user browser checks passed: creation reset, pending choices and owner changes, empty users, external refresh, available routes and scoped grants.');
  } finally {
    await browser.close();
  }
})().catch((error) => { console.error(error); process.exitCode = 1; });
