---
title: "xbot"
weight: 0
geekdocHidden: true
---

<div class="xb-landing">

<div class="xb-hero" markdown="0">
  <span class="xb-hero__badge"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-plug-zap"></use></svg> 自托管 · 一个 Agent，全渠道</span>
  <h1 class="xb-hero__title">把 AI Agent 部署到<br>你自己的服务器上</h1>
  <p class="xb-hero__sub">
    配置一次，全团队通过 <strong>飞书 / QQ / 浏览器 / 终端</strong> 与同一个 Agent 对话。
    它能调用工具、读写文件、跑命令、委派子 Agent —— 数据始终不出你的服务器。
  </p>
  <div class="xb-hero__cta">
    <a class="xb-btn xb-btn--primary" href="/zh-cn/getting-started/">快速开始 →</a>
    <a class="xb-btn xb-btn--ghost" href="https://github.com/ai-pivot/xbot">GitHub</a>
  </div>

  <div class="xb-install">
    <div class="xb-install__head">
      <span class="xb-install__badge"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-clipboard-copy"></use></svg> 把以下指令发给你的 Agent</span>
      <span class="xb-install__hint">Claude Code · Codex · Cursor …</span>
    </div>
    <pre class="xb-install__cmd"><code>帮我在这台机器上安装并启动 xbot：
curl -fsSL https://raw.githubusercontent.com/<wbr>ai-pivot/<wbr>xbot/<wbr>master/<wbr>scripts/install.sh | bash</code></pre>
    <div class="xb-install__foot">
      <button type="button" class="xb-install__copy" data-copied="已复制 ✓">复制指令</button>
      <span class="xb-install__note">
        一条命令装好 Web UI + 全部内置插件 ·
        <a href="/zh-cn/installation/">其他安装方式</a>
      </span>
    </div>
  </div>

  <div class="xb-shot">
    <img src="/img/app/hero.png" alt="xbot Web 界面：多会话侧边栏、工具调用与迭代进度">
  </div>
  <p class="xb-shot__caption">
    真实会话：多轮对话 · 工具调用（Grep / FileReplace / Shell）· 逐迭代思考过程 · 多会话并行
  </p>
</div>

<h2 class="xb-section-title">为什么选 xbot？</h2>
<p class="xb-section-sub">大多数 AI 编程 Agent 只活在终端里。xbot 不一样 —— 一个 Agent，全渠道。</p>

<div class="xb-compare" markdown="1">

| | **xbot** | Codex / Claude Code / OpenCode |
|--|------|-------------------------------|
| **多渠道** | 飞书 · QQ · Web · CLI | 仅终端 |
| **团队共享 LLM** | 管理员配一次，所有人用 | 各自配置 API Key |
| **自托管** | <strong class="xb-yes">✓</strong> 数据不出服务器 | <strong class="xb-yes">✓</strong> |
| **飞书集成** | 文档、多维表格、云盘、卡片 | <span class="xb-no">—</span> |
| **子 Agent + 群聊** | 委派、并行、辩论 | 仅子 Agent |
| **插件系统** | 工具、hooks、widget、渠道插件 | 有限 |

</div>

{{< hint type=important >}}
**最常见场景**：部署 Server 模式 → 连接飞书应用 → 全团队在群里 @机器人对话，无需各自配置 API Key。
{{< /hint >}}

<h2 class="xb-section-title">核心特性</h2>
<p class="xb-section-sub">开箱即用的 Agent 能力，以及为生产部署准备的工程细节。</p>

<div class="xb-grid">
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-brain-circuit"></use></svg></div>
    <div class="xb-card__title">多轮对话 + 工具调用</div>
    <p class="xb-card__desc">Shell、文件读写、网页搜索、定时任务、子 Agent 委派，逐迭代可见思考与工具结果。</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-messages-square"></use></svg></div>
    <div class="xb-card__title">多渠道接入</div>
    <p class="xb-card__desc">同一个 Agent，飞书 / QQ / 终端 / 浏览器不同入口，会话与上下文互不串扰。</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-key-round"></use></svg></div>
    <div class="xb-card__title">团队共享 LLM 订阅</div>
    <p class="xb-card__desc">管理员配置一次，全团队直接使用；支持多订阅、按模型切换与上下文配额。</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-mouse-pointer-2"></use></svg></div>
    <div class="xb-card__title">全功能 TUI</div>
    <p class="xb-card__desc">鼠标交互、命令面板 (Ctrl+K)、多会话侧边栏、主题系统。</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-house"></use></svg></div>
    <div class="xb-card__title">完全自托管</div>
    <p class="xb-card__desc">一条命令安装，数据不出你的服务器；SQLite 单文件存储，备份即拷贝。</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-puzzle"></use></svg></div>
    <div class="xb-card__title">可扩展</div>
    <p class="xb-card__desc">Skills、SubAgents、MCP 协议、插件系统（工具 / hooks / widget / 渠道）。</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-bot"></use></svg></div>
    <div class="xb-card__title">AI-Native 配置</div>
    <p class="xb-card__desc">Agent 可通过 <code>config</code> 与 <code>tui_control</code> 工具自行调整配置和界面。</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-plug-zap"></use></svg></div>
    <div class="xb-card__title">插件分享能力</div>
    <p class="xb-card__desc">插件可产出可分享的面板，生成公开链接（仅主动分享的内容才有 token）。</p>
  </div>
