# xbot

> Go AI Agent framework with message bus + plugin architecture. Supports Feishu/Web/CLI channels (QQ / NapCat are plugin-provided), tool calling, pluggable memory, skills, subagents, MCP integration.

## Quick Reference

- **⛔ 禁止直接 push 主分支（用户明确要求，2026-09-10；违反会被严厉批评）。** 任何改动一律走 **分支 + Pull Request**：
  `git checkout -b <fix|feat|chore>/<slug>` → commit → `git push origin <branch>` → `gh pr create --base master`。
  **绝不允许 `git push origin master`**（历史事故：agent 连续多轮直接 push master，绕过 review 与 CI 门禁）。合并交给用户/CI，agent 的职责是开 PR 并**确保 CI 全绿**。
- **⛔ 分支 / PR 纪律（用户明确要求，2026-09-24；违反会被严厉批评）：**
  - **开新分支、开 PR、合并 —— 必须先问用户**，没说就绝不动。一次任务**只开一个 PR**；修完一个 bug 后发现新问题（哪怕同会话、哪怕修好了）也**先停下来汇报**，等用户说"开 PR / 修掉它"再动 —— 2026-09-24 事故：用户只让排查"React #185 是什么"，agent 修完直接开了第二个 PR（#411），被用户严厉批评。
  - **Commit / Push 不用问**：只要不在 master，**已开分支上随便 commit、随便 push**（问"要不要 commit/push"是浪费用户时间）。需要请示的只有三件事：**开新分支、开 PR、合并**。
- Entry points: `cmd/xbot-cli/` (CLI), `cmd/runner/` (remote sandbox), `cmd/xbot/` (server)
- Build: `go build ./...` | Test: `go test ./...` | Lint: `golangci-lint run ./...`
- Config: `~/.xbot/config.json`, env var overrides
- Subscriptions: `~/.xbot/config.json` (CLI) or DB `user_llm_subscriptions` (Server) — the single source of truth for LLM config
- Pre-commit: gofmt → golangci-lint → go build → go test
- **插件分享能力（schema v65 `shared_artifacts`）是【通用宿主能力】，禁止任何 genui 定制逻辑（用户明确要求）。** 宿主只认 `plugin_id` + `content_type`（由产出插件命名）+ **对宿主不透明的 payload** —— 只有该插件注册的 `shareRenderer`（`ctx.share.registerRenderer({ kind:'shareRenderer', contentType, render })`）能解释它；公开页 `/s/:token` 按 contentType 派发（与 `messageRenderer` 同一模式：能力由插件**声明**）。插件侧 API：`ctx.share.create/list/revoke`（HTTP、需登录）+ `registerRenderer`；**分享入口是插件自己的 UI**（如 genui 的 `ShareablePanel` 自带分享按钮），核心不出现任何内容类型判断。**安全边界（用户明确要求「没分享的不能让外界无鉴权访问」）**：只有主动 `create`（**鉴权**）过的内容才有 token；公开端点仅 `GET /api/share/{token}`（按 token 读一条，**无列举、无按 ID 猜测**，未知/已撤销/已过期**统一 404**，不泄露 token 是否曾存在）；未分享内容根本不在表里 → 无鉴权路径不可达。token = 256-bit `crypto/rand`（在 `ShareService.Create` 内铸造，调用方不可提供），撤销/过期立即失效（`Cache-Control: no-store`）。前后端权限白名单必须同步：`plugin/permissions.go` 的 `PermShare` ↔ `web/src/plugin-api/manifest.ts` 的 `'share'`（`TestAllFrontendPermissionsRegistered` 守护）。**`module_url` 由 web 层从 serve 目录解析**（`WebChannel.pluginModuleURL` 读 plugin.json 的 `web.entry`），**绝不能门控在 plugin manager 上** —— `serverapp/server.go` 的 `SetPluginDirs` 曾误放在 `pm != nil` 分支内，导致插件系统关闭时公开分享页拿不到 `module_url`（分享链接打不开，2026-09-10 踩坑）。
- **本地 pre-commit hook（`.githooks/pre-commit`）**：对齐 CI 全部本地可执行检查（golangci-lint / go build / go test / 前端 eslint+vitest+build）。启用：`git config core.hooksPath .githooks`（每 clone 需执行一次，团队共享）。SKIP_RACE=1 跳过 race 检测（本地提速，CI 仍跑）；RUN_E2E=1 额外跑 Playwright E2E（默认跳过，耗时）。golangci-lint 须用**官方 release 二进制**（`curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin v2.10.1`）——`go install` 编译的版本内置 go1.25 会报「lower than targeted go 1.26」。
- **Deploy Docs 的 Hugo 必须用官方 release 二进制，禁止改回 `go install`**（2026-09-09 连续 3 次失败的根因）：`go install github.com/gohugoio/hugo@vX` 每次都经 `proxy.golang.org` 解析模块图 + `sum.golang.org` 校验**每一个**模块，后者会间歇性返回 `stream error: INTERNAL_ERROR` → job 在**解析任何 markdown 之前**就挂（重试无效；本地同一条命令 41s 成功 → 纯 CI 侧网络故障）。`.github/workflows/docs.yml` 现为：下载官方 release tarball（`curl --retry 5 --retry-all-errors`）→ `hugo_X_checksums.txt` 做 `sha256sum -c`（保留 sumdb 曾提供的供应链保证）→ `sudo tar -xzf ... -C /usr/local/bin hugo`。同时删掉 `setup-go`（docs-site 无 `go.mod`，主题资产走 npm）。
- **⚠️ 连续「带工具」的迭代必须【跨迭代折叠成一行】（2026-09-15 用户第三次真机复查后定稿，**取代**此前"禁止跨迭代合并工具"那条的适用范围）**：`IterationGroup` 是逐迭代组件，每个迭代各自渲染 `FoldedToolGroup tools={iteration.tools}` ⇒ **连续 N 个 tool-only 迭代 = N 个独立 pill 行**（用户现场：5 个 `task_status` / 7 个 `Shell` 一连串 tool-only 迭代 ⇒ 看起来"一个工具一行"，**与 CSS 宽度无关** —— 宽度/槽位修复只能让同一迭代内的多个 pill 同行，跨迭代永远合不了）。正解：`TurnBody.mergeToolRuns()` 把**连续、带工具**的迭代合并为一个渲染块 —— ⚠️ **头部迭代可以带 reasoning/content**（用户 2026-09-15 指出：我最初要求 run 内**全部**迭代 tool-only ⇒ 头部被排除、它的工具单独成行 ⇒ 截图里"`N 失败` chip + 失败 pill 在上、其余 pill 在下"的**真因，不是什么置顶逻辑**）；只有**后续成员**才要求 tool-only（`!content && !reasoning`），带文本的迭代仍是独立块（文本不合并、不丢） ⇒ pill 共享**同一个 wrap 行**（pill 各自独立、点击展开各自详情，信息不丢）；保留**首个迭代号**（高度缓存/窗口 key 稳定）+ `useMemo` 保引用稳定（不击穿 `CommittedTurn` 的 memo 与迭代级窗口化）。**注意区分**：被删的是"折叠级别/摘要行/flattenIterations"那套**旧格式**；这里折叠的是**工具行布局**（无摘要、无级别开关、每个 pill 仍独立可点）。另：`ROW_ROW_CLASS` **不能带 `w-full`**（它是 `flex-col` 里的兄弟 ⇒ 强行换行把 `N 失败` chip 顶成独立一行，用户「失败单独一行很丑」）⇒ 用 `min-w-0 flex-1` 让 chip 与 pill **同一视觉行内联**。E2E 判据（几何）：`[data-testid="tool-pill-row"]` 数量 ≤2（修复前 = 逐迭代数）且 pill 的 y 集合收敛；失败 chip 必须与**某个 pill 垂直重叠**（⚠️ 不能用"top 相等"：flex `items-center` 下不同高度元素 top 天然不同，实测 chip top=192 / 同行 pill top=202）。
- **设计原则「色彩只表达状态」（2026-09-15 用户：`FileReplace` 的琥珀黄名字"很容易让人觉得有 warn"）**：**黄/橙/红只属于失败与终止** —— pill 名称在**运行/生成中**保留**分类色**（需要注意力；sweep 动效契约不变），在**成功/排队/终止**改**中性前景色**（`var(--text-primary)`）；分类色只留在**左侧 3px 条 + 图标槽**。分类色板里"写入"由 `#fbbf24`（警示黄）→ `#14b8a6`（teal）。
- **⚠️ 部署后必须比对 hash 是否变化**（2026-09-15 我连续两轮误报"部署成功"）：`npm run build 2>&1 | tail -1` **管道吞掉退出码** ⇒ 构建失败时脚本照旧 `cp` **旧 dist**，served hash 不变；所以"已部署"的判据只能是 **`local=… served=…` 且 hash 与上一版不同**。本轮真凶正是一次**构建失败**（补 `useMemo` import 的检查只看了文件第 1 行 ⇒ `useMemo` 未导入 ⇒ `tsc TS2304`）—— 用户看到的仍是旧包，而我以为修复已生效。
- **⛔ 每个 iteration 必须独立渲染 + 每个工具一个 pill —— 折叠级别 / 跨迭代合并工具 / 摘要行已【彻底删光】（2026-09-11 首提；2026-09-12 用户二次要求"这个过时样式必须彻底，完全删光相关代码"，因为 master 上仍有残留）。** 唯一允许的形态：`TurnBody` 逐迭代渲染（`IterationGroup`：T 折叠 / O 文本 / C 工具）+ `FoldedToolGroup` 的 pill 行（每个工具一个 pill，点击展开该工具详情；>8 工具时前 7 pill + `+N` 溢出）。**已删除清单（源码层不得再出现）**：`types/agent.ts` + `types/shared.ts` 的 `CollapseLevel`/`COLLAPSE_LEVEL_*`/`MERGE_TOOLS_*`；`hooks/useCollapseLevel.ts`（整个文件，含 `useMergeTools`/`defaultOpenForLevel`）；`AssistantMessage` 的 `isAllCommitted` 摘要折叠分支（`t('agent.processed', …)` = "已处理 N 次迭代 · 调用 M 个工具"）；`TurnBody` 的 `flattenIterations`/`ContentBlock` 合并路径 + level/mergeTools props；`FoldedToolGroup` 的 `level='none'` 独立卡片分支与 `!mergeTools` 的 `SingleToolFold`（`ROW_BUTTON_CLASS` 一并删除）；`SettingsInteraction` 的「合并工具调用」开关；`lib/userSettings.ts` 的 `xbot-collapse-level`/`xbot-merge-tools` 同步映射；i18n 的 `processed`/`mergeTools*`/`collapseLevel*` key（×3 语言）；`MessageList`/`MessageItem`/`AgentPanel`/`LiveIteration`/`IterationHistory`/`ProgressPanel` 的 level/mergeTools 透传。**防回归守护：`web/src/components/agent/noLegacyFoldFormat.test.tsx`**（源码层 = 禁用标识符逐文件扫描 + i18n key 断言；渲染层 = 逐迭代 `[data-iter-id]`、无摘要文案、每工具 `[data-testid="tool-pill"]`）。⚠️ 页面里那个 `▸ 已处理 2 次迭代 · 调用 2 个工具` 就是本条的残留形态 —— **不是"样式偏好"，是用户已点名删除的旧格式**。**新增任何"折叠/合并"渲染前先确认这是用户明确要的**。
- **本地预览 docs-site 必须先构建主题资产（2026-09-11 踩坑）**：GeekDoc 的 CSS/JS **不在 git 里**，由 `docs-site/themes/hugo-geekdoc` 的 npm 构建产出（CI 的 `Build theme assets` 步骤）。直接 `hugo` 会生成一个**完全没有样式**的页面（`/fonts/*` 与主样式表全部 404，`<link rel=stylesheet href=baseURL/>` 指向首页本身），看起来像"站点坏了"。本地预览：`cd docs-site/themes/hugo-geekdoc && npm install && npm run build` → `cd ../.. && hugo server`。另：`baseURL = ""` 只在 CI 用 `--baseURL` 覆盖，本地构建必须显式传 `--baseURL`，否则同样是无样式页。
- **docs-site/README 截图的两个硬约束（2026-09-11 用户两次指出，务必遵守）**：① **`running` 工具只能出现在最后一个（仍在进行中的）turn** —— 已收尾 turn 的工具必须全部是 `done`；把 `status: running` 的工具放在"修复完成"这类已完结迭代**之前**是自相矛盾的，一眼假。② **必须体现"点击单个 pill 气泡 → 展开该工具详情"这一交互**（用户设计的核心），截图前用 Playwright 真实 `click()` `[data-testid="tool-pill"]`（filter hasText），让 popover 出现在帧内（状态头 + summary + JSON args + 红绿 diff）；**只拍 pill 行（未点击）是不够的**。另：pill 行本身就是**唯一**形态（折叠级别/合并工具键已删除，`xbot-collapse-level`/`xbot-merge-tools` 不再存在）—— 截图用 master 自身默认渲染即可，不要为截图去改产品代码。
- **⚠️ 跨分支共享的二进制资产必须逐字节核对版本（2026-09-11 事故，用户连续两次看到错图）。** `docs-site/static/img/app/hero.png` 同时存在于两个 PR 分支：#350（`fix/remove-iteration-collapse`）已更新为"每 iter 独立渲染"版（142582 B），#349（`feat/docs-site-fancy`）仍是**旧的折叠版**（107443 B）—— 而**消费这张图的 landing 页在 #349**。结果：在 #349 上构建出的首页 hero **仍然显示已删除的 `Processed N iterations · M tools` 折叠行**，看起来像"代码没删干净"，实际是图没同步。**教训**：① 改动一个被多分支共享的资产时，必须确认**消费方所在分支**也拿到新版本；② 对比方式（最快）：`git cat-file -s "$(git rev-parse <branch>:<path>)"`；③ 截图/图片类资产**不能只看文件名或字节数就下结论**，也不能只看"文件已替换"——必须**用 `view_image` 看内容**确认（本次正是靠 view_image 才发现产物里还是旧图）。
- **docs-site 首页是**独立 landing**（`layouts/index.html`），**不继承 GeekDoc 的 `_default/baseof.html`**（2026-09-11 用户反馈"首页是文档的一部分，有文档站的 header 和侧边栏好丑"）。** baseof 会给每个页面套上文档外壳：`.wrapper` → `site-header`（brand + 搜索 + 主题切换）→ `<aside class="gdoc-nav">`（文档导航树）→ `.gdoc-page`。首页若走它，就**看起来像"文档里的一篇文章"**（hero 被挤进文档窄栏、旁边杵着导航树）。Hugo 用 `layouts/index.html` 渲染首页，**该模板不参与 baseof 链**，因此首页要独立：重述 `<head>`（复用主题的 `head/*` partial，保证 main.scss / main.js / custom.css 照常加载）+ 自建极简 sticky nav 与 footer + 全宽 `<main>`（1160px）。**文档页完全不受影响**（仍走 baseof，header 与侧栏都在）。验证方式：构建后 grep 产物 —— 首页 `gdoc-nav`/`gdoc-header`/`site-header` 必须为 **0** 且 `xb-nav` 存在；`getting-started` 的 `gdoc-nav` 必须仍很大。主题切换复用 GeekDoc 的 `#gdoc-color-theme` id（`colortheme.js` 按该 id 绑定），但用自家 glyph，避免依赖主题的图标 sprite。
- **⚠️ 落地页的原始 HTML 块【绝不允许出现空行】—— 用 `term` shortcode，不要手写 `<div class="xb-term"><pre><code>`（2026-09-11 "整页样式全是问题"根因）。** CommonMark 的 HTML block 在**第一个空行**处结束，之后的文本会被当 markdown 解析。落地页里手写的终端块内容带空行 → 空行后的 `# 2) 启动…` 变成 **`<h1>`**（巨大字号 + 上边距，一路往下全是错行距）、URL 被 linkify 成 `<a>`、`1. …` 变成 `<ol>`。**`markdown="0"` 救不了**（Hugo/Goldmark 并不因此保留整块；`.xb-hero` 之所以看起来正常，只是因为它空行后跟的全是纯 HTML，两种解析结果一样）。修法：`layouts/shortcodes/term.html`（`{{< term label="bash" >}}…{{< /term >}}`）—— `{{< >}}` 的 inner **原样输出、不走 markdown**，空行和 `#` 都是字面量。⚠️ shortcode 里 **不要**再包 `htmlEscape`：Hugo 已经对 `.Inner` 转义过一次，再转一次就是 `&amp;gt;` / `&amp;#39;` 双重转义（直接 `{{ .Inner | strings.TrimSpace }}`）。守护：构建后断言首页 `<h1>` 只有 hero 一个，且 `.xb-term` 内不含 `<a>/<p>/<ol>/<h1>`。
- **⛔ 首页安装必须是「首屏里的极短安装卡」，不许挪回中段、不许写成段落（2026-09-11 用户明确要求）。** 用户原话：「安装必须在很显眼的地方且非常短，比如 `把以下指令输入给你的agent`」「主页必须直接渲染」（不是链接到文档页）。现状：`.xb-install` 卡片直接放在 **hero 内**（`xb-hero__cta` 之后、`xb-shot` 之前），内容 = 两行指令（`帮我在这台机器上安装并启动 xbot：` + 一行 `curl -fsSL …/install.sh | bash`）+ `复制指令` 按钮 + 一行说明（`一条命令装好 Web UI + 全部内置插件 · 其他安装方式`）。对标 opencode / bun / uv 的首屏一条命令。落地页原先的「一分钟安装」「让 Agent 帮你装」两段（四步编号 + 大段文字）已合并成页面底部一行的「其他安装方式」。**改这块时必须 `view_image` 逐屏看桌面 1440 与手机 390 两种宽度**。两个技术坑：① 复制按钮是 `layouts/index.html` 里的 ~15 行原生 JS（`navigator.clipboard` 不可用时回退 `execCommand('copy')`），图标用 sprite 里的 `i-clipboard-copy`（sprite 是单行文件，追加 symbol 时注意别破坏结尾 `</svg>`）；② URL 断行必须用**显式 `<wbr>`（路径分隔符处）+ `overflow-wrap: break-word`** —— `break-all`/`anywhere` 会让浏览器取「最后一个能放下的字符」从而把 URL 从单词中间劈开（桌面 `.../master/scr`+`ipts/install.sh`，手机 `...co`+`m/...`）；`overflow-wrap: break-word` 才会优先用 `<wbr>` 断点。窄屏额外把 `.xb-install__cmd` 字号降到 0.76rem。
- **落地页不套 `.gdoc-markdown`（`layouts/index.html` 独立外壳），所以主题的 `table` / `code` 规则都不生效 —— 任何组件样式必须自己写全。** 真实事故：对比表 `<table>` 没有 `.gdoc-markdown` 祖先 → 主题的 `width:100%` 不适用 → 裸表 shrink-to-fit 成 638px，三列挤在一起。修法：`.xb-compare table { width:100%; table-layout:fixed }` + `th/td:nth-child(n)` 显式列宽 + 窄屏 `overflow-x:auto`。同理架构图**不要用 ASCII art**：CJK 字形在浏览器等宽字体里不是双宽，`┌─│` box-drawing 画出来的框必然错位（`飞书/QQ/Web/CLI` 与边框对不齐）→ 用 `.xb-arch` 的 flex + 箭头伪元素（字体怎么换都对齐）。
- **禁止 AI agent 自行重启 xbot server**：`kill`/`pkill` xbot 进程、`nohup` 重新启动 server 等操作会中断用户正在进行的会话。除非用户明确要求重启，否则只 build 二进制 + 前端部署，由用户自行重启。前端改动 `vite build` 后 `cp dist/*` 部署即可，无需重启 server（唯一例外：改了 Go 侧静态服务/Cache-Control 时必须重启，用 `nohup bash -c 'sleep 10 && supervisorctl restart xbot-server' &` 后台异步调度，supervisorctl 不在 PATH —— 用 nix store 完整路径 `/nix/store/5mc40v8qa34jyilh5jgsfi1sc42f77hv-python3.8-supervisor-4.2.2/bin/supervisorctl`）。**⚠️ 前端部署目录不是固定的 —— 必须实测（2026-09-10 纠正，此前一轮记录有误）**：`web.static_dir` 为空时解析结果取决于实例的启动方式，**实测两个实例就读不同目录**：用户实例（`/home/smith/go/bin/xbot-cli serve --config /home/smith/.xbot/config.json`）服务 **`<bin>/web/dist`（= `/home/smith/go/bin/web/dist`）**；隔离实例（`XBOT_HOME=/tmp/xbot-newuser/.xbot ./bin/xbot-cli serve`）服务 **`$XBOT_HOME/web/dist`**。`~/.xbot/web/dist` 只是 XBOT_HOME 下的候选，**不代表正在服务的目录**。**唯一可靠的判定 = 探测文件**：`echo PROBE-a > <dirA>/__probe.txt; echo PROBE-b > <dirB>/__probe.txt; curl -s --noproxy '*' http://127.0.0.1:PORT/__probe.txt` —— 返回哪个内容实例就读哪个目录（记得删掉探测文件）。部署后**必须验证** served `index-*.js` hash == `web/dist/index.html` 的 hash；不一致就是部署到了错误目录（表现为"改了没生效"，曾被误判为缓存问题白查一轮）。
- **⚠️ 用户环境铁律：前端永远最新、后端永远最新且刚重启——用户报告 bug 时用户一定是对的。** 不要怀疑版本/缓存/部署状态（"用户用了旧代码/SW 缓存了旧 bundle/部署没生效"这类假设全部禁止——那是浪费轮次的错误方向）。每次 bug 排查都从"代码有 bug"的前提出发：前端和后端就是刚构建、刚部署、刚重启的最新 master 代码。历史上曾用裸 `http.FileServer` 导致 SW 缓存旧 bundle（已修复：`serveStaticFile` 对 `sw2.js`→`no-store`、`index.html`→`no-cache`、`assets/*`→`immutable 1y`，SW 已改名 `sw2.js` 绕开污染缓存）——该类问题已被部署管线根治，不再复现，不再作为排查方向。验证部署状态（如确需）：`curl -s --noproxy '*' -D - -o /dev/null http://127.0.0.1:16000/sw2.js`（本地 curl 必须带 `--noproxy '*'`，否则 502——本地代理拦截）。
- Issue templates: `.github/ISSUE_TEMPLATE/` — YAML forms (`*.yml`) for web UI, Markdown templates (`*.md`) for CLI/AI use with `gh issue create --template`. AI agents MUST read and fill the `.md` templates (not `.yml`) since YAML Issue Forms are web-UI-only and cannot be submitted via `gh issue create --body`.

