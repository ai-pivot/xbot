---
title: "xbot"
weight: 0
geekdocHidden: true
---

<div class="xb-landing">

<div class="xb-hero" markdown="0">
  <span class="xb-hero__badge"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-plug-zap"></use></svg> Self-hosted · One agent, every channel</span>
  <h1 class="xb-hero__title">Put your AI agent<br>on your own server</h1>
  <p class="xb-hero__sub">
    Configure it once and the whole team talks to the same agent through
    <strong>Feishu / QQ / browser / terminal</strong>. It calls tools, reads and edits files,
    runs commands and delegates sub-agents — your data never leaves your host.
  </p>
  <div class="xb-hero__cta">
    <a class="xb-btn xb-btn--primary" href="/getting-started/">Get started →</a>
    <a class="xb-btn xb-btn--ghost" href="https://github.com/ai-pivot/xbot">GitHub</a>
  </div>

  <div class="xb-install">
    <div class="xb-install__head">
      <span class="xb-install__badge"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-clipboard-copy"></use></svg> Paste this prompt into your agent</span>
      <span class="xb-install__hint">Claude Code · Codex · Cursor …</span>
    </div>
    <pre class="xb-install__cmd"><code>Install and start xbot on this machine:
curl -fsSL https://raw.githubusercontent.com/<wbr>ai-pivot/<wbr>xbot/<wbr>master/<wbr>scripts/install.sh | bash</code></pre>
    <div class="xb-install__foot">
      <button type="button" class="xb-install__copy" data-copied="Copied ✓">Copy prompt</button>
      <span class="xb-install__note">
        One command: Web UI + every built-in plugin ·
        <a href="/installation/">Other ways to install</a>
      </span>
    </div>
  </div>

  <div class="xb-shot">
    <img src="/img/app/hero.png" alt="xbot web UI: multi-session sidebar with tool calls and iteration progress">
  </div>
  <p class="xb-shot__caption">
    A real session: multi-turn chat · tool calls (Grep / FileReplace / Shell) · per-iteration reasoning · parallel sessions
  </p>
</div>

<h2 class="xb-section-title">Why xbot?</h2>
<p class="xb-section-sub">Most AI coding agents live in a terminal. xbot doesn't — one agent, every channel.</p>

<div class="xb-compare" markdown="1">

| | **xbot** | Codex / Claude Code / OpenCode |
|--|------|-------------------------------|
| **Multi-channel** | Feishu · QQ · Web · CLI | Terminal only |
| **Shared team LLM** | Admin configures once, everyone uses it | Everyone brings their own API key |
| **Self-hosted** | <strong class="xb-yes">✓</strong> Data never leaves your server | <strong class="xb-yes">✓</strong> |
| **Feishu integration** | Docs, Base, Drive, interactive cards | <span class="xb-no">—</span> |
| **Sub-agents + group chat** | Delegate, parallelize, debate | Sub-agents only |
| **Plugin system** | Tools, hooks, widgets, channel plugins | Limited |

</div>

{{< hint type=important >}}
**The most common setup**: run in server mode → connect a Feishu app → the whole team chats with the bot in a group, no individual API keys required.
{{< /hint >}}

<h2 class="xb-section-title">Core features</h2>
<p class="xb-section-sub">Agent capabilities that work out of the box, plus the engineering details production deployments need.</p>

<div class="xb-grid">
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-brain-circuit"></use></svg></div>
    <div class="xb-card__title">Multi-turn chat + tool calling</div>
    <p class="xb-card__desc">Shell, file I/O, web search, cron jobs and sub-agent delegation — with per-iteration reasoning and tool results.</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-messages-square"></use></svg></div>
    <div class="xb-card__title">Every channel</div>
    <p class="xb-card__desc">The same agent over Feishu / QQ / terminal / browser, with per-session isolation.</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-key-round"></use></svg></div>
    <div class="xb-card__title">Shared LLM subscriptions</div>
    <p class="xb-card__desc">Configured once by an admin, used by the whole team — multiple subscriptions, per-model switching and context quotas.</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-mouse-pointer-2"></use></svg></div>
    <div class="xb-card__title">Full-featured TUI</div>
    <p class="xb-card__desc">Mouse support, command palette (Ctrl+K), multi-session sidebar and a theming system.</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-house"></use></svg></div>
    <div class="xb-card__title">Truly self-hosted</div>
    <p class="xb-card__desc">One command to install. SQLite single-file storage — a backup is a file copy.</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-puzzle"></use></svg></div>
    <div class="xb-card__title">Extensible</div>
    <p class="xb-card__desc">Skills, sub-agents, the MCP protocol and a plugin system (tools / hooks / widgets / channels).</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-bot"></use></svg></div>
    <div class="xb-card__title">AI-native configuration</div>
    <p class="xb-card__desc">The agent can reconfigure itself and its UI through the <code>config</code> and <code>tui_control</code> tools.</p>
  </div>
  <div class="xb-card">
    <div class="xb-card__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-plug-zap"></use></svg></div>
    <div class="xb-card__title">Shareable plugin panels</div>
    <p class="xb-card__desc">Plugins can produce shareable panels and mint public links — only explicitly shared content gets a token.</p>
  </div>