</div>

<h2 class="xb-section-title">我该用哪个渠道</h2>
<p class="xb-section-sub">同一个 Agent 内核，四种接入方式，按你的场景挑一个即可。</p>

<div class="xb-channels">
  <div class="xb-channel">
    <div class="xb-channel__name"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-terminal"></use></svg> CLI</div>
    <p class="xb-channel__desc">开发者与终端用户：全功能 TUI，流式输出，工具调用。</p>
  </div>
  <div class="xb-channel">
    <div class="xb-channel__name"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-feather"></use></svg> 飞书</div>
    <p class="xb-channel__desc">团队协作：群里 @机器人 对话，支持消息卡片与文档集成。</p>
  </div>
  <div class="xb-channel">
    <div class="xb-channel__name"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-globe"></use></svg> Web</div>
    <p class="xb-channel__desc">任何有浏览器的人：注册 / 登录，邀请制，移动端自适应。</p>
  </div>
  <div class="xb-channel">
    <div class="xb-channel__name"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-message-circle"></use></svg> QQ</div>
    <p class="xb-channel__desc">个人或小圈子：QQ 聊天窗口交互（via NapCat）。</p>
  </div>
</div>

<h2 class="xb-section-title">其他安装方式</h2>
<p class="xb-section-sub">
Windows：<code>irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex</code>
&nbsp;·&nbsp;
中国大陆走镜像：<code>curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash</code>
</p>
<p class="xb-section-sub">
装完打开 <code>http://localhost:8082</code> → 注册账号 → 齿轮 → LLM → 填 Base URL / API Key / 选模型 → 开聊。
可选参数（默认值通常就是你要的）与全部配置项 → <a href="/zh-cn/installation/">安装与配置</a>。
</p>
<p class="xb-section-sub">
给 AI agent 的可执行安装手册（每步有断言、含无头配 LLM 与排错表）→ <a href="/zh-cn/agent-install/">Agent 安装手册</a>。
安装脚本跑完会把这份文档的地址打出来，所以把上面那段指令发给 agent 之后，它会自己知道下一步读什么。
</p>

<h2 class="xb-section-title">架构</h2>
<p class="xb-section-sub">Backend 是纯 RPC 客户端接口（零业务逻辑），Transport 负责实际执行。</p>

<div class="xb-arch" markdown="0">
  <div class="xb-arch__row">
    <div class="xb-arch__node">
      <span class="xb-arch__node-title">飞书 · QQ · Web · CLI</span>
      <span class="xb-arch__node-sub">channels</span>
    </div>
    <span class="xb-arch__link" aria-hidden="true"></span>
    <div class="xb-arch__node">
      <span class="xb-arch__node-title">Dispatcher</span>
      <span class="xb-arch__node-sub">channel/</span>
    </div>
    <span class="xb-arch__link" aria-hidden="true"></span>
    <div class="xb-arch__node xb-arch__node--accent">
      <span class="xb-arch__node-title">Agent Loop</span>
      <span class="xb-arch__node-sub">agent/ · Transport</span>
    </div>
    <span class="xb-arch__link" aria-hidden="true"></span>
    <div class="xb-arch__node">
      <span class="xb-arch__node-title">LLM</span>
      <span class="xb-arch__node-sub">llm/</span>
    </div>
  </div>
  <div class="xb-arch__leaves">
    <span>Agent Loop 调用 →</span>
    <code>tools/</code>
    <code>memory/</code>
    <code>skills/</code>
    <code>plugins/</code>
  </div>
</div>

阅读完整 [架构概览](/zh-cn/architecture/)。

<h2 class="xb-section-title">社区</h2>
<p class="xb-section-sub">遇到问题、想要新功能，或者只是想聊聊。</p>

<div class="xb-links">
  <a class="xb-link" href="/zh-cn/getting-started/">
    <span class="xb-link__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-book-open"></use></svg></span>
    <span class="xb-link__text">文档<span class="xb-link__desc">完整指南和参考</span></span>
  </a>
  <a class="xb-link" href="https://github.com/ai-pivot/xbot/issues">
    <span class="xb-link__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-bug"></use></svg></span>
    <span class="xb-link__text">GitHub Issues<span class="xb-link__desc">报告 bug 或请求功能</span></span>
  </a>
  <a class="xb-link" href="https://github.com/ai-pivot/xbot/discussions">
    <span class="xb-link__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-message-circle"></use></svg></span>
    <span class="xb-link__text">Discussions<span class="xb-link__desc">提问和分享想法</span></span>
  </a>
  <a class="xb-link" href="https://github.com/ai-pivot/xbot/releases">
    <span class="xb-link__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-package"></use></svg></span>
    <span class="xb-link__text">Releases<span class="xb-link__desc">下载最新版本</span></span>
  </a>
  <a class="xb-link" href="https://github.com/ai-pivot/xbot/blob/master/CHANGELOG.md">
    <span class="xb-link__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-file-text"></use></svg></span>
    <span class="xb-link__text">更新日志<span class="xb-link__desc">最新变化</span></span>
  </a>
  <a class="xb-link" href="/zh-cn/development/">
    <span class="xb-link__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-git-pull-request"></use></svg></span>
    <span class="xb-link__text">贡献指南<span class="xb-link__desc">如何参与贡献</span></span>
  </a>
</div>

</div>
