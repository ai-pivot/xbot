import { chromium } from 'playwright';

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });

  // Don't mock anything - just go to the real page
  await page.goto('http://localhost:5173/', { waitUntil: 'networkidle', timeout: 10000 }).catch(() => {});
  await page.waitForTimeout(3000);
  await page.screenshot({ path: '/tmp/ui-real-01.png', fullPage: true });

  // Try logging in with admin/admin
  const userBtn = page.locator('button[type="submit"]').first();
  if (await userBtn.isVisible()) {
    // Check if we're on login form
    const header = await page.locator('h2').first().textContent().catch(() => '');
    console.log('Page header:', header);
    
    // Fill username & password
    const inputs = page.locator('input');
    const count = await inputs.count();
    for (let i = 0; i < count; i++) {
      const type = await inputs.nth(i).getAttribute('type');
      if (type === 'text') await inputs.nth(i).fill('admin');
      if (type === 'password') await inputs.nth(i).fill('admin');
    }
    await userBtn.click();
    await page.waitForTimeout(3000);
    await page.screenshot({ path: '/tmp/ui-real-02-loggedin.png', fullPage: true });
    console.log('URL after login:', page.url());
    
    // Type a test message
    const editor = page.locator('.ProseMirror').first();
    if (await editor.isVisible().catch(() => false)) {
      await editor.click();
      await page.keyboard.type('1111111111111111111111111111111111111111');
      await page.keyboard.press('Enter');
      await page.waitForTimeout(5000);
      await page.screenshot({ path: '/tmp/ui-real-03-msg.png', fullPage: true });
      
      // Dump the actual user message bubble HTML and computed styles
      const info = await page.evaluate(() => {
        const blueDivs = document.querySelectorAll('.bg-blue-600');
        return Array.from(blueDivs).map(el => {
          const s = getComputedStyle(el);
          const r = el.getBoundingClientRect();
          return {
            html: el.outerHTML.substring(0, 300),
            width: r.width,
            right: r.right,
            viewport: window.innerWidth,
            overflowWrap: s.overflowWrap,
            wordBreak: s.wordBreak,
            widthCSS: s.width,
            maxWidth: s.maxWidth,
            display: s.display,
            overflow: s.overflow,
          };
        });
      });
      console.log('\n📊 Actual bubble data:');
      info.forEach((b, i) => {
        console.log(`\n--- Bubble ${i} ---`);
        console.log('HTML:', b.html);
        console.log(`width=${b.width} right=${b.right} viewport=${b.viewport}`);
        console.log(`CSS: width=${b.widthCSS} maxW=${b.maxWidth} overflow-wrap=${b.overflowWrap} word-break=${b.wordBreak} overflow=${b.overflow}`);
        console.log(`Overflows: ${b.right > b.viewport}`);
      });
    }
  }

  await browser.close();
})();
