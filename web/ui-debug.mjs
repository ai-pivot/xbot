import { chromium } from 'playwright';

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });

  await page.goto('http://localhost:5173/');
  await page.waitForTimeout(2000);
  await page.screenshot({ path: '/tmp/ui-10-landing.png', fullPage: true });

  // dump current DOM
  const html = await page.evaluate(() => document.body.innerHTML.substring(0, 3000));
  console.log('=== Landing page HTML ===');
  console.log(html);

  // check URL
  console.log('\n=== Current URL ===');
  console.log(page.url());

  // try to find form elements
  const inputs = await page.evaluate(() => {
    const els = document.querySelectorAll('input, button, form');
    return Array.from(els).map(e => ({
      tag: e.tagName,
      type: e.type,
      name: e.name,
      placeholder: e.placeholder,
      text: e.textContent?.substring(0, 50),
      className: e.className?.substring(0, 80),
    }));
  });
  console.log('\n=== Form elements ===');
  console.log(JSON.stringify(inputs, null, 2));

  // Try login
  const usernameInput = page.locator('input').first();
  if (await usernameInput.isVisible()) {
    await usernameInput.fill('admin');
    const pwdInput = page.locator('input[type="password"]').first();
    if (await pwdInput.isVisible()) {
      await pwdInput.fill('admin');
    }
    // click submit button
    const btn = page.locator('button').last();
    await btn.click();
    await page.waitForTimeout(3000);
    console.log('\n=== URL after login attempt ===');
    console.log(page.url());
    await page.screenshot({ path: '/tmp/ui-11-after-login.png', fullPage: true });
    
    const afterHtml = await page.evaluate(() => document.body.innerHTML.substring(0, 3000));
    console.log('\n=== After login HTML ===');
    console.log(afterHtml);
  }

  await browser.close();
})();
