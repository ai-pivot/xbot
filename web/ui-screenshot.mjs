import { chromium } from 'playwright';

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });

  // 1. 先截图登录页
  await page.goto('http://localhost:5173/');
  await page.waitForTimeout(2000);
  await page.screenshot({ path: '/tmp/ui-01-login.png', fullPage: true });
  console.log('✅ Login page screenshot saved');

  // 2. 登录（尝试用默认凭据）
  const usernameInput = page.locator('input[type="text"], input[placeholder*="用户"], input[placeholder*="user"]').first();
  const passwordInput = page.locator('input[type="password"]').first();
  const submitBtn = page.locator('button[type="submit"], button:has-text("登录"), button:has-text("Login")').first();

  if (await usernameInput.isVisible()) {
    await usernameInput.fill('admin');
    await passwordInput.fill('admin');
    await submitBtn.click();
    await page.waitForTimeout(3000);
    await page.screenshot({ path: '/tmp/ui-02-after-login.png', fullPage: true });
    console.log('✅ After login screenshot saved');
  }

  // 3. 发送一条消息看用户气泡
  const editor = page.locator('.tiptap-editor, .ProseMirror').first();
  if (await editor.isVisible()) {
    await editor.click();
    await page.keyboard.type('这是一条测试消息，用来测试用户消息气泡的显示效果。This is a test message for checking user message bubble rendering. abcdefghijklmnopqrstuvwxyz0123456789');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(3000);
    await page.screenshot({ path: '/tmp/ui-03-user-msg.png', fullPage: true });
    console.log('✅ User message screenshot saved');
  }

  // 4. 等助手回复
  await page.waitForTimeout(5000);
  await page.screenshot({ path: '/tmp/ui-04-assistant-reply.png', fullPage: true });
  console.log('✅ Assistant reply screenshot saved');

  // 5. 检查页面 DOM 结构（用户消息区域）
  const userMsgHtml = await page.evaluate(() => {
    const msgs = document.querySelectorAll('[data-msg-id]');
    const results = [];
    for (const m of msgs) {
      const rect = m.getBoundingClientRect();
      const parent = m.parentElement;
      const parentRect = parent ? parent.getBoundingClientRect() : null;
      results.push({
        tag: m.tagName,
        classes: m.className,
        width: rect.width,
        height: rect.height,
        left: rect.left,
        right: rect.right,
        parentWidth: parentRect?.width,
        parentClasses: parent?.className,
        overflowsParent: parentRect ? rect.right > parentRect.right : null,
        innerHtml: m.innerHTML?.substring(0, 200),
      });
    }
    return results;
  });
  console.log('\n📊 DOM analysis:');
  console.log(JSON.stringify(userMsgHtml, null, 2));

  // 6. 检查所有消息容器的布局
  const layoutInfo = await page.evaluate(() => {
    const container = document.querySelector('.chat-messages');
    if (!container) return { error: 'no .chat-messages found' };
    const rect = container.getBoundingClientRect();
    return {
      containerWidth: rect.width,
      containerClasses: container.className,
    };
  });
  console.log('\n📐 Layout info:');
  console.log(JSON.stringify(layoutInfo, null, 2));

  await browser.close();
})();