## Knowledge Files

- `docs/agent/architecture.md` — package map, message flow, pipeline, Transport (Call+Close)/Backend/DirectBackend/Lifecycle separation, key interfaces, concurrency, TokenTracker, CompressPipeline, PersistenceBridge
- `docs/agent/install.md` — **安装的 agent 入口（指针，不是完整文档）**：指向公开可执行手册 `https://ai-pivot.github.io/xbot/agent-install/`（zh: `/zh-cn/agent-install/`），并保留最小事实集（`setup --check` 是唯一完整性判据 / LLM 配置在数据库不在 config.json / systemd 服务名是 `xbot-server` 且 `serve` 没有 `--install-service` / 回复文本在 `iteration_history`）。**改安装流程时同步更新公开页 + `scripts/install.sh` 头部注释 + 运行结束打印**（三处）
- `docs/agent/agent.md` — agent loop, middleware, SubAgent, context management, masking, dynamic context, reminder
- `docs/agent/llm.md` — LLM clients, streaming pitfalls, retry behavior, model tiers (vanguard/balance/swift)
- `docs/agent/subscription.md` — **subscription system 完整文档**: LLMFactory cache、GetLLM/GetLLMForChat/GetLLMForModel 解析链、max_context 优先级、所有切换场景（per-session/全局/settings/启动恢复）、会话隔离规则、Invalidate 速查表、TUI↔Backend 数据同步、**UserContext 统一解析（ResolveUserContext）**、**v63 多用户删除（单 operator + admin allowlist）**
- `docs/agent/tools.md` — built-in tools: Shell, Read, Edit, Glob, Grep, Cd, Fetch, WebSearch, Cron, SubAgent, CreateChat, SendMessage, Worktree, config, tui_control, TodoWrite, context_edit, AskUser, DownloadFile, ChatHistory, ManageTools, Skill, EventTrigger, TaskManager, hooks system (agent/hooks/), sandbox types, ChannelPluginTransport (stdio channel plugin transport)
- `docs/agent/settings.md` — settings system: single registry (agent/setting_runtime.go), cli_settings.go, UpdatePerModelConfig, subscription-scoped vs user-scoped, runtime apply chain
- `docs/agent/conventions.md` — error handling, logging, testing, naming, build, **local/remote unification**
- `docs/agent/hooks.md` — hooks lifecycle events, handler types, configuration, gotchas
- `docs/agent/channel.md` — CLI (BubbleTea TUI), Feishu, Web adapters, asyncCh pattern, deterministic rendering, mouse support, settings panels
- `docs/agent/memory.md` — letta vs flat providers; **xbot provider BM25 搜索 OR 语义**（`fts5OrQuery` 召回 + bm25 排序；dedup 保持 `fts5SafeQuery` AND —— 多词查询一个词缺席不能零化结果集，"frpc vs frps" 用户报告根因）
- `docs/agent/plugin.md` — plugin system architecture, runtimes, integration, RPC bridge
- `docs/agent/worktree.md` — git worktree-based multi-agent workspace isolation, WorktreeRegistry, AutoDetectAndInit, peer discovery, path security
- `docs/agent/web-message-store.md` — **Web 消息渲染架构（方案 A）**: MessageStore 单一消息状态机（Map<turnID, TurnSlot>，每 turn 1 user + 1 assistant，live 是 assistant 的未完成态）。取代 "两套数据 + buildMessageRows 启发式去重" —— 唯一性由结构保证，渲染层零去重，从根上消除 "turn 消失/重复" 整类 bug（exactDup 跨 turn 迭代号匹配等）。buildMessageRows/liveMessage prop/dedupMessages 已删除。**「一个 turn 一条 user 行」的可判定规则**：该 turn **dbID 最小**的 user 行（turn 开始时的用户消息）；同 turn 后来的 user 行是内部载体（view_image 注入，`Internal`），既不渲染（后端过滤）也不能顶掉用户消息（前端 `mergeHistory` 取最早那条）
- **⚠️ `view_image` 的 follow-up 注入行必须标记 `Internal`（2026-09-16 用户报告「上传图片后自己的 user msg 变了、图片地址也变了」根治）**：`agent/engine_run.go` 的 `injectViewImages` 用 **user role** 承载 `![label](/api/files/viewimg/…)` 引用（OpenAI tool role 不能带图），且**复用触发它的用户消息的 turn_id**——若不标记，渲染层（每 turn 只有一个 user 槽位）就会把注入行当用户消息，**顶掉用户真实输入**（正文变「📷 …」、`<img src>` 从 `/api/files/download?key=uploads%2F…` 变成 `/api/files/viewimg/<uuid>`；DB 实证 tenant 140480 turn 904：1780461 真实 + 1780467/1780477 注入）。三处契约：① `llm.ChatMessage.Internal` ↔ `session_messages.internal_only`（schema **v67**，`migrateV66ToV67`，写 `appendMessageWith`、读 `getHistoryFromWith`）；② 渲染路径过滤——`channel.ConvertMessagesToHistory{,WithIterations}` 入口 `filterInternalMessages`（**LLM 上下文 Replay/GetAllMessages 不过滤**，模型后续 turn 仍要看到图片）；③ 前端 `MessageStore.mergeHistory` 的 user 槽位**取 dbID 最小那条**（不是"后写覆盖"，与 loadMore 批次到达顺序无关）——兜住**存量**注入行（DB 里已写、无标记）。Tests: `channel/view_image_internal_test.go`（结构化/legacy 两条路径 + 过滤不动调用方切片）、`storage/sqlite/history_internal_test.go`（marker 往返 + Replay 保留）、`web/src/chat/viewImageUserRow.test.ts`（5 例：状态机/MessageStore/跨批次）、`web/e2e/user-message-integrity.spec.ts`（真实浏览器：user 行数=1、正文=用户消息、`src=uploads/…`、无注入文案）
- **⚠️ P0（2026-09-16 用户报告）：最后迭代的权威载体必须能并入**已 committed**的 turn（`web/src/chat/reduce.ts`）** —— 现象「会话 busy→idle 后从别的会话切回，看不到**最后一个迭代**的内容，刷新后才出现」。根因：最后迭代的**唯一权威载体**是 `text.progressHistory`（后端 `recordFinalIteration` 补记 —— 它没有"下一迭代"事件带动）与 `phase_done.finalIteration`；而切会话时面板是 **mounted** 的（`useActiveSSESubscription` 只按可见性增删订阅 ⇒ 不可见窗口内的 finalize 事件全丢），本地 turn 可能已按**过时快照** committed，SSE 用 `last_event_id` 回放的那条权威事件一到就被 `reduce` 的**幂等短路**丢弃（旧代码：`text_final` 里 `if (t.phase.kind === 'committed') return s`；`phase_done` 里 `if (target !== s.activeTurn) return …`）⇒ 载体丢失 ⇒ 内容永久缺失，只有整页刷新（完整 fetch）才补回。**契约**：① 已 committed 的 turn 收到 `text_final(progressHistory)` → **增量并入**（同号权威覆盖、append-only）；② 非 active turn 收到 `phase_done(finalIteration)` → 同样并入；③ **幂等**：无实际变化时返回**原 state 引用**（零渲染，避免重放抖动）。回归守护 `web/src/chat/p0-last-iteration.test.ts`（3 例，含"还原修复即 2 例红"的判别力）。
- **⛔ P0（2026-09-18 第二次，线性不一致）：迟到/误传的 turn 结尾信号冻结了运行中的 live turn ⇒ live iter「不断出现消失」、刷新才恢复（`web/src/chat/reduce.ts`）。** 现象：切 session 后新迭代出现又消失、历史稳定、刷新恢复。机制：`session(idle)` / `session_idle`（`restoreActiveProgress` 竞态 / SSE 重放的陈旧 idle）把 live turn **冻结**并清 `activeTurn`；此后该 turn 的 iteration/stream 事件因 `kind !== live` 被**整批丢弃**，直到带 `active` 快照的 `history_replaced` 把它升级回 live ⇒ 出现 ⇒ 再次冻结 ⇒ 消失（`p0-live-iteration-oscillation.test.ts` 逐事件复现）。后端顺序保证：turn 的迭代事件只出现在它的 idle 之前 ⇒ **冻结后收到更大迭代号即证明 idle 是陈旧/误传的**。修复：`iteration` case 新增 **frozen 遮蔽解除** —— 与既有 committed 遮蔽解除同规则同证据标准（`ev.iter > maxIter` ⇒ 解冻恢复 live：frozen 数据全保留、`streaming: true`、`lastSeq: null` 重置 I5 基准）；`ev.iter <= maxIter` 仍按重放丢弃。⚠️ 对称性检查表：committed 与 frozen 的遮蔽解除规则必须成对存在，改其一必查另一。守护：`web/src/chat/p0-live-iteration-oscillation.test.ts`（冻结后新迭代必须解冻；4 轮 idle⇄对账⇄新迭代交错，迭代列表单调不回退）。
- **⛔ P0（2026-09-19 第三次，同族）：`stream` case 漏做遮蔽解除 ⇒ 冻结后 live 进度「消失且永远不再更新」（`web/src/chat/reduce.ts`）。** 用户报告：「手机熄屏一段时间后解锁，看到的 busy session 中的 live 进度消失且永远不再更新」。**现场取证**（真实 DB + 服务端日志）：turn 23 在 03:45–03:52 每 15–30s 增量落库新迭代（后端一直在跑），客户端渲染停在 iteration 12（= 03:45:09 那次落库）之后再也不动 ⇒ **事件到了却被状态机整批丢弃**（不是"没收到"：日志里该会话 SSE 反复重连、DB 内容与截图逐条对齐）。机制：迟到/误传的 turn 结尾信号（`session(idle)` / agent-idle，来源见上一条）把运行中的 live turn 冻结；而 `stream` case 当时只有 committed 分支 + 空壳分支，**非空壳 frozen 直接 `return s`** ⇒ 该 turn 的**所有**流式事件被丢弃；LLM 生成期只有流式事件（结构化事件只在迭代边界/工具状态变化时发）⇒ live 永远回不来。修复三处（同一契约）：① `stream` case 补 **frozen 遮蔽解除**，与 `iteration` case **同一证据标准** `ev.iteration > maxIter`（+ `hasStreamEvidence`）；② committed 分支同样收紧到该标准（原先只要求"带流式载荷"—— 重放事件也带载荷，会把**已结束**的 turn 复活成 live/busy 幽灵）；③ 升级后的 `iter` 必须落在**进行中的那个迭代**（原为 `EMPTY_LIVE.iter=1` ⇒ 紧随其后的同迭代流式帧被判「迭代前进」⇒ 清空刚恢复的内容）。**教训**：`iteration`/`stream`/`phase_done`/`text_final` 四条事件路径的"遮蔽/解冻"规则必须逐条对齐 —— 只补一条就会留下"该事件类型整类丢失"的死角。守护：`web/src/chat/p0-live-iteration-oscillation.test.ts`（冻结后**流式**事件必须解冻并继续更新；committed + 只带 reasoning 的流式帧不得把 content 清空）/**E2E** `web/e2e/mobile-frozen-live-revive.spec.ts`（真实浏览器 + 真实 SSE 消费链路：迟到 idle 冻结后流式必须恢复 live 并持续更新；**变异自证**：撤掉 reduce 修复 ⇒ 该 E2E 必红）。
- **⛔ P0（2026-09-20 第四次，同族）：`ProgressEvent.Seq` 是 **per-Run** 水位 —— 同一个 turn 的 Run 重启后，structured 事件被 I5 gate **整批吞掉**，而 `stream` 事件无 gate ⇒ 「新迭代 live 时渲染、**完成后立刻消失**、前端历史永久卡死在熄屏前的进度」（`web/src/chat/reduce.ts`）。**用户报告**（手机熄屏很久后回来）：idle/busy 已正常，但新 iter 出现即消失、历史不动。**机制**：`Seq` 由后端 `buildMainRunConfig` 内的 `var progressSeq atomic.Uint64` 铸造 ⇒ **每个 Run 从 1 重新计数**；而同一个 turn 的 Run 会被重启（最典型：服务端重启后的 resume —— `resolveResumeTurnID` 复用被中断 turn 的 turn_id + `IterationStart = K+1` 续接**迭代号**，但 seq 归零）。客户端在熄屏/断线期间保留的是**旧 Run** 的 `ChatState.lastSeq` ⇒ 恢复后新 Run 的 structured 事件（seq 1..N ≤ 旧水位）被 `ev.seq <= s.lastSeq → return s` 判成"重放"丢弃；`stream` 事件**故意没有 seq gate**（累积全量推送）⇒ 打字机照常 ⇒ 迭代"出现即消失"（携带 delta 的事件被吞 + 下一帧 stream 的 `advanced` 清空内容），`iterations` 永不增长 ⇒ 历史卡死（刷新才从 DB 补回）。**修复**：I5 gate 改为 `isStaleSeq(lastSeq, seq, live, {iter, iterationsDelta, finalIteration})` = **seq ≤ 水位 且事件不携带任何新迭代信息**才算重放（新信息 = `ev.iter > live.iter`，或 delta/finalIteration 中含 `> 已持有最大迭代号` 的迭代）。证据标准与遮蔽解除同源：**迭代号在 turn 域内单调，后端绝不对更早的迭代重发更大号** ⇒ 携带更大迭代号的事件不可能是"已应用过的重放"。⚠️ 对称性：`iteration` 与 `phase_done` 两条 seq-gated 路径必须用**同一判据**（只补一条会留下"该事件类型整类丢失"的死角）。守护：`web/src/chat/p0-resume-run-seq-restart.test.ts`（5 例：新 Run 的 iteration / stream-then-delta / phase_done 必须应用 + 真重放仍丢弃 + 同 Run seq 单调语义不变；修复前 3 例红）。
- **⛔ 熄屏恢复的 catch-up（`restoreActiveProgress`）不得静默失败、不得静默丢弃快照（用户 2026-09-20：「理论上熄屏后应该触发 catch up；消息很长时 catch up iter 有 bug？catch up 失败应该直接 session reload —— 我怀疑是现在静默失败导致 bug」）。** `web/src/providers/sseConnection.ts` 原有两处静默：① `if (this.progressVersion !== progressVersion) return` —— fetch 期间只要有**任何结构化事件**到达（流式期间几乎必然）就丢掉恢复快照；而**断连窗口丢掉的迭代只有这份快照（或 DB reload）能补**（新事件不会重放它们：流式帧只带当前迭代的全量文本、结构化事件只带 0-1 条 delta）⇒ 迭代永久缺失；② `catch {}` 完全吞掉 RPC 失败 ⇒ 既不 reload 也无日志 ⇒ 界面永久停在旧进度。**修复**：① 快照**一律投递**，过期判定交给唯一的状态机判据（I5 的 `isStaleSeq`：纯重放被丢弃、携带缺失迭代则应用；迭代是 append-only union + content 非空优先 ⇒ 应用过期快照不会回退）；provider 只负责**真实**数据丢失的全量 reload 降级（`resync_required` / turnID 变化 / >100 迭代 gap / **RPC 失败**）；② `catch` → `console.warn` + `replay_gap{force_reload:'true'}` + `dispatchSessionsResync()`。守护：`src/providers/sseConnection.test.ts` 的「applies the recovery snapshot even when a lifecycle event bumped progressVersion during the RPC」+「falls back to a full DB reload when the recovery RPC fails (never a silent no-op)」（修复前分别红）。
- **⛔ 「思考中…」占位符与已渲染迭代不得并存（用户 2026-09-20 截图：「思考中和思考 stream 明显不可能同时存在才对」—— 上方一条已完成的「思考 15175 字」，下方又冒出「思考中…」）。** 判据 `liveIterationInFlight({iteration, iterationHistory})`（`web/src/components/agent/progressStore.ts`，**全仓唯一实现**）= 「进行中迭代号**尚未**作为历史渲染过」；两个占位符渲染点（`LiveIteration` 的空内容分支、`MessageList` 的 busy 占位符）**共用**它（互斥 ⇒ 同一状态恰好一个指示器）；`MessageList` 另加 `tailIsLiveRow`（尾行就是 live 行 ⇒ 尾部渲染的已是该 turn 自己的迭代内容，不再叠加占位符，避免把"已提交迭代 + 思考中"并排画出来）。配套状态侧修复：`iteration` case 的 `iter` **单调不回退**（`advanced ? ev.iter : prev.iter` —— gap 修复 delta 会携带更早的迭代号，回退会让"进行中迭代"落在一个已渲染成历史块的迭代上，正是那个矛盾画面）。守护：`LiveIteration.test.tsx`（边界态必须显示占位符（`iteration: 2` 在飞）/ 在飞迭代已渲染 ⇒ 不得显示）+ `MessageList.test.tsx`（既有三条不变量用例不变）。
 ⇒ 新状态机（M4 起的唯一渲染源）**完全丢弃**它（旧 `useProgressStream` 已不再挂载，其 `case 'sync_progress'` 是死代码）。那条快照本是"每 15s 至少一次 live 状态 ⇒ 不再卡死"的设计兜底；当前冻结态只能靠后续业务事件解冻（本条的流式解除覆盖了主要场景）。若将来再出现"冻结后长时间无事件"的形态，把 `sync_progress` 接进状态机是首选结构性修复（它携带服务端权威的进行中快照）。
- **⛔⛔ 不变量（用户 2026-09-19 反复点名，此前多轮未修好）：「只要输入框是 cancel 按钮，就一定不能上面渲染的内容是 idle 内容」。** 形式化：`busy（composer = cancel）⟹ 列表里必须有一个可见的"进行中"信号`。composer 的 busy = `currentSession.running（服务端 reconcile 权威：session-tree/status REST 对账 + SSE session）|| progressSnapshot.streaming || busyFallback(activeTurn !== null)`；而 turn 的 live-ness 此前**只**由事件驱动 ⇒ 两侧权威分叉。**两个破坏点，缺一不可地修**：
  ① **渲染层（`MessageList`）**：`frozen` 行（cancel / idle 兜底定格）同样 `isPartial=true`，被当作 live 行（`liveId`）⇒ (a) 它拿到的 `liveProgress` 是 `liveProgressFromState` 的**空**快照（frozen ⇒ `activeTurn===null` ⇒ EMPTY）⇒ 自身不渲染任何进行中信号；(b) busy 占位符的条件 `liveId === null` 因此不成立 ⇒ 也被抑制 ⇒ **cancel + 内容像 idle**。修复：`ChatMessage.frozen`（`integrate.ts` 只对 `case 'frozen'` 置 true）⇒ `liveId` **排除 frozen 行**（frozen 行的工具/内容由 `deriveRows` 折进 `iterations`，不需要 liveProgress）；占位符条件由 `liveId === null` 改为 **`!liveShowsIndicator`**（`liveShowsIndicator = liveId !== null && (liveProgress.streaming === true || liveProgress.phase === 'compressing')`）—— busy 时**只要 live 行自身没渲染信号就必须渲染占位符**（含"live 行存在但 streaming=false"的窗口），仍保持「恰好一个指示器」（互斥）。
  ② **状态层（`reduce`）**：一条**迟到/误传/重放**的 coarse `session(idle)` / `agent-idle`（**不带 turn 身份**，可能来自 SSE 重连的 `last_event_id` 回放窗口或 `restoreActiveProgress` 竞态）冻结运行中的 turn 并清 `activeTurn` ⇒ 运行中的 turn 被渲染成 idle（正是本 bug：**busy 的会话看起来是 idle**）。修复：`ChatState.sessionRunning` + 领域事件 **`session_running`**（`useAgentChatState` 的 `sessionRunning` 参数 ← `AgentPanel` 的 `currentSession?.running`，每次变化 dispatch）只作**闸门**：`session(idle)` / `session` idle 在 **`sessionRunning === true` 时直接忽略**（陈旧信号；真结束由对账翻 false）；`session_running(false)` ⇒ live turn 定格（内容保留）。⛔ **绝不允许"为了让不变量成立而伪造 live turn"**（我 2026-09-19 的回归：在 `history_replaced` 里按"最新未 finalize"提升 ⇒ DB 还原的 turn 走 `commitViaFold`（`integrate.ts`）判据恒成立 ⇒ **已结束的 turn 被伪装成 live** ⇒ composer 幽灵 busy + 占位符被抑制 ⇒ 普通切会话必现「cancel + 看不到任何进行中信号」，用户原话「你的这个修改会在 p0 bug 上再加一个 p0」）。live-ness 只由真实信号决定：事件路径（`stream`/`iteration` 的遮蔽解除）+ 服务端权威快照（`history_replaced` 的 `ev.active`，仅当它指向该 turn 时恢复 live）；**可视化保障交给渲染层**：busy 而**列表尾行**没有"进行中"渲染时，必须渲染占位符（`MessageList` 的 `tailShowsIndicator` —— 判据看**尾行**，这样即便存在"不在可视尾部"的 live 行也不会把底部信号挡掉）。守护：`web/src/chat/p0-busy-invariant.test.ts`（running 时 coarse idle 必须被忽略 / running=false 才定格 / **running=true 绝不把历史 turn 提升为 live**（本回归的判别用例：在 2c11010d 上必红）/ 幂等零渲染）× `MessageList.test.tsx`（busy+frozen ⇒ 占位符必须渲染；busy+streaming live 且在尾部 ⇒ 占位符被抑制；**busy + live 行不在尾部 ⇒ 占位符必须渲染**）× **E2E `web/e2e/busy-invariant.spec.ts`**（真实浏览器：**前提** stop 按钮可见 ⇒ 必须有 `.sweep-text` 进行中信号；含"coarse idle 冻结出的 frozen 行 + busy"与"active_progress 缺失的 busy 会话"两形态）。
- `docs/agent/multimodal.md` — **多模态视觉输入（v64）**: 引用协议（content 只存稳定引用，构建时转 data: URL）+ vision 纯手动 per-model 开关（`subscription_models.vision`，无内置白名单）+ `llm.ImageResolver`（四类 ref：viewimg://、/api/files/viewimg/、/api/files/download?key=、http(s)://、file:// workspace 白名单）+ 预处理（≤2048px/≤4MB/bmp|tiff→png）+ `view_image` 工具（follow-up user 消息注入——OpenAI tool role 不能带图；该注入行标记 `llm.ChatMessage.Internal` → DB `session_messages.internal_only`，**只进 LLM 上下文，绝不渲染成用户消息**）+ LRU（32 张/128MB）+ 预算（最近 8 张）+ 降级占位（vision off/加载失败/超预算）
- `docs/agent/web-consistency-design.md` — **Web 消息一致性设计（Raft 模型）**: Log（eventStream ring + DB）→ Snapshot（get_history/active_progress）→ State Machine（ProgressStore/MessageStore）映射；两个独立 seq 序列（SSE envelope per-route vs ProgressEvent.Seq per-Run，混用即 bug）；已保证的一致性（渲染线性一致性/turn 边界原子性/三路 DB reload 修复链）；弱网风险点 V1-V4 + 修复优先级
- `docs/agent/web-linearizability.md` — **Web 前端形式化证明（Raft 模型）**: 状态 S=(M,P,L,W)；不变量 I1-I7 + 引理 L1-L7 + 定理 T1-T7（渲染线性一致性/turn 边界无闪烁/顺序正确/reload 一致性/追赶收敛/gap 修复/跨 turn 隔离）；诚实标注 4 个已知前提违反点 V1-V4（修复前最终一致，修复后无条件线性一致）
- `docs/agent/genui-plugin-design.md` — **GenUI (display_html) 插件化设计**: display_html 已从内置工具迁移为独立 Go stdio channel 插件 `plugins/xbot-genui/`（channel_tools 声明 + `channels:["web"]` + `ui` 元数据）。通用性设计 §9（UI 能力由工具元数据声明，不由工具名决定 —— `tools.UIDecl`/`tools.UIDeclProvider` 接口，engine_wire 流式提取 + 前端渲染判定全部读元数据）+ 形式化证明 §10 T1-T9（正确性 + 流畅性）。前端 XBOT_UI 运行时（`web/src/genui/runtime.tsx`：组件库/ECharts/three/motion/主题）。**前端 GenUI 渲染已迁移到 messageRenderer 声明**：`PluginRuntime.renderTool` 调度器（`matchesTool` 匹配 `{tool}`/`{uiMode}`/`{role}`/`{}` + priority 降序 + null fallback）+ 内置 `builtinGenuiRenderer`（matches `{uiMode:'genui'}`）+ `builtinLegacyDisplayHtmlRenderer`（matches `{tool:'display_html'}` 兜底旧历史消息），注册于 `AgentPanel`；`ToolRender`/`AssistantMessage`/`ToolGroup` 改经 renderTool 派发，`genui.ts` 的 `isGenUITool` 删除 `name==='display_html'` fallback（只留 `uiMode` 判据）。`ChannelToolDecl.Channels/UI` 字段 + `ChannelToolBridge` 实现 UIDeclProvider + `execute_tool` result 支持 `ui_code`。**插件 channel 激活需要 `channels.<name>.enabled=true`（config.json）**——`stdioChannelPluginProvider.IsEnabled(nil)` 返回 false，仅装插件不创建 channel 实例，display_html 工具不可见。

### Gotcha knowledge files (Read the relevant one BEFORE changing that area)

| File | Covers |
|------|--------|
| `docs/agent/gotchas-agent-core.md` | concurrency, subscriptions/settings, context management & compression, append-only history/rewind, cron, startup, hooks, windows |
| `docs/agent/gotchas-cli-tui.md` | CLI/BubbleTea, deterministic rendering, rendering perf/panics, CLI todos & sessions, remote-CLI races, TUI control & config tools, SubAgent progress identity, reasoning contamination, backend/transport |
| `docs/agent/gotchas-web-frontend.md` | web message store & state machine, streaming/live iteration rules, iteration windowing, virtual list, tool pills, copy menu, sidebar/panel layout, feishu-free web UI contracts |
| `docs/agent/gotchas-feishu.md` | Feishu channel: CardKit streaming cards + native CoT rendering |
| `docs/agent/gotchas-plugins.md` | plugin system, web plugin runtime, i18n of plugin manifests |
| `docs/agent/gotchas-tools.md` | view_image, truncation/retrieval hints, share_file key sanitization |
| `docs/agent/gotchas-llm.md` | Responses API encrypted reasoning round-trip |
| `docs/agent/gotchas-install-worktree.md` | one-click setup/release, git worktree isolation |
| `docs/agent/gotchas-misc.md` | 2026-09-17 batch (synthetic tools, reminder parsing, CoT) |

## ⛔ Critical invariants (one-line index — full text in the knowledge files)

The highest-priority rules, one line each. **Before changing the related code, Read the named file** — it carries the full rule, the incident that produced it, and the guard test.

- ⛔ `docs/agent/gotchas-agent-core.md` — `max_concurrency` 只能有一个存储位置（2026-09-17 用户报告根治）：「设了 100 并发，只起 4-5 个子代理就卡」+「统计里的 TTFT 很短、和入库时间对不上」。
- ⛔ `docs/agent/gotchas-agent-core.md` — 模型解析严禁用"裸模型名"——任何时候解析模型必须带订阅 id（用户明确要求，2026-09-11）。
- ⛔ `docs/agent/gotchas-agent-core.md` — 压缩三把尺子 + 无限循环三重防线（2026-08-30 "200k 上下文无限循环压缩"修复）。
- ⛔ `docs/agent/gotchas-agent-core.md` — 停机必须先把 WAL 的已提交内容 checkpoint 进主库（2026-09-17 丢数据事故；
- ⛔ `docs/agent/gotchas-cli-tui.md` — 迭代边界必须保留已渲染的工具 —— 新状态机（`chat/reduce.ts`）曾漏掉旧 store 的同款守卫（2026-09-18 P0 渲染回归）。
- ⛔ `docs/agent/gotchas-cli-tui.md` — 写事务边界由「跨 handle 原子性」决定，不能只看性能（2026-09-18 缩短写事务的教训）。
- ⛔ `docs/agent/gotchas-cli-tui.md` — 读接口绝不允许改 `last_active_at`（2026-09-11 线上 bug：刷新页面把所有昨天的会话变成"今天 active"）。
- ⛔ `docs/agent/gotchas-cli-tui.md` — `tui_control` 是 CLI 渠道专属工具（用户 2026-09-20 要求「应该只在 cli 注册」；
- ⛔ `docs/agent/gotchas-web-frontend.md` — 工具描述就是行为：提示里让模型「等」，它就会等——反向也要主动改写（2026-09-12 用户要求 6 项工具引导优化）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — `break-words`（`overflow-wrap: break-word`）不能阻止「超长不可断 token」把 shrink-to-fit 盒子顶宽 —— 必须用 `wrap-anywhere`（`overflow-wrap: anywhere`）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — FileReplace 无限循环守卫（`doReplace`）—— new_string 包含 old_string 时替换是 no-op 但永远"成功"。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 每帧回调里读几何 = 强制同步布局（2026-09-18 生产 dev-build trace 归因，用户「还是卡得要死」）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 迭代级窗口化：交互成本必须与 turn 内迭代数解耦（2026-09-13「手机上 iter 多了还是很卡，点什么交互都要等几秒」根治）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 窗口化必须让"每帧元素数"与迭代数无关（2026-09-13 用户报告「turn busy 且该 turn 特别长（几千 iter）时手机必卡，切会话/刷新都没用」）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 切会话（长历史会话）卡顿的三个根因（2026-09-17；
- ⛔ `docs/agent/gotchas-web-frontend.md` — 冻结（卸载内容）的可信判据是「内容有没有被裁剪」，绝不是高度下限（2026-09-18 trace 12.gz 生产掉帧根治；
- ⛔ `docs/agent/gotchas-web-frontend.md` — 所有「每帧一次」的更新必须走同一个帧调度器（`web/src/lib/frameScheduler.ts`，2026-09-18 零掉帧重构）：页面曾有 6 条独立更新流各自调度 rAF/interval（`chat/store.ts` 通知、`progressStore.ts` 通知、`useTypewriter` 的 50ms interval、`MessageList` 的 scroll/observe/follow、`TurnBody` 的 flush），trace 9.gz 实测 8 秒 2047 次渲染 ≈ 4 次/帧 ⇒ `te @ vendor-react` 2387ms +…
- ⛔ `docs/agent/gotchas-web-frontend.md` — IO/RO 回调必须 rAF 合帧，禁止在回调里逐个同步标脏（2026-09-18 trace 8.gz 实测；
- ⛔ `docs/agent/gotchas-web-frontend.md` — 窗口化的 settle 采样必须「合并式」调度 —— 每 key 一个定时器 + 每次变化 clear+set = 每秒 7.5k 次 install/remove，`clearTimeout` 独占 21% CPU（2026-09-17 卡顿复发根治；
- ⛔ `docs/agent/gotchas-web-frontend.md` — 排队面板（StagingTray）设计铁律（2026-09-13 用户两次否掉上一版后确定）：① 只有一个展开概念 —— 展开/收起整条面板（`collapsed`）即全量渲染，长队列靠容器内部滚动（`max-h-[min(50vh,22rem)] overflow-y-auto overscroll-contain`）容纳；
- ⛔ `docs/agent/gotchas-web-frontend.md` — 工具 pill 手机溢出根治：`min-w-0` 必须补齐到整条 shrink-to-fit flex 链（2026-09-13 用户「subagent 工具 pill 手机上有时候超宽，这样像 bug」）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 迭代块禁止用 `content-visibility` 做离屏跳过（2026-09-13 用户报告「向上快速滚动鬼打墙，看起来一直在滚其实几乎一点没动，永远到不了最上方」；
- ⛔ `docs/agent/gotchas-web-frontend.md` — 窗口化状态的身份必须是「内容」而不是「组件实例」，且「没有布局的测量」永不是测量（2026-09-13「手机上 iter 多了就卡 / 开侧边栏慢 5-6 倍」真正根因根治）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 追加行必须「权威重测」虚拟列表（2026-09-17 用户报告「`!pwd` 输出看不见」的真正最后一环，纯前端）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 虚拟列表行键必须全表唯一（`buildUniqueRowKeys`，2026-09-24 生产崩溃 React #185 根治）：两行共享同一个 `getItemKey` 键（`turn-${turnID}-${role}`）→ TanStack `itemSizeCache` 互覆 → `resizeItem` 每轮 delta≠0 → notify → 无限嵌套重渲染（50 层后 #185）。复现 `virtualizer_duplicate_key_loop.test.ts`；重复行加 `#dup` 后缀 + `[DUPROWKEY]` 诊断。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 编辑器事件回调里**绝不同步 setState**（同一天第二个 React #185：「输入框语音输入、频繁改文字必崩」）：`useCompletion` 的 `editor.on('update'|'selectionUpdate')` 在事务派发中同步 `setTextContent`，且值每次都不同 ⇒ 嵌套更新爆表。契约：事件回调只「标脏 + 排一帧」（`frameScheduler`）；配套堵住 draft effect（`onDraftConsumed` 进 ref、内容相同不 `setContent`）、父组件 `useCallback` 稳定身份、placeholder effect 去多余派发。守护 `useCompletion.test.tsx`（变异自证红→绿）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 命令（`!cmd`/slash）回复的前端渲染契约（2026-09-17 "发了没反应 / 消息消失" 三连根因）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 判别式（discriminator）类改动必须有【真实链路】守护 —— E2E mock 不能作为唯一判据（2026-09-21 P0「`!cmd` 输出不显示」的根因与教训）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 网页终端里"凭空出现"的乱码 = 终端探针的【应答被 PTY 回显】（2026-09-22 用户报告：`10;rgb:cccc/cccc/cccc11;rgb:1e1e/1e1e/1e1e12;2$y…;0c`）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 「一行多个」的完整正解（2026-09-15 用户三轮真机复查后定稿，三个坑缺一不可）：① 上限必须落在 wrapper 上、且必须不含百分比 —— `maxWidth: calc(50% - 8px)` 写在 pill 上无效（pill 的包含块是 `LazyPillPopover` wrapper = 内容定宽 indefinite，规范规定百分比对 indefinite 包含块按 `none` 处理；
- ⛔ `docs/agent/gotchas-web-frontend.md` — 「所有 pill 的 icon 与首字符左对齐」靠【恒定槽位宽度】保证（同日用户追加要求）：① 每个 pill 都渲染 3px 左色条（失败=红实条 / 终止=灰虚线 / 其余=分类色）——只在失败时才渲染会让槽位宽度跳变；
- ⛔ `docs/agent/gotchas-web-frontend.md` — i18n：组件内的用户可见文案必须走 i18n 实例（不依赖 `t` prop 透传），且三语同步（2026-09-15）：pill 的 `失败/已终止/排队/生成中/执行中`、`系统` 角标、`N 失败`、`+N` 此前是硬编码中文（en/ja 用户看到中文）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 分类切换器 + 全部折叠必须在桌面 `core.sessions` 面板里渲染（`SessionViewBar`，桌面面板与手机抽屉共用）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — docked 面板标题栏【不再】把点击当折叠（2026-09-15 用户：「点 `Sessions` 这个词有bug，别的位置没有」）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 空态判定必须用【可见】而非【存在】：`PanelDock` 的 `noPinned` 提示旧条件是 `sideIds.length === 0` —— 折叠/浮走唯一展开面板时该条件为假 ⇒ 既没面板也没提示（黑栏）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 常驻面板（`PINNED_DEFAULTS`/`core.sessions`）不许离开左栏（2026-09-15 用户：「就 sessions 这一行还有一个有完全一样的 bug 的按钮」——⌄ 折叠 / ⤢ 升浮窗的结果与标题点击同款：会话面板整个从堆叠消失，左栏只剩空态提示）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 输入框禁止 `readOnly`-until-focus 反自动填充（手机端永远不弹软键盘；
- ⛔ `docs/agent/gotchas-web-frontend.md` — 打开链接必须过协议白名单：`resolveOpenableHref` 只放行 http/https/mailto（`javascript:` / `data:` / `file:` 一律拒绝），相对链接按 base 解析成绝对地址（消息里有 `/api/files/download?...` 这类同源链接）；
- ⛔ `docs/agent/gotchas-web-frontend.md` — `historyReady` 必须是【派生状态】，不能是可写 state —— 否则切会话会闪一帧「空 MessageList」（2026-09-18 用户报告「切换会话会闪烁一瞬间错误布局」）。
- ⛔ `docs/agent/gotchas-web-frontend.md` — 「会话只要开始切换就应该渲染 loading」还覆盖第二条路径：切 tab 进入一个【已存在】的面板（用户 2026-09-18 二次反馈，帧级 E2E 定稿）。
- ⛔ `docs/agent/gotchas-feishu.md` — CardKit 流式契约（2026-09-14 真实 API 探针实测，别再靠推断）：`CardElement.Content`（打字机流式）只能写「建卡模板里声明过」的元素 id；
- ⛔ `docs/agent/gotchas-feishu.md` — 飞书 CardKit 渲染 primitive 白名单（2026-09-14 生产事故复盘，别再用别的写法）：只有两条 API 现场被证明"真的会渲染"：① 整卡 `Card.Update`（结构：`collapsible_panel` 思考块/工具块、各迭代正文、markdown 表格）② `CardElement.Content` 写「建卡 `Card.Update`/`Create` 时就在卡片 JSON 里声明过」的元素（正文打字机流式）。
- ⛔ `docs/agent/gotchas-plugins.md` — 插件 i18n 是【平台通用能力】：文案随插件清单走，绝不允许写进宿主的 `web/src/i18n/*.ts`（2026-09-19 用户要求）。
- ⛔ `docs/agent/gotchas-plugins.md` — 语言切换后文案必须动态更新（2026-09-19 用户实测「改了语言插件没动态变化」；
- ⛔ `docs/agent/gotchas-plugins.md` — 占位 agent 面板【不承载会话时不得渲染任何面板 UI】——「切换会话闪烁一瞬间错误布局」的真因（2026-09-18，帧级 E2E 定稿）。
- ⛔ `docs/agent/gotchas-plugins.md` — 不可见面板不得在 DOM 里保留 chrome（2026-09-19 P0：切回「已打开过」的 tab 时输入框浮在消息区上方一闪）。
- ⛔ `docs/agent/gotchas-plugins.md` — 飞书原生 CoT 五个渲染根因（2026-09-17 用户连续四次真机复查后定稿，每条都有红灯→绿灯的判别力测试）：① 工具身份必须用宿主的 `protocol.ToolProgress.CallID`（= dsh-lark 的 `event.data.callId`），绝不能用「名字#迭代号#(Label|Args)」拼 —— 工具完成时 `updateToolResultLine` 会用 `formatToolProgress(Name, Arguments)` 重算 label，而 active 快照里 label 还是空的 ⇒ 同一次调用得到两个 key ⇒ `TOOL_CALL_ST…
- ⛔ `docs/agent/gotchas-plugins.md` — AskUser 面板「完全不弹窗」的根因（2026-09-17 用户 P0 实测；
- ⛔ `docs/agent/gotchas-plugins.md` — runner 注册表是「执行目标」与「远程机器面板」的单一权威 —— 幽灵机器（`runners` 表只增不减）根治（2026-09-20 用户实测：面板 1 台真机、选择器 6 条）。
- ⛔ `docs/agent/gotchas-plugins.md` — runner「所有命令 exit code 2」根治（2026-09-22 用户 P0：ssh-runner 连 GPU 机 1101 后 agent 全部命令失败）。
- ⛔ `docs/agent/gotchas-tools.md` — 名字不承担唯一性：key 形如 `agent/<uuid>/<name>`，uuid 是每次发布新铸的 ⇒ 不同会话分享同名文件天然各自独立（守护测试断言两次分享 key 不同）。
- ⛔ `docs/agent/gotchas-llm.md` — 「要了 reasoning 就必须要摘要」——`encrypted_content` 是给无状态重放用的，用户看不到（2026-09-19 用户报告 gpt-6-astra 在 Web 不显示 reasoning 的根因）。
- ⛔ `docs/agent/gotchas-misc.md` — 注入型（fake）工具【绝不】在 content 里声明「这不是你调用的工具」（2026-09-17 用户纠正，取代同日早些时候的做法）：`agent.syntheticInjectionNotice()` 已整体删除，`newSyntheticToolPair` 不再给注入工具的结果加任何前缀 —— 注入结果就是一段普通 tool-result 文本（与 Shell/Read 输出同形态）。
- ⛔ `docs/agent/gotchas-misc.md` — 飞书原生 CoT 的「停止生成」按钮必须处理（2026-09-23 用户报告「中止按钮没处理」）：平台契约 —— 用户点 CoT 消息上的「停止生成」时，飞书发 `card.action.trigger` 回调，`action.tag == "cot_stop"`（value 带 cot_id/message_id；
- ⛔ `docs/agent/gotchas-misc.md` — 飞书 CoT 测试必须禁用异步 drainer（走 `newFakeCoT`），否则 flaky（2026-09-23 master CI 失败根治）：`feishuCoT.emit` 会 `go c.drain()` 启动异步写线程；
## ⚠️ Warnings (one-line index — full text in the knowledge files)

Same contract: one-line digest here, full text (with incident + guard test) in the named knowledge file.

- ⚠️ `docs/agent/gotchas-agent-core.md` — Loop breaker（iteration-loop detection）默认关闭（实验开关，2026-08-31 起）——必须在 config.json `agent.experimental.iteration_loop_detection: true` 显式开启。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 排查"TTFT 很短但体感很慢"必须知道：排队时间不计入 TTFT。
- ⚠️ `docs/agent/gotchas-agent-core.md` — Web 端 LLM 配置必须「两边数据统一」：设置里改完，会话 LLM 选择栏必须立刻更新（用户报告 2026-09-16：「设置里添加/更新 LLM 之后，当前会话的 llm 选择栏不更新，需要刷新」）。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 会话模型绑定永不留空 + 单 operator user_default_model 兜底（2026-09-07 "模型漂移 + 显示 1M 但 200k 触发压缩"根治）。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 估算 token 禁止做决策（2026-09-02 用户指令，全局原则——Development Principles "Never Estimate Tokens" 条目）。
- ⛔ `docs/agent/gotchas-agent-core.md` — v55+ 回复文本回填（`fillAssistantContentFromIterations`）只能补「该 turn 的收尾回复行」：按 turn 补所有 `content==''` 的 assistant 消息 = 把该 turn 最终回复复制进它的每条无正文迭代 ⇒ 上下文暴涨 + 模型**复读上一 turn 的回复**（2026-09-24 用户报告「上一个迭代结束的 Content 在下一个 turn 的某一个迭代中莫名其妙重复一次」；DB 实证 turn 48 最终迭代 == turn 49 第 2 迭代 byte-identical，prompt_chars 432,095 → 801,870）。写路径契约（回复行 = turn 收尾行、content 空、无 tool_calls）由 `TestHandleRunOutput_ProducesReplyRowShapeTheFillReliesOn` 守护。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 压缩失败【绝不允许】终止用户的 turn（2026-09-15 用户报告「自动压缩 / 主动压缩（compact_context）导致迭代终止」根治）。
- ⚠️ `docs/agent/gotchas-agent-core.md` — xbot-memory 的 LLM 调用必须流式（`m.generateLLM`，2026-08-30 "PostCompress 卡 10 分钟"修复）——三个调用点（updateCoreSummary/generateSessionSummary/extractAtomicMemories）曾直调 `llmClient.Generate`（非流式）。
- ⚠️ `docs/agent/gotchas-agent-core.md` — xbot-memory 的 LLM client 必须参数化传递，禁止共享可变字段（2026-09-02 chat_BD94FA4BB469 事故修复）——`XbotMemory.llmClient/model` 共享字段已删除。
- ⚠️ `docs/agent/gotchas-agent-core.md` — `allow_self_compact` 已接入设置系统（2026-09-04，web 设置 → 智能体）。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 压缩管道 Pre/Post 钩子异步化 + 压缩请求 verbatim 缓存命中（2026-09-02 重构，"压缩卡 12m38s"根治）。
- ⚠️ `docs/agent/gotchas-agent-core.md` — xbot 记忆 provider 会话隔离 + supersede 链（2026-09-02 重构，"注入错误/过期记忆"根治）。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 主 agent 与 SubAgent 共享 offload store（2026-09-11 用户要求「相互可见」）：目录 key 一律用 canonical root session，归属（OwnerKey）用于隔离清理。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 任何模型可见的截断都必须自带「怎么取回」提示，且截断必须 rune 安全（2026-09-11 修复：subagent 结果被截断后模型不知道用 offload_recall）。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 渲染丢迭代第二形态（2026-08-23 15:02 事故，tenant 150660 turn 67→68）：恢复 turn 只有一条空壳 assistant（v55+ 占位行）时，`!isIntermediate` 结构化分支的 `pendingIters = nil` 蒸发上一 turn 的全部迭代。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 重启 resume 必须复用被中断 turn 的 turn_id + 续接迭代号（resolveResumeTurnID + IterationStart，2026-08-29 "两个大 dom" 修复）——否则同一逻辑 turn 被拆成 user(N)/resume(N+1)/resume(N+2) 三个 turn，前端渲染成多个 assistant 块，与不重启的最终渲染不一致。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 跨分支合并的集成断裂：一个分支导出/改名 helper，另一个分支仍引用旧名（2026-09-11 把 #359–#366 合并成一个 PR 时踩到）。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 命令回复（无 turn_id）必须渲染为 legacy 独立行 —— M4 状态机的 `text_final` 遇到 `turnID=null` 且 `activeTurn=null` 会 `return s` 静默吞掉（2026-09-17 "!pwd 没有输出" 的第三个根因，纯前端）。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 本机（`none`）沙箱的用户工作区必须真的创建 —— 报错 `fork/exec /bin/bash: no such file or directory` 说的是「工作目录不存在」，不是「bash 不存在」（2026-09-17 `!cmd` 首次运行失败的第二个根因）。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 命令回复（`!cmd` bang / slash）绝不允许继承正在跑的 turn 的 id —— 否则命令输出会被该 turn 的真回复覆盖而静默消失（2026-09-17 "!pwd 没有输出" 的第四个根因，也是真正原因）。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 请求失败重试：默认重试（白名单已废弃，2026-09-11 用户要求「所有失败都触发指数退避」）。
- ⚠️ `docs/agent/gotchas-agent-core.md` — Tool-call arguments JSON 损坏双层检测（2026-08-30 "parse args: unexpected end of JSON input" 修复）——网关中段丢 chunk/尾部断流时两层静默放行，agent 收晦涩 JSON 语法错误盲目重试烧 token。
- ⚠️ `docs/agent/gotchas-agent-core.md` — 读路径绝不能被"已删除会话"重新 materialize（2026-09-10「幽灵会话」根治）：`tenants` 里反复冒出用户没创建过的会话 —— 名字是默认的 `chat_XXXX`、`msgs=0`、不在 `user_chats`，删了又生（用户报告"总是莫名其妙多几个我没有的会话"）。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — `drainAndProcessNotifications` batches all notifications into ONE user message. 
- ⚠️ `docs/agent/gotchas-cli-tui.md` — 前端组件里绝不嵌套 `<button>`（2026-09-11 同一轮抓到两处：StagingTray 表头 + ContextBar）。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — pending AskUser 的唯一权威是「持久化会话状态」——内存注册表 / 前端 `askUserPrompts` / CLI 磁盘文件 / Feishu 卡片 map 全部只是可失效缓存。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — 「没有 pending」在前端的唯一判据是「载荷不含任何问题」——`parseAskUserPrompt` 必须返回 `null`，绝不合成空 prompt（2026-09-20 CI 9 个 spec 全红）。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — 不变式：busy ⇒ 不存在 pending（用户 2026-09-16 拍板：「只要 busy，那么问题就自动被 cancel 了」）。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — 每一处"清 pending"必须经同一出口（清缓存 + 落库配对 + 发 `ask_user_resolved`），且清内存与落 `ask_answer` 之间不得有顺序缝隙。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — 手机设置/后台 gap 后 busy 永久卡死（2026-08-30 "打开设置期间会话完成，退出后卡 busy，最终回复已渲染"）：restoreActiveProgress 的 done/null 分支必须 dispatch agent-idle。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — SESSION-PANEL GLOBAL-STATE BAN（2026-09-01 "cancel 一个 session 导致所有 busy 的 session 状态异常"根治 + ESLint 编译层约束）：per-session 代码禁止直接操作 window 事件，跨 session 信号必须经 `sessionEvents.ts`（类型级强制 session 身份）。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — `isProgressLifecycleEvent` 不得包含 `stream_content`（纯流式 delta，不是状态变化）。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — SubAgent 的 RunConfig 必须与主 Agent 的 ToolContext 接线对齐 —— 漏接字段会让工具静默失效或直接报错（2026-09-12 用户实测 "background tasks not supported (BgTaskManager not configured)"）。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — SubAgent / CreateChat 的 `role` 是 best-effort（用户决策 2026-09-16）——缺省或拼写不准都尽量匹配，只有有歧义才报错。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — SubAgent 调用链只校验深度，不校验角色重复（用户决策 2026-09-16，`agent/engine.go` `CallChain.CanSpawn`）。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — SubAgent progress 事件必须携带 TurnID（`interactive.go` wireSubAgentProgress）—— 否则 Web 子代理 SSE 实时进度不更新。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — SubAgent 会话渲染错序根治（2026-08-30 "子代理完成后进入他的会话，user 消息出现在 assistant 回答之后"）：完成路径的 user 行和最终回复行必须 stamp TurnID + deriveTurnIDs 补 Pass 3。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — SubAgent stream 回调必须始终推送全量（`interactive.go` wireSubAgentProgress StreamContentFunc/StreamReasoningFunc）—— 用 delta push（StreamDelta）会导致流式内容倒流。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — 子代理 idle 后 Web 渲染重复历史 + 缺最后 iter —— 三处必须与主 agent 对齐。
- ⚠️ `docs/agent/gotchas-cli-tui.md` — 「跳过通知」类优化的判据必须与【渲染投影】一致，绝不能用内部结构（2026-09-15 我引入的严重回归）：`MessageStore.mergeHistory` 为消除"切会话 0.5s 闪烁"加了"内容未变 ⇒ 不通知"的短路，最初指纹取内部结构（`slots.user/assistant + legacy + pendingUsers`）——它漏了 live 行、且与 `toRows()` 的行集并非一一对应 ⇒ user 行回填…
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 新建/派生会话必须同时打开（或聚焦）该会话的 agent tab —— session-per-tab 下"切会话 = 切 tab"（2026-09-17「点侧栏『新建会话』，确认后侧栏新会话高亮了，但窗口没切过去，必须再点一下新会话」根治）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — Goal 实时链路（2026-09-05 "agent set goal complete 后前端样式不更新"根治）：goal 状态是【会话级】状态，与 todos 同模式经 TDSM 流转。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 用户从 UI 编辑 goal / todos 必须【立刻】生效，不得等后端 push —— 乐观覆盖 = `usePendingEdit`（2026-09-12 用户报告"编辑 goal 按 Enter 后恢复编辑前内容、刷新才生效"，todos 同类）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — `interruptMode` 必须随 busy=false 自动重置（2026-09-05 "插话/排队 UI 不随 busy→idle 切换"根治）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 上传进度 + 媒体插入 + 下载端点（2026-09-06 "粘贴文件卡住 spin 很久 + 上传成功后 tiptap 插入媒体"）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 附件引用绝不重复追加（2026-09-11 用户报告「web 粘贴图片后发送，图片变成两张」）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 非图片附件的下载引用 = 单一实现 `uploadDownloadRef`，绝不把内部失败文案写进用户可见正文（2026-09-20 用户 P0：「本地静态存储下上传 15.gz，消息里出现 `📎 [用户上传文件: 15.gz] (获取下载链接失败)`」）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — tiptap 输入框（MessageInput）链接/上传四件套（2026-09-05 "链接不可见/文字吸入超链接/上传类型限制"根治）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 注入型（synthetic）通知工具的卡片渲染契约（2026-09-11）：`tools.SyntheticToolHints` 是唯一载体，走 `ToolHints` 通道（模型不可见）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 内置/注入工具的渲染必须「超级 fancy + 信息量大」，且绝不显示内部 snake_case 名字（2026-09-12 用户反馈"做的太烂了…一点信息量都没有，还丑"）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 注入型工具卡片的呈现优先级：输出 > 命令；
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 注入型工具必须「事件化 + 承接说明 + 完成徽标」，且可见文案绝不能被测试断言（2026-09-12 用户两次反馈："友好名字不好看，有点奇怪" / "怎么让用户立刻意识到这是之前后台任务/subagent 结束了？"）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 注入型工具必须走「统一字段 + markdown 渲染」，不许自造 payload 特例（2026-09-12 用户："别的 tool 怎么渲染的？shell 那种？用统一的字段" / "我要你直接渲染它输出的 markdown"）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — shell 后台任务提示必须带参数化 task_id（防 `bg:` 前缀污染）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — `filterPanels`（useTabManager.ts）产出的 `grid.root` 必须保持 branch —— dockview `fromJSON` 断言 "root must be of type branch"。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 触屏的行内操作：多个操作必须折叠成「一个 ⋯ + 菜单」，不能并排常显；
- ⚠️ `docs/agent/gotchas-web-frontend.md` — i18next 26 的默认插值是 `{{ }}` 双括号 —— 单括号 `{name}` 不会被替换，会原样渲染给用户（不报错、不警告）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — i18n 占位符必须与 `t()` 调用点传的参数名逐字一致 —— 名字对不上时 i18next 不报错、直接渲染字面量模板（2026-09-17 用户报告："删除会话的时候弹窗内容有问题，看上去是占位符没有实际被替换掉"）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — radix `ScrollArea` 的 Viewport 内部有 `display:table; 
- ⚠️ `docs/agent/gotchas-web-frontend.md` — `history_replaced` step 1 的 "live 胜" 分支必须 union incoming 迭代（2026-08-29 "重启后 turn 的 iter 1..k 全消失"根治——竞态根因）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — StagingTray queue 残留已 dequeue 的消息（2026-09-04 "切 tab 时 user msg 已 dequeue 但 web 仍显示"修复）：`AgentPanel` 的 queue hydration effect deps 只含 `[chatID, messageChannel, isSubAgent]` —— 切 tab（SSE 断开）期间后端 dequeue 发出的 `queue_state` 事…
- ⚠️ `docs/agent/gotchas-web-frontend.md` — user msg 消失（2026-09-04 "不断背诵出师表"场景根治）：SSE 丢 turn_started/user_echo 后 committed turn 的 user 永缺 —— AgentPanel 检测最新 committed turn 无 user 行 → 程序化 reload 嫁接 DB user（"强刷恢复"的等价物）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — "思考 N 字"必须显示真实字符数（2026-09-04 "committed 之后数字不对"修复）： reasoning label 曾用 `Math.ceil(text.length / 4)` 估算（670 字符显示"思考 167 字"）—— 与真实值语义分裂。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — TODO 可编辑 + 一键设为 goal（2026-09-11）：后端 `set_todos` 是唯一写入口，前端不做本地副本（单一数据源）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — todos 的 key 必须统一用 canonical sessionKey（RootSessionKey），禁用 physicalChannel override 的 SessionKey —— 上一条「buildToolExecutor 补 SessionKey(override)」只修了一半。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 同一原则的第三实例：`buildToolContext` 的 `BgSessionKey` 也必须用 canonical key —— 否则后台任务通知创建重复 tenant（"会话变两个 + cancel 后 busy + 历史丢失"）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — web 端「Reconnecting…」卡死不重连的根因：watchdog 只在 onopen 启动，首次 connect() 失败时（onopen 从未触发）无任何主动重连兜底。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — `text_final` 的 finalText 属于【进行中迭代】（`max(live.iter, 已完成迭代最后号)`），AskUser cancel 时若该迭代未完成必须【追加】新迭代，绝不能覆盖上一个已完成迭代 —— "askuser 取消后迭代渲染混乱顺序错乱"根治。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — `buildMessageRows` exactDup MUST require `m.turnID===0 || live.turnID===0` — 
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 待发队列拖动调序（2026-09-11）：队列的【顺序权威】是 shadow queue（`bgSessionState.queue`），不是 msgCh —— 而 msgCh 是普通 Go channel（FIFO，无法就地重排），所以调序必须把新顺序【投影】回 msgCh。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 前端 dispatch 队列状态必须用持有 ChatStore 的那个 hook —— `useChatMessages` 的 `store` 是 `MessageStore`（没有 `dispatch`）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — Stream events carry Iteration; 
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 上条的另外两个形态（2026-09-18 用户复测 trace 6.gz 时抓出，其中①是我自己引入的回归）：① "退化回退路径"绝不能放在每帧都跑的 effect 里 —— `MessageList` 贴底 effect（依赖含 `liveProgress` ⇒ 每流式帧跑）曾写成「内存数字可用则用、否则回退读 `scrollHeight/clientHeight/scrollTop`」，而内存判据漏算了内容原点（容器 padding…
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 脏标记必须下沉到 chunk：长 turn 的每帧代价必须与 N 无关（2026-09-18 用户报告「高度估算算得慢 / async 计算导致抖动 / 理论上只要计算倒数的几十个迭代…应该快如闪电」根治）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 窗口化的「首帧竞态」：ref 回调先于观察器创建 → 首个 commit 挂载的块永不被观测（2026-09-13「加载的历史消息长了就卡 / 开侧边栏慢五六倍 / iter 特别多」根治，+13 行）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 冻结（窗口化卸载内容）的唯一凭据是「内容实测复核通过」——「瞬态小高度」与「复核量到占位本身」都会造成永久空块（2026-09-13 `81bbb195` 回归根治）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — loadMore（向上翻页）契约：「一次手势 = 一次请求」，触发权必须由 IO intersection 状态机管理（2026-09-13「翻页要请求十几次才真的加载到下一页」根治）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 任何新包裹层都必须 `min-w-0`（否则断掉 pill 收缩链 ⇒ 手机端退化成一行一个）：2026-09-15 我新增的 `CopyTarget`（右键/长按复制入口）在 tools 级渲染的是无 class 的裸 div —— flex 子项默认 `min-width: auto`，拒绝收缩到内容宽度以下 ⇒ 长参数把整行撑满，`tool-pill-width.spec.ts` 的两条守护（390px/320px）当场红。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 渲染代价必须与 turn 内迭代数无关（浏览器侧）：迭代块 containment（2026-09-13 trace 111.gz，用户要求「随着 turn 里的 iter 增加，性能没有任何下降」）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 流式帧代价必须与 turn 长度无关（2026-09-12 Trace-20260912T100816，用户要求"前端 performance 和 agent turn 长度完全无关，永远是一个常数"）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 2026-09-13 回归：性能修复打在旧管线（MessageStore），而渲染早已切到新状态机（`chat/reduce+derive+integrate`）—— 长 turn 卡顿复发，根因是「管线错配」。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 命令行的【位置】= 按「时间锚点」插回原位，绝不固定沉底（用户 2026-09-21：「现在所有的 `!cmd` 内容（包括输入和输出）会固定挂在会话底部，能不能按消息顺序展示在消息列表中？」）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — `!cmd` 的输入/输出必须【落库】—— 否则刷新即消失（用户 2026-09-21：「为什么 !cmd 消息的输入输出在页面刷新之后就消失了？」）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 乐观 busy 只能用于「会开启 turn」的发送（2026-09-17 用户报告「`!pwd` 输出下方多出一个『思考中』」根治）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 滚动掉帧根治（2026-08-29 Trace-181624，bundle 直读验证——⚠️ sourcemap unminify 对 rolldown 合并 chunk 有段错位，坐标必须 awk bundle 直读二次确认）： ① observeElementOffset rAF 合帧（MessageList `rafCoalescedObserveElementOffset` 模块函数）——TanStack 默认实现每个 scro…
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 触屏设备禁 layout-attribute 动画（2026-09-07 手机端 todo 面板展开掉帧根治）：`@media (hover: none)` 下 `.fold-container` 只保留 opacity 过渡、`.collapsible-motion` 高度 keyframes 关闭。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — Foreground shell promote-to-background（2026-09-07，"执行中的 shell 用户可以手动转后台"）。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — `BackgroundPanel` xterm mounting: uses `useState` callback ref (`setContainer`) not `useRef` — 
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 为什么当年错了：实测单 turn 最多 1,661 个迭代（≈3.6MB）确实让历史加载随迭代数线性变长 ⇒ 三处"压体积"各截一刀。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 内置工具必须在【两处】都登记 —— 漏一处就看起来像"未分类的未知工具"（用户 2026-09-19：「我说这个折叠版本的 icon 你搞好看点」，针对 `share_file` 的折叠 pill）：① `toolIcons.tsx` 的 `TOOL_ICON_MAP`（缺失 ⇒ 落到 `FALLBACK_ICON = Wrench` 通用扳手）；
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 长 JSON 参数简化：`formatParam()` 把 `{"task_id": ["3f8f492a"]}` 抽成 `task_id: 3f8f492a`（≤2 键；
- ⚠️ `docs/agent/gotchas-web-frontend.md` — i18n key 名 `collapseAll` / `expandAll` 是禁词：旧「折叠级别」特性用过 `collapseAll`，`noLegacyFoldFormat.test.tsx` 的 `FORBIDDEN_CODE`（`collapseAll:`）与 `DEAD_KEYS` 会直接红 ⇒ 本特性用 `collapseAllGroups` / `expandAllGroups`（任何新 key 命名先避开 `collap…
- ⚠️ `docs/agent/gotchas-web-frontend.md` — `i18n.t()` 的期望值与浏览器渲染的语言必须显式对齐（2026-09-15 CI 红灯根因）：spec 里 `import i18n from '@/i18n'` 的实例跑在 Node（无 `navigator`/`localStorage` ⇒ 回落 `DEFAULT_LOCALE` = zh-CN），而 app 在浏览器里按 `navigator.language`（Playwright 默认 `en-US`）渲染 en ⇒…
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 菜单/面板必须 `createPortal(..., document.body)`（React 19 从 `react-dom` 导入）：虚拟行用 `transform: translateY(...)` 定位（`MessageList.tsx`），CSS 下 `position: fixed` 的包含块会变成最近的被 transform 的祖先，再叠加 `.virt-row{contain:layout}` / `.iter-blo…
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 守护：`web/e2e/msg-actions.spec.ts`（迭代目标数 == 迭代数、右键某迭代只复制该迭代、工具级逐项复制、触屏长按面板贴视口底部且项数 == 工具数+1、`[data-testid="msg-actions"]` 必须为 0 —— 即"不再有悬浮条"、右键链接 → 打开链接（断言新标签最终 URL）+ 复制链接地址 + 有选区 → 复制选区）+ `MessageActions.test.tsx`（白名单 / 落…
- ⚠️ `docs/agent/gotchas-web-frontend.md` — loading 屏幕只在「确实有会话、历史尚未到达」时遮挡面板 —— 无会话时必须交出输入区（2026-09-15 CI 事故）：`AgentPanel` 的闸门必须是 `(chat.historyReady === false && !!chatID) || resumeLoading`，不能只判 `historyReady`。
- ⚠️ `docs/agent/gotchas-web-frontend.md` — 锁屏恢复后 busy 会话必须从 `active_progress` 恢复 live —— 即使 DB 快照已把「在跑的 turn」折成 committed（2026-09-18 用户报告「手机端 busy 会话熄屏再打开：loading 后明明 busy 却看不到最新进度且不再更新」）。
- ⚠️ `docs/agent/gotchas-feishu.md` — 文件存储（本地 static / 云 OSS）必须能在 Web 设置里配置，且「schema / provider 构建 / 掩码」三处各自单一来源（2026-09-18 用户要求「web 端设置里加上配置云存储」）。
- ⚠️ `docs/agent/gotchas-feishu.md` — Web 设置 → 渠道面板（内置 + 用户注册插件渠道统一管理，含一键飞书绑定）。
- ⚠️ `docs/agent/gotchas-feishu.md` — 飞书进度 = CardKit 流式卡片，形态对齐 Web（每迭代 T→O→C），无 header。
- ⚠️ `docs/agent/gotchas-feishu.md` — 显式工作目录不存在时【自动创建】（2026-09-20 用户要求「创建新会话时如果选择了一个不存在的目录则自动创建」）。
- ⚠️ `docs/agent/gotchas-feishu.md` — 每迭代 TTFT/TPOT 导出 + DB 迭代表记录（schema v58）+ 优雅退出导出 in-flight stream 内容。
- ⚠️ `docs/agent/gotchas-feishu.md` — iteration_history SELECT 加列必须同步【全部】Scan 路径——`GetIterationHistoryByTurns` 有自己的内联 Scan，不走 `scanIterationRecords`（v59 事故：SELECT 14 列只 Scan 11 个 → rows.Scan 报错被 `continue` 静默吞掉 → 批量查询恒返回 0 行，TestGetIterationHistoryByTurns 红灯…
- ⚠️ `docs/agent/gotchas-feishu.md` — TTFT 三重根因（2026-08-29 "tool 生成完毕后 ttft 突变成很小数值 + 记录全是错的 + tool-only 迭代 ttft 虚高"）——三处基准/锚点不一致，修复后 TTFT 全链路统一为「请求发出时刻 → 首个流事件」。
- ⚠️ `docs/agent/gotchas-feishu.md` — 输出 token=0 双根因（2026-08-29 "输出 token 计算有问题，有时候是 0，tool 的 sse 没计算"）——DB 实证：tool 迭代 tokens=0 但 input_tokens/cached_tokens 正常（usage chunk 到达且 PromptTokens>0 guard 通过），tokens>0 的行偏小。
- ⚠️ `docs/agent/gotchas-feishu.md` — 输出 token=0 真正主因（第三根因，2026-08-29 晚补充）：`snapshotCompletedIteration` 的 tracker-delta 语义错误——`TokenTracker.RecordLLMCall` 是【覆盖】语义（`completionTokens = completion`，每次调用覆盖为该次 API 返回的 per-call 值），delta 计算（`cur - lastSnapshotCompl…
- ⚠️ `docs/agent/gotchas-feishu.md` — live tok/s 短流 fallback（2026-08-30 "tok/s 有计算问题，在只有一两个 sse 的时候"）——`liveStats` 的 1 秒滑动窗口要求 ≥2 样本且间隔 ≥200ms，短流（1-2 个 SSE chunk，间隔 <200ms）永远不成型 → tok/s 恒 0（尽管 tokens 在流动）。
- ⚠️ `docs/agent/gotchas-feishu.md` — SubAgent Run 必须分配 per-session TurnID（assignSubAgentTurnID）—— 否则持久化全 turn_id=0，前端 turn↔迭代关联断裂。
- ⚠️ `docs/agent/gotchas-feishu.md` — 后台 subagent 进度泄漏的第二盲区：Run 结束后 stale 检查失效 —— runDone 关闭。
- ⚠️ `docs/agent/gotchas-feishu.md` — 撤销 resetProgress 的 frozen 检查 —— frozen 快照在 commit 后必须 reset（否则残留到下一 turn → 重复渲染）。
- ⚠️ `docs/agent/gotchas-feishu.md` — `MessageStore.toRows()` 的 frozen 合并分支必须输出 `isPartial: true`（V5）——否则 cancel 后正在执行的 tool（最新 iter）从 UI 消失。
- ⚠️ `docs/agent/gotchas-feishu.md` — turn_id / iteration invariants — 
- ⚠️ `docs/agent/gotchas-feishu.md` — "思考中"占位符唯一渲染点 = LiveIteration（2026-09-04 "切换会话后 agent 消息完全空白 + 渲染两个思考中"双 bug 根治）。
- ⚠️ `docs/agent/gotchas-feishu.md` — 压缩期间（`progress.phase === 'compressing'`）不渲染思考占位符 —— 每个状态下有且只有一个状态指示器（2026-09-15 用户报告截图：`thinking…` 叠在 `Compressing context…` 上方，"这有点怪"）。
- ⚠️ `docs/agent/gotchas-feishu.md` — 虚拟行（`.virt-row`）上的动效绝不允许改 `transform` —— CSS 动画会覆盖内联定位（2026-09-16 用户报告「快速滚动出现内容抖动重叠」根治）。
- ⚠️ `docs/agent/gotchas-feishu.md` — `shouldAdjustScrollPositionOnItemSizeChange` 的「完全在视口上方」判据必须用 DOM 真相，不能用 virtualizer 的 item 坐标（2026-09-15 修 `stream-jitter.spec.ts` 长期 flaky 的根因）。
- ⚠️ `docs/agent/gotchas-plugins.md` — 插件会话身份协议（2026-09-06 "刷新后 git 面板显示不是 repo"根治）：`window.__xbot_session__` 是插件 RPC 注入 cwd 的唯一身份源，绝不能伪造兜底身份。
- ⚠️ `docs/agent/gotchas-plugins.md` — 插件 editor-view API（VSCode webviewPanel 语义，§5.4）。
- ⚠️ `docs/agent/gotchas-plugins.md` — Ambience store 高频 emit 风暴（2026-08-29 桌宠事件事故，三条防御缺一不可）。
- ⚠️ `docs/agent/gotchas-plugins.md` — Ambience 桌宠事件源 = SSE 桥（emitPluginEvent），不是 web_plugin_event。
- ⚠️ `docs/agent/gotchas-plugins.md` — Ambience 渲染静态成本三禁令（2026-08-29 第二次性能事故——React emit 频率修复无效后的真根因）。
- ⚠️ `docs/agent/gotchas-plugins.md` — VSCode 式布局定制系统（`web/src/plugin-runtime/layoutTypes.ts` + `layoutRegistry.ts`）：命名布局槽位 + 用户/插件可移动 UI 元素。
- ⚠️ `docs/agent/gotchas-plugins.md` — 内置插件视图必须静态 import，禁止动态 `import()` 加载（React #311 黑屏根因）。
- ⚠️ `docs/agent/gotchas-plugins.md` — 手机端设置 Sheet（SettingsDialog）必须做 `sm:` 断点适配——桌面类无断点会挤爆手机内容区（2026-08-29 "手机 LLM 配置 tab 超出屏幕"修复）。
- ⚠️ `docs/agent/gotchas-plugins.md` — 一个会话至多被一个 agent 面板渲染（session-per-tab 不变量）——「切会话后同一 user 行重复渲染」根治（2026-09-16）。
- ⚠️ `docs/agent/gotchas-plugins.md` — 飞书进度渲染 = 飞书原生 CoT（「思考过程」），对齐 dsh-lark（2026-09-16 用户要求：「飞书模仿 dshlark 改成飞书原生 cot，飞书的渲染功能和 dshlark 对齐」）。
- ⚠️ `docs/agent/gotchas-plugins.md` — Runner 体系已单用户化 + 会话级绑定（2026-09-17 重构，schema v69）。
- ⚠️ `docs/agent/gotchas-plugins.md` — 内置插件 `xbot.ssh-runner`（SSH 自动纳管远程机器）—— 控制面插件，零主模块耦合；
- ⚠️ `docs/agent/gotchas-plugins.md` — 本地 docker sandbox 已整体删除；
- ⚠️ `docs/agent/gotchas-plugins.md` — `/goal` 只能在后端 pop 并执行该消息后设置 —— 发送路径禁止乐观写 goal（2026-09-16 用户报告：「发 goal 消息时（点 goal 按钮），即使消息排队也会立刻设置 goal；
- ⚠️ `docs/agent/gotchas-llm.md` — 跨轮次持久化：`session_messages.reasoning_items`（schema v66 + `migrateV65ToV66`，`tableExists`+`columnExists` 双守卫幂等）+ `appendMessageWith` 写入 / `getHistoryFromWith` 读回。
- ⚠️ `docs/agent/gotchas-install-worktree.md` — `setup` 能力探针必须只有一处实现，且绝不能用管道喂 grep（2026-09-11 事故：装 stable 静默变成 nightly）。
- ⚠️ `docs/agent/gotchas-install-worktree.md` — channels 激活修复是 manifest 驱动 + set_if_missing + builtin-only 边界：`fixChannelActivationConfig`（setup.go）只扫 `plugins/builtin/`（setup 自己装的 release 插件）的 plugin.json `contributes.channelProvider.config_schema[].{key:"enabled", d…
- ⚠️ `docs/agent/gotchas-install-worktree.md` — release.yml 产物矩阵：frontend job 追加 esbuild 插件 web 资产 —— 每个声明 `web.entry` 的插件都要构建，esbuild 产物按插件目录形状落盘 `build/plugin-web/<plugin-id>/web/`（`--splitting` 让该插件的 view 入口共享同一份 rpc/ui 单例 chunk；
- ⚠️ `docs/agent/gotchas-install-worktree.md` — plugins/package.sh 通用打包：遍历 `plugins/*/plugin.json`（源目录用连字符 `xbot-genui`，插件 ID 用点 `xbot.genui`——tarball 内是 ID 布局，脚本维护 src-dir→ID 映射）；
- ⚠️ `docs/agent/gotchas-install-worktree.md` — PR CI 覆盖插件模块（独立 `plugins/*/go.mod`）：根模块的 `go build ./...`/`go test ./...` 永远到不了它们（嵌套 module 被 Go 排除），所以此前插件编译失败或自身测试失败只在 release 的 build-plugins job 才暴露（= 发布时）。
- ⚠️ `docs/agent/gotchas-misc.md` — interactive SubAgent 寻址必须经唯一解析器（2026-09-17「全收口」）：所有按 `(role, instance)` 寻址的入口（`SendToInteractiveSession` / `InspectInteractiveSession` / `InterruptInteractiveSession` / `UnloadInteractiveSession` / `ContinueInteractiveSe…

## Development Principles

### Never Estimate Tokens

**任何时候禁止估算 token——永远不可以用假的 token 数做决定。我们有收集 usage。**（2026-09-02 用户指令，compression verbatim 预算检查 CJK 低估 33% 事故）

- **LLM API 返回的 `usage.prompt_tokens` / `completion_tokens` 是唯一权威数据源**（agent 侧经 `TokenTracker.GetPromptTokens()`；`maybeCompress` 的触发判断就是范例——真实 API 值 vs 阈值）
- `chars×2/3`、`chars/3`、`msg×200` 这类换算在 CJK 内容下**低估 33%+**（CJK ≈ 1 token/char，换算假设 1.5 chars/token）——基于估算的预算检查会放行实际超限的请求 → LLM input-too-long 报错冒泡而非走 fallback
- **估算只允许出现在无损展示**（日志、进度条、粗略提示），**绝不允许出现在决策路径**（预算检查、路径选择、触发判断、达标验证）
- 无真实数据的路径（如 `handleInputTooLong`——错误响应不含 usage）→ **走保守 fallback**（bounded 路径），不用估算冒险
- 修 bug 或加功能时遇到"要不要给这个请求/上下文算 token"的问题：答案永远是找 `TokenTracker` / usage 事件 / RunConfig 里的真实值往下传；拿不到就重构传值链路（加参数），不要就地换算

### Never Blame the User's Binary

**永远不假设用户用了旧二进制。** 如果怀疑版本问题，说明自己的排查逻辑有漏洞，不是用户的问题。

### Always Reproduce Before Fixing

**修复任何 bug 之前，必须先写测试复现问题。** 没有复现测试的修复是盲修——你不知道自己是否真的修了根因，也不知道未来是否会回归。

#### 流程

1. **写测试复现** — 单元测试（Go `test` 或前端 `vitest`）或 E2E 测试（Playwright）。测试必须 **先失败**（红灯），证明 bug 确实存在。
2. **分析根因** — 理解为什么会产生 bug，不要只看症状。
3. **最小修复** — 只改必要的代码，修复后测试变绿。
4. **验证无回归** — 运行完整测试套件，确保修复没有引入新问题。

#### 为什么重要

- 没有复现测试 = 没有证据证明 bug 存在，也没有证据证明修复有效
- 偶发性 bug（竞态、时序、网络）尤其需要测试——手动验证不可靠
- 测试是活文档——未来开发者能从测试理解 bug 的触发条件
- 防止"修复"引入回归——测试在 CI 中持续守护

#### 对前端 UI bug

- 优先用 Playwright E2E 测试（`web/e2e/`），用 `page.route()` mock 后端，不需要真实服务器
- 滚动、渲染、状态等视觉行为必须用 E2E 验证，单元测试无法覆盖 DOM 层面

### Always Prefer Explicit

**核心原则：永远优先使用显式 API，避免隐式假设。**

本项目遵循 "always prefer explicit" 开发原则。大量 bug 源于隐式设计——调用者无法从 API 签名推断出所有必要参数或行为。

#### 具体实践

1. **避免直接使用结构体作为公共 API 参数**
   - ❌ `func NewFoo(cfg FooConfig) *Foo` — 调用者可能遗漏 `FooConfig` 中的关键字段
   - ✅ `func NewFoo(opts ...FooOption) *Foo` — 使用私有结构体 + 构造函数 + 显式 Option 模式
   - ✅ `func NewFoo(required string, optional ...string) *Foo` — 必填参数显式列出

2. **假设调用者只看到你的 API 签名**
   - 调用者没有义务阅读实现细节
   - API 签名应自解释：参数名、类型、顺序应清晰表达意图
   - 使用 `// WithXxx` 风格的 Option 函数提供可选配置

3. **宁可冗长，不要隐晦**
   - 5 个显式参数优于 1 个包含 20 个字段的结构体
   - 如果必须用结构体，确保必填字段在构造函数中强制提供
   - 使用 `Must` 前缀函数（如 `MustParse`）在编译期捕获错误

4. **文档即合同**
   - 每个公共函数/类型必须有 godoc 注释
   - 注释应说明 "什么" 和 "为什么"，而不仅仅是 "如何"
   - 参数约束（如 "must not be empty"）应在注释中明确说明

#### 为什么重要

- 减少运行时 panic 和零值 bug
- 提高代码可读性和可维护性
- 让新贡献者能快速理解 API 用法
- 编译器帮你捕获更多错误

## Project Context

`ProjectContextMiddleware` auto-loads this file into system prompt. After code changes, update the relevant Knowledge Files to keep documentation in sync.

**⛔ 注入 system prompt 的项目上下文文件**必须**有硬预算（`formatProjectContext` → `maxProjectContextChars` = **100k 字符（rune，不是字节）** + 截断提示里**明确要求模型把文件缩到预算内**）；`formatGlobalContext` 同规则、同一常量。** 2026-09-23 之前本文件是 **690 KB / 500k 字符（≈180k tokens）**且被**无上限**注入 —— 于是 **system prompt 单独就吃掉几乎整个 200k 上下文窗口**，而压缩只重写**消息**、永不改 system prompt ⇒ 每次压缩都"无效"（不可压缩部分本身就超线）⇒ 自动压缩每 5 迭代空转一次、模型在病态 prompt 下反复复读工具调用数小时（真实事故：`maybeCompress` 触发 146 次、`Compaction ineffective ... reduction=-68%`、单 turn 724 次 LLM 请求；用户看到的是「上下文超 200k 却不压缩 + 一直复读」）。触发路径：会话一开始 CWD 不在仓库（system ≈32k），**agent `Cd` 进仓库后** `ProjectContextMiddleware` 从 `mc.CWD` 读到本文件 → system 暴涨到 690 KB。**PR #95 曾把 `formatProjectContext` 的截断整段删掉（并留了 `never_truncates` 测试当契约）——那是错的**：用户内容的无界注入不是"保真"，是让模型窗口破产。**同日已按项目自己的设计把细节整段搬进 `docs/agent/gotchas-*.md`（逐字保留），本文件缩到 ~65k 字符**。⚠️ **单位（别再算错）**：tokenizer 大约 **CJK 1 token/字符、ASCII 1 token/4 字符**；CJK 字符 = 3 UTF-8 字节 ⇒ **字节数 ≠ 字符数（差 3 倍）**，预算一律按 **rune** 计。守护：`agent/project_context_size_test.go`（500k 字符 AGENTS.md ⇒ 注入 ≤ 预算+包装、尾部 sentinel 绝不泄漏、CJK 截断 rune 安全、拼装后 system prompt ≤ 预算+8k；含"预算内文件必须全文注入"）+ `middleware_builtin_test.go` 的 `truncates_huge_content_with_read_hint`。

**⚠️ 每次代码改动必须同时维护 docs-site 文档站点。** 项目有三层文档，改动后要同步对齐：
1. **AGENTS.md**（本文件）— 关键 gotcha 内联在这里，保持全局可见。
2. **`docs/agent/`** — 内部知识文件（agent 用 Read 按需查阅）。
3. **`docs-site/content/{en,zh-cn}/`** — 面向用户的 Hugo 文档站点（GeekDoc 主题，中英双语）。改动了 **公共 API、插件系统、配置、工具、setup 流程** 时必须找到对应页面（`plugins/`、`tools/`、`getting-started/` 等）同步更新**两个语言版本**，不能只更新内部知识文件。漏掉 docs-site 会让用户读到过时文档，和代码不同步。

### No Hacks, No Fallbacks, No Defensive Programming

**禁止 hack、兜底逻辑和防御性编程。从根源保证正确，而非叠加防护层。**

这一原则是 TUI 渲染系统多年 bug 修复的教训总结。大量 bug 源于"加一层防御"而非"修根因"——每层防御本身引入新的边界条件，最终形成难以维护的防御栈，反而制造更多 bug。

#### 核心规则

1. **从根源修复，不加补丁**
   - ❌ 数据在传输中丢失 → 加 dedup 函数去重丢失后的重复
   - ✅ 数据在传输中丢失 → 修复传输层使数据不丢失
   - ❌ 状态可能陈旧 → 加 `alreadySnapped` 检查覆盖陈旧场景
   - ✅ 状态可能陈旧 → 修复状态管理使陈旧不可能发生

2. **不写兜底链**
   - ❌ `if a == "" { a = b }; if a == "" { a = c }; if a == "" { a = d }` — 四级 fallback 链
   - ✅ 确定唯一权威数据源，只读那一个
   - 如果数据可能因 coalescing/并发丢失，修复 coalescing/并发设计，而非加 fallback

3. **不写防御性检查**
   - ❌ `if prev != nil && prev.Iteration == expected && len(prev.ActiveTools) > 0` — 三重前提条件
   - ✅ 保证调用前置条件由架构不变量保证，函数只处理正常路径
   - 如果前置条件可能不满足，那是调用方的 bug，应在调用方修复

4. **不写性能损害型防护**
   - ❌ 每次 updateViewportContent 全量扫描 messages 做去重（O(N) per frame）
   - ✅ 保证写入路径不产生重复（O(1)，写入即唯一）
   - ❌ 每次 snapshot 分配 map 做去重
   - ✅ 数据源保证无重叠，不需要去重

5. **审计现有防御层**
   - 修改代码时，检查周围的防御性代码是否因根因修复而变得多余
   - 多余的防御代码必须一并删除——它们不是"安全网"，是噪声
   - 每一层防御都应该有明确的 bug ID 或场景说明为什么需要存在

#### 具体到 TUI 渲染

- **progressSlot coalescing**（原 progressCh buffer=1 已替换）：`SendProgress` 使用 mutex-protected `progressSlot` + buffer-1 `progressSignal` 替代旧 buffer-1 channel。旧设计中 eviction+re-insert 有 race window，结构化的 "done" 事件可被静默丢弃，导致工具永久卡在 "running"（● 不变 ✓）。新设计：`progressSlot` 始终持有最新合并后的事件，`handleProgressDrain` 读取并清空 slot。结构化事件永远不会丢失——stream-only 不能驱逐 structured，structured 替换时保留 stream fields/TokenUsage/CWD。`coalesceProgress`（asyncCh 层）仍保留用于合并连续 progress 消息。
- **snapshotIterationChange**：引擎保证 iteration 切换时 `snapshotCompletedIteration` 已执行（ActiveTools → CompletedTools）。`mergeProgressState` 在合并前做浅拷贝保护 prev，snapshot 直接从 prev 读取所有字段，无需 fallback 链。
- **handleProgressDone**：PhaseDone 事件经过 progressFinalizer，CompletedTools 包含全部工具。按 `lastIter` 过滤 CompletedTools 防止跨迭代工具污染。无 `lastCompletedTools`、无 `pendingToolSummary`——iterations 直接保留在 `progressState.iterations` 中供 `handleAgentMessage` 读取。
- **turnDoneFlags/pendingToolSummary 已删除**：用单一 `replyProcessed bool` 替代。`handleProgressDone` 完成后 iterations 保留在 `progressState` 中（endAgentTurn 不清除），`handleAgentMessage` 直接读取。队列刷新用 `!m.replyProcessed` 守卫 dead window。
- **新用户消息开始前必须固化旧 streaming turn。** `PhaseDone` 后 `endAgentTurn` 会刻意保留 `streamingMsgIdx` 和 `progressState`，等待最终 `handleAgentMessage` 做无闪烁收尾。但 `sendMessage`/`sendToAgent` 会在 `startAgentTurn()` 前先 append 用户消息并刷新 viewport；如果不先调用 `finalizeStaleStreamingBeforeNewUserMessage()`，这次刷新会把上一个 turn 的 live stream 渲染到新用户消息下面。真实新消息入口必须在 append user message 前固化旧 assistant partial、清 `streamingMsgIdx`/旧 progress stream cache。**`startAgentTurn()` 内部也调用 `finalizeStaleStreamingBeforeNewUserMessage()`**，覆盖所有不经过 `sendMessage`/`sendToAgent` 的路径（auto-start、bg task 注入、`/compress`、`cliProcessingMsg`、AskUser 回调）。已有调用的路径因 `streamingMsgIdx=-1` 走 early return，无副作用。
- **guide 颜色跳变已修复**：`updateStreamingOnly` 在 `!m.typing` 时立即使用 `DimGuideSt`，消除 PhaseDone→handleAgentMessage 之间的 bright→dim 跳变。
- **GotoBottom 统一守卫**：所有 `GotoBottom()` 调用通过 `!m.userScrolledUp` 守卫。慢速路径（fullRebuild 后）不再无条件强制滚动。
- **handleCancelAck 必须 `updateViewportContent()`**：cancel ack 更新缓存后必须立即推送到 viewport，否则 viewport 显示 stale streaming 内容直到下次 tick（用户感知为"历史消失再重现"）。
- **renderTurnBody 去重**：同一 LLM response 文本从 `iter.Thinking`（ThinkingContent）和 `fallbackContent`（msg.content）两条路径渲染。精确匹配去重是正确做法——它们源自同一数据，必然相等。前缀匹配/百分比阈值是 hack
