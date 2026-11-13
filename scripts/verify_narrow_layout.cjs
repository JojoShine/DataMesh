// Optional browser acceptance check; use an existing Playwright installation.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const origin = process.env.BASE_URL || 'https://localhost:19443';
const accounts = JSON.parse(fs.readFileSync(process.env.EXPERIENCE_ACCOUNTS_FILE || '.local/experience/accounts.json'));
const account = accounts.security_author || accounts;
const routes = (process.env.NARROW_ROUTES || 'exchange/sources,exchange/directory,assets/catalog,security/policies,security/proofs').split(',');

(async () => {
  const browser = await chromium.launch({headless:true, channel:'chrome'});
  const context = await browser.newContext({ignoreHTTPSErrors:true});
  let principal;
  try {
    const response = await context.request.post(origin+'/auth/login', {headers:{Origin:origin}, data:{username:account.username, password:account.password}});
    assert.equal(response.status(), 200, 'login');
    principal = await response.json();
    if (process.env.FRONTEND_DIST) {
      const dist = path.resolve(process.env.FRONTEND_DIST);
      await context.route('**/*', async route => {
        const url = new URL(route.request().url());
        const file = url.pathname.startsWith('/static/') ? path.resolve(dist, '.'+url.pathname) : route.request().isNavigationRequest() ? path.join(dist, 'index.html') : null;
        if (file && file.startsWith(dist+path.sep) && fs.existsSync(file)) await route.fulfill({path:file});
        else await route.continue();
      });
    }
    await context.addInitScript(() => {
      window.layoutOverflows = [];
      const start = performance.now();
      const sample = () => {
        if (document.body && document.documentElement.scrollWidth > innerWidth+1 && window.layoutOverflows.length < 24) {
          window.layoutOverflows.push({width:innerWidth, actual:document.documentElement.scrollWidth});
        }
        if (performance.now()-start < 4000) requestAnimationFrame(sample);
      };
      requestAnimationFrame(sample);
    });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    for (const width of [320, 390, 540, 1920]) {
      await page.setViewportSize({width, height:1080});
      for (const route of routes) {
        await page.goto(origin+'/platform/'+route);
        await page.waitForLoadState('networkidle');
        await page.locator('.ant-table-content').waitFor();
        assert.equal(await page.locator('.ant-menu-submenu-popup:visible').count(), 0, `${width} ${route} unsolicited sidebar popup`);
        assert.deepEqual(await page.evaluate(() => window.layoutOverflows), [], `${width} ${route} initial frames`);
        const check = await page.evaluate(() => {
          const table = document.querySelector('.ant-table-content');
          table.scrollLeft = table.scrollWidth;
          const workspace = document.querySelector('.workspace').getBoundingClientRect();
          const controls = [...document.querySelectorAll('.asset-catalog-filters input,.asset-catalog-filters button,.security-trace-file,.ant-pagination')];
          return {documentWidth:document.documentElement.scrollWidth, viewport:innerWidth,
            tableRight:table.getBoundingClientRect().right, lastColumnReachable:table.scrollLeft+table.clientWidth >= table.scrollWidth-1,
            controlsContained:controls.every(control => {const r=control.getBoundingClientRect(); return r.left>=workspace.left-1 && r.right<=workspace.right+1;})};
        });
        assert.ok(check.documentWidth<=width+1 && check.tableRight<=width+1 && check.lastColumnReachable && check.controlsContained, `${width} ${route}: ${JSON.stringify(check)}`);
        if (route === 'exchange/sources') {
          for (const name of width<992 ? ['展开侧栏','收起侧栏'] : ['收起侧栏','展开侧栏']) {
            await page.getByRole('button',{name,exact:true}).click();
            await page.waitForTimeout(350);
            assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1), `${width} ${name}`);
          }
          assert.deepEqual(await page.evaluate(()=>window.layoutOverflows), [], `${width} sidebar transition`);
        }
      }
      console.log(JSON.stringify({width,routes:routes.length,initialFrames:true,tableEnd:true,controls:true,sidebar:true}));
    }
    assert.deepEqual(errors, []);
  } finally {
    if (principal) await context.request.post(origin+'/auth/logout', {headers:{Origin:origin,'X-CSRF-Token':principal.csrf_token}});
    await browser.close();
  }
})().catch(error => {console.error(error.message); process.exitCode=1;});
