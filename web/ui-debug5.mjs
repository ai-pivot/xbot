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
          ok: true, processing: false, last_seq: 10,
          messages: [
            { id: 1, role: 'user', content: 'read main.go', created_at: '2026-05-18T12:00:00Z' },
            // Intermediate assistant with tool_calls, no detail → should be filtered
            { id: 2, role: 'assistant', content: '', tool_calls: '[{"id":"c1","name":"Read","arguments":"{}"}]', detail: null, display_only: 0, created_at: '2026-05-18T12:00:01Z' },
            // Tool result
            { id: 3, role: 'tool', content: 'file content here', tool_name: 'Read', created_at: '2026-05-18T12:00:02Z' },
            // Final assistant with content + detail (iteration history)
            { id: 4, role: 'assistant', content: 'I read the file. Here is main.go content:\n\n```\npackage main\n```', tool_calls: null, detail: '[{"iteration":1,"thinking":"need to read file","completed_tools":[{"name":"Read","status":"done","summary":"Read main.go"}]}]', display_only: 0, created_at: '2026-05-18T12:00:03Z' },
          ],
        }),
      });
    } else if (url.includes('/api/chats')) {
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true, chats: [{ chat_id: 'default', label: '默认会话', last_active: '', preview: '', is_current: true }] }) });
    } else {
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true }) });
    }
  });

  await page.goto('http://localhost:5173/', { waitUntil: 'networkidle' });
  await page.waitForTimeout(2000);

  const info = await page.evaluate(() => {
    const allText = document.body.innerText;
    const virtualItems = document.querySelectorAll('[data-index]');
    const assistantTurns = document.querySelectorAll('[data-testid="assistant-turn"]');
    return {
      hasFileContent: allText.includes('package main') || allText.includes('main.go'),
      virtualItemCount: virtualItems.length,
      assistantTurnCount: assistantTurns.length,
      bodyPreview: allText.substring(0, 800),
    };
  });
  console.log('\n📊 Result:');
  console.log(JSON.stringify(info, null, 2));
  await page.screenshot({ path: '/tmp/ui-debug-toolcall.png', fullPage: true });
  await browser.close();
})();
