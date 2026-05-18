import { chromium } from 'playwright';
import { readFileSync } from 'fs';

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });

  // Intercept API calls to bypass auth
  await page.route('**/api/**', (route) => {
    const url = route.request().url();
    console.log(`  Intercepted: ${url}`);
    
    if (url.includes('/api/history')) {
      // Return fake messages to test UI rendering
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          ok: true,
          messages: [
            { id: 1, role: 'user', content: '你好，这是一条测试消息', created_at: '2026-05-18T12:00:00Z' },
            { id: 2, role: 'assistant', content: '你好！我是 xbot 智能助手。\n\n这是一段**Markdown**回复，包含：\n\n- 列表项 1\n- 列表项 2\n\n```python\ndef hello():\n    print("Hello, World!")\n```\n\n以及一些普通文字和一个很长的 URL 测试：https://example.com/very/long/path/that/should/wrap/properly/and/not/overflow/the/container/boundary/at/all\n\n> 引用块测试', created_at: '2026-05-18T12:00:01Z' },
            { id: 3, role: 'user', content: '短消息', created_at: '2026-05-18T12:00:02Z' },
            { id: 4, role: 'assistant', content: '收到！', created_at: '2026-05-18T12:00:03Z' },
            { id: 5, role: 'user', content: '这条消息包含一个很长的英文单词 supercalifragilisticexpialidocious 和一个超长连续字符 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 以及一个长 URL https://github.com/ai-pivot/xbot/pull/66/commits/abcdefghijk1234567890', created_at: '2026-05-18T12:00:04Z' },
            { id: 6, role: 'assistant', content: '好的，我来测试各种边界情况。', created_at: '2026-05-18T12:00:05Z' },
          ],
        }),
      });
    } else if (url.includes('/api/settings')) {
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({}) });
    } else if (url.includes('/api/presets')) {
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([]) });
    } else {
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({}) });
    }
  });

  await page.goto('http://localhost:5173/', { waitUntil: 'networkidle' });
  await page.waitForTimeout(2000);
  
  // Screenshot 1: Full page with messages
  await page.screenshot({ path: '/tmp/ui-20-messages.png', fullPage: true });
  console.log('✅ Screenshot saved: ui-20-messages.png');

  // Analyze DOM structure and layout
  const analysis = await page.evaluate(() => {
    const results = [];
    
    // Find all message elements
    const allDivs = document.querySelectorAll('div');
    const msgDivs = Array.from(allDivs).filter(d => d.getAttribute('data-msg-id') || d.getAttribute('data-index'));
    
    for (const el of msgDivs) {
      const rect = el.getBoundingClientRect();
      const parent = el.parentElement;
      const parentRect = parent ? parent.getBoundingClientRect() : null;
      const viewportW = window.innerWidth;
      
      results.push({
        className: el.className?.substring(0, 120),
        width: Math.round(rect.width),
        right: Math.round(rect.right),
        viewportWidth: viewportW,
        overflowsViewport: rect.right > viewportW,
        parentWidth: parentRect ? Math.round(parentRect.width) : null,
        overflowsParent: parentRect ? rect.right > parentRect.right + 2 : null,
        text: el.textContent?.substring(0, 80),
      });
    }
    return results;
  });
  
  console.log('\n📊 DOM Layout Analysis:');
  for (const item of analysis) {
    console.log(`  ${item.overflowsViewport ? '🔴 OVERFLOW' : '✅ OK'} | width=${item.width} right=${item.right} viewport=${item.viewportWidth} parentW=${item.parentWidth} | ${item.text?.substring(0, 50)}`);
    console.log(`    class: ${item.className}`);
  }

  // Check specific CSS computed styles on user message bubble
  const bubbleStyles = await page.evaluate(() => {
    const bubbles = document.querySelectorAll('.bg-blue-600');
    return Array.from(bubbles).map(b => {
      const style = getComputedStyle(b);
      const rect = b.getBoundingClientRect();
      const parent = b.parentElement;
      const parentRect = parent ? parent.getBoundingClientRect() : null;
      return {
        width: Math.round(rect.width),
        maxWidth: style.maxWidth,
        overflowWrap: style.overflowWrap,
        wordBreak: style.wordBreak,
        overflow: style.overflow,
        right: Math.round(rect.right),
        parentWidth: parentRect ? Math.round(parentRect.width) : null,
        overflowsParent: parentRect ? rect.right > parentRect.right + 2 : null,
        text: b.textContent?.substring(0, 60),
      };
    });
  });
  
  console.log('\n🔵 User Bubble Styles:');
  for (const s of bubbleStyles) {
    console.log(`  ${s.overflowsParent ? '🔴 OVERFLOW' : '✅ OK'} | w=${s.width} maxW=${s.maxWidth} overflow=${s.overflow} wrap=${s.overflowWrap} break=${s.wordBreak} | ${s.text}`);
  }

  await browser.close();
})();
