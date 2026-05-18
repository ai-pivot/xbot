import { chromium } from 'playwright';

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });

  await page.route('**/api/**', (route) => {
    const url = route.request().url();
    if (url.includes('/api/history')) {
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          ok: true,
          messages: [
            { id: 1, role: 'user', content: '1111111111111111111111111111111111111111111111111111111111111111111111111111', created_at: '2026-05-18T12:00:00Z' },
            { id: 2, role: 'assistant', content: '收到', created_at: '2026-05-18T12:00:01Z' },
            { id: 3, role: 'user', content: '短消息', created_at: '2026-05-18T12:00:02Z' },
            { id: 4, role: 'assistant', content: 'OK', created_at: '2026-05-18T12:00:03Z' },
            { id: 5, role: 'user', content: '正常中文消息，看看换行效果如何。', created_at: '2026-05-18T12:00:04Z' },
            { id: 6, role: 'assistant', content: '看起来不错。', created_at: '2026-05-18T12:00:05Z' },
          ],
        }),
      });
    } else {
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({}) });
    }
  });

  await page.goto('http://localhost:5173/', { waitUntil: 'networkidle' });
  await page.waitForTimeout(2000);
  await page.screenshot({ path: '/tmp/ui-30-final.png', fullPage: true });

  // Check all blue bubbles
  const bubbles = await page.evaluate(() => {
    return Array.from(document.querySelectorAll('.bg-blue-600')).map(b => {
      const rect = b.getBoundingClientRect();
      const parent = b.closest('.max-w-\\[80\\%\\]') || b.parentElement?.parentElement;
      const parentRect = parent ? parent.getBoundingClientRect() : null;
      const viewportW = window.innerWidth;
      return {
        text: b.textContent?.substring(0, 50),
        bubbleW: Math.round(rect.width),
        bubbleR: Math.round(rect.right),
        parentW: parentRect ? Math.round(parentRect.width) : null,
        viewportW,
        overflows: rect.right > viewportW,
      };
    });
  });

  console.log('\n📊 Bubble overflow check:');
  for (const b of bubbles) {
    console.log(`  ${b.overflows ? '🔴 OVERFLOW' : '✅ OK'} | bubbleW=${b.bubbleW} right=${b.bubbleR} parentW=${b.parentW} viewport=${b.viewportW} | "${b.text}"`);
  }

  await browser.close();
})();
