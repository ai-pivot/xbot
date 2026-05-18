import { chromium } from 'playwright';

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });

  // Mock API
  await page.route('**/api/**', (route) => {
    const url = route.request().url();
    if (url.includes('/api/history')) {
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          ok: true,
          processing: false,
          last_seq: 10,
          messages: [
            { id: 1, role: 'user', content: '你好', created_at: '2026-05-18T12:00:00Z' },
            { id: 2, role: 'assistant', content: '你好！我是AI助手，有什么可以帮你？', created_at: '2026-05-18T12:00:01Z', detail: null, tool_calls: null, display_only: 0 },
            { id: 3, role: 'user', content: '第二个问题', created_at: '2026-05-18T12:01:00Z' },
            { id: 4, role: 'assistant', content: '这是第二个回答。\n\n```python\nprint("hello")\n```\n\n一些**粗体**文字。', created_at: '2026-05-18T12:01:01Z', detail: null, tool_calls: null, display_only: 0 },
          ],
        }),
      });
    } else if (url.includes('/api/chats')) {
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true, chats: [{ chat_id: 'default', label: '默认会话', last_active: '', preview: '', is_current: true }] }) });
    } else if (url.includes('/api/context-info')) {
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true, prompt_tokens: 100, max_tokens: 128000, usage_pct: 0.1, source: 'api' }) });
    } else {
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true }) });
    }
  });

  await page.goto('http://localhost:5173/', { waitUntil: 'networkidle' });
  await page.waitForTimeout(2000);

  // Check DOM for messages
  const domInfo = await page.evaluate(() => {
    const allText = document.body.innerText;
    const userMsgs = document.querySelectorAll('[data-msg-id]');
    const assistantTurns = document.querySelectorAll('[data-testid="assistant-turn"]');
    const virtualItems = document.querySelectorAll('[data-index]');
    
    return {
      bodyTextPreview: allText.substring(0, 500),
      userMsgCount: userMsgs.length,
      assistantTurnCount: assistantTurns.length,
      virtualItemCount: virtualItems.length,
      virtualItemIndices: Array.from(virtualItems).map(el => el.getAttribute('data-index')),
      // Check for loading state
      hasBouncingDots: allText.includes('Preparing') || allText.includes('准备中'),
      hasThinkingOrb: !!document.querySelector('.thinking-orb'),
      // Check message content visibility
      visibleText: allText.includes('你好！') || allText.includes('第二个回答'),
    };
  });

  console.log('\n📊 DOM Analysis:');
  console.log(JSON.stringify(domInfo, null, 2));

  // Screenshot
  await page.screenshot({ path: '/tmp/ui-debug-assistant.png', fullPage: true });
  console.log('\n📸 Screenshot saved to /tmp/ui-debug-assistant.png');

  // Check virtual list container height
  const virtInfo = await page.evaluate(() => {
    const container = document.querySelector('[style*="position: relative"]');
    if (!container) return { error: 'no virtual container found' };
    const style = getComputedStyle(container);
    const children = container.children;
    const childInfo = Array.from(children).map(c => ({
      tag: c.tagName,
      dataIdx: c.getAttribute('data-index'),
      width: c.getBoundingClientRect().width,
      height: c.getBoundingClientRect().height,
      text: c.textContent?.substring(0, 80),
    }));
    return {
      containerHeight: style.height,
      childCount: children.length,
      children: childInfo,
    };
  });
  console.log('\n📐 Virtual list:');
  console.log(JSON.stringify(virtInfo, null, 2));

  await browser.close();
})();