</div>

<h2 class="xb-section-title">Which channel should I use?</h2>
<p class="xb-section-sub">One agent core, four ways in — pick whatever fits your workflow.</p>

<div class="xb-channels">
  <div class="xb-channel">
    <div class="xb-channel__name"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-terminal"></use></svg> CLI</div>
    <p class="xb-channel__desc">For developers: full TUI, streaming output, tool calls.</p>
  </div>
  <div class="xb-channel">
    <div class="xb-channel__name"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-feather"></use></svg> Feishu</div>
    <p class="xb-channel__desc">For teams: chat in a group, interactive cards and doc integration.</p>
  </div>
  <div class="xb-channel">
    <div class="xb-channel__name"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-globe"></use></svg> Web</div>
    <p class="xb-channel__desc">For anyone with a browser: sign-up / login, invite-only, mobile-friendly.</p>
  </div>
  <div class="xb-channel">
    <div class="xb-channel__name"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-message-circle"></use></svg> QQ</div>
    <p class="xb-channel__desc">For individuals and small groups, via NapCat.</p>
  </div>
</div>

<h2 class="xb-section-title">Other ways to install</h2>
<p class="xb-section-sub">
Windows: <code>irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex</code>
&nbsp;·&nbsp;
Behind the GFW: <code>curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash</code>
</p>
<p class="xb-section-sub">
Then open <code>http://localhost:8082</code> → create an account → gear icon → LLM → base URL / API key / pick a model → chat.
Optional flags (the defaults are usually what you want) and every config key → <a href="/installation/">Install &amp; configure</a>.
</p>

<h2 class="xb-section-title">Architecture</h2>
<p class="xb-section-sub">The backend is a pure RPC client interface (zero business logic); the transport layer does the real work.</p>

<div class="xb-arch" markdown="0">
  <div class="xb-arch__row">
    <div class="xb-arch__node">
      <span class="xb-arch__node-title">Feishu · QQ · Web · CLI</span>
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
    <span>Agent Loop calls →</span>
    <code>tools/</code>
    <code>memory/</code>
    <code>skills/</code>
    <code>plugins/</code>
  </div>
</div>

Read the full [architecture overview](/architecture/).

<h2 class="xb-section-title">Community</h2>
<p class="xb-section-sub">Questions, feature requests, or just want to chat.</p>

<div class="xb-links">
  <a class="xb-link" href="/getting-started/">
    <span class="xb-link__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-book-open"></use></svg></span>
    <span class="xb-link__text">Documentation<span class="xb-link__desc">Guides and reference</span></span>
  </a>
  <a class="xb-link" href="https://github.com/ai-pivot/xbot/issues">
    <span class="xb-link__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-bug"></use></svg></span>
    <span class="xb-link__text">GitHub Issues<span class="xb-link__desc">Report bugs or request features</span></span>
  </a>
  <a class="xb-link" href="https://github.com/ai-pivot/xbot/discussions">
    <span class="xb-link__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-message-circle"></use></svg></span>
    <span class="xb-link__text">Discussions<span class="xb-link__desc">Ask questions and share ideas</span></span>
  </a>
  <a class="xb-link" href="https://github.com/ai-pivot/xbot/releases">
    <span class="xb-link__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-package"></use></svg></span>
    <span class="xb-link__text">Releases<span class="xb-link__desc">Download the latest version</span></span>
  </a>
  <a class="xb-link" href="https://github.com/ai-pivot/xbot/blob/master/CHANGELOG.md">
    <span class="xb-link__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-file-text"></use></svg></span>
    <span class="xb-link__text">Changelog<span class="xb-link__desc">What's new</span></span>
  </a>
  <a class="xb-link" href="/development/">
    <span class="xb-link__icon"><svg class="xb-ico" aria-hidden="true"><use href="/icons.svg#i-git-pull-request"></use></svg></span>
    <span class="xb-link__text">Contributing<span class="xb-link__desc">How to get involved</span></span>
  </a>
</div>

</div>
